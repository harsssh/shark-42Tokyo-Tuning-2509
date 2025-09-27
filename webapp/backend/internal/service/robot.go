package service

import (
	"backend/internal/model"
	"backend/internal/repository"
	"backend/internal/service/utils"
	"context"
	"log"
	"sort"
	"strconv"
	"sync"
	"time"
)

const (
	shippingPoolTarget       = 200
	shippingReplenishBatch   = 50
	defaultRobotCapacityHint = 100
	lightweightCacheTTL      = 30 * time.Second
)

type RobotService struct {
	store *repository.Store

	lightweightMu        sync.RWMutex
	lightweightCache     []model.Product
	lightweightCacheExp  time.Time
	lightweightCacheHint int
}

var (
	dpPool = sync.Pool{
		New: func() interface{} {
			return make([]int, 0)
		},
	}
	choicePool = sync.Pool{
		New: func() interface{} {
			return make([]*knapChoice, 0)
		},
	}
)

type knapChoice struct {
	orderIndex int
	prev       *knapChoice
}

func NewRobotService(store *repository.Store) *RobotService {
	return &RobotService{store: store}
}

func (s *RobotService) GenerateDeliveryPlan(ctx context.Context, robotID string, capacity int) (*model.DeliveryPlan, error) {
	var plan model.DeliveryPlan

	err := utils.WithTimeout(ctx, func(ctx context.Context) error {
		return s.store.ExecTx(ctx, func(txStore *repository.Store) error {
			orders, err := txStore.OrderRepo.GetShippingOrders(ctx)
			if err != nil {
				return err
			}
			if len(orders) == 0 {
				if err := s.replenishShippingOrders(ctx, txStore, nil, shippingPoolTarget, capacity); err != nil {
					log.Printf("Failed to replenish shipping orders: %v", err)
				}
				orders, err = txStore.OrderRepo.GetShippingOrders(ctx)
				if err != nil {
					return err
				}
			}
			plan, err = bestSelectOrdersForDelivery(ctx, orders, robotID, capacity)
			if err != nil {
				return err
			}
			if len(plan.Orders) == 0 {
				if err := s.replenishShippingOrders(ctx, txStore, &orders, shippingPoolTarget, capacity); err == nil {
					plan, err = bestSelectOrdersForDelivery(ctx, orders, robotID, capacity)
					if err != nil {
						return err
					}
				}
			}
			if len(plan.Orders) == 0 {
				if fallback := selectFallbackOrder(orders, capacity); fallback != nil {
					plan = model.DeliveryPlan{
						RobotID:     robotID,
						TotalWeight: fallback.Weight,
						TotalValue:  fallback.Value,
						Orders:      []model.Order{*fallback},
					}
				}
			}
			if plan.Orders == nil {
				plan.Orders = []model.Order{}
			}
			if len(plan.Orders) > 0 {
				orderIDs := make([]int64, len(plan.Orders))
				for i, order := range plan.Orders {
					orderIDs[i] = order.OrderID
				}

				if err := txStore.OrderRepo.UpdateStatuses(ctx, orderIDs, "delivering"); err != nil {
					return err
				}
				log.Printf("Updated status to 'delivering' for %d orders", len(orderIDs))
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return &plan, nil
}

func (s *RobotService) UpdateOrderStatus(ctx context.Context, orderID int64, newStatus string) error {
	return utils.WithTimeout(ctx, func(ctx context.Context) error {
		return s.store.ExecTx(ctx, func(txStore *repository.Store) error {
			if err := txStore.OrderRepo.UpdateStatuses(ctx, []int64{orderID}, newStatus); err != nil {
				return err
			}
			if newStatus == "completed" {
				hint := defaultRobotCapacityHint
				if err := s.replenishShippingOrders(ctx, txStore, nil, shippingPoolTarget, hint); err != nil {
					log.Printf("Failed to replenish shipping orders after completion: %v", err)
				}
			}
			return nil
		})
	})
}

func bestSelectOrdersForDelivery(
	ctx context.Context,
	orders []model.Order,
	robotID string,
	robotCapacity int,
) (model.DeliveryPlan, error) {
	n := len(orders)
	if n == 0 || robotCapacity <= 0 {
		return model.DeliveryPlan{RobotID: robotID, Orders: []model.Order{}}, nil
	}

	filtered := make([]model.Order, 0, n)
	for _, o := range orders {
		if o.Weight <= 0 || o.Value < 0 {
			continue
		}
		if o.Weight > robotCapacity {
			continue
		}
		filtered = append(filtered, o)
	}
	if len(filtered) == 0 {
		return model.DeliveryPlan{RobotID: robotID, Orders: []model.Order{}}, nil
	}

	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].OrderID == filtered[j].OrderID {
			return filtered[i].ProductID < filtered[j].ProductID
		}
		return filtered[i].OrderID < filtered[j].OrderID
	})

	return dpSolution(filtered, robotID, robotCapacity)
}

func dpSolution(orders []model.Order, robotID string, robotCapacity int) (model.DeliveryPlan, error) {
	W := robotCapacity

	dpBuf := getDPBuffer(W)
	choicesBuf := getChoiceBuffer(W)

	for i, o := range orders {
		w, v := o.Weight, o.Value
		if w <= 0 || v < 0 || w > W {
			continue
		}
		for cw := W; cw >= w; cw-- {
			alt := dpBuf[cw-w] + v
			if alt > dpBuf[cw] {
				dpBuf[cw] = alt
				choicesBuf[cw] = &knapChoice{orderIndex: i, prev: choicesBuf[cw-w]}
			}
		}
	}

	bestW, bestV := 0, 0
	for w := 0; w <= W; w++ {
		if dpBuf[w] > bestV {
			bestV = dpBuf[w]
			bestW = w
		}
	}

	var (
		picked      []model.Order
		totalWeight int
		totalValue  int
	)
	for node := choicesBuf[bestW]; node != nil; node = node.prev {
		order := orders[node.orderIndex]
		picked = append(picked, order)
		totalWeight += order.Weight
		totalValue += order.Value
	}

	putDPBuffer(dpBuf)
	putChoiceBuffer(choicesBuf)

	return model.DeliveryPlan{
		RobotID:     robotID,
		TotalWeight: totalWeight,
		TotalValue:  totalValue,
		Orders:      picked,
	}, nil
}

func getDPBuffer(capacity int) []int {
	buf := dpPool.Get().([]int)
	needed := capacity + 1
	if cap(buf) < needed {
		buf = make([]int, needed)
	} else {
		buf = buf[:needed]
		for i := range buf {
			buf[i] = 0
		}
	}
	return buf
}

func putDPBuffer(buf []int) {
	for i := range buf {
		buf[i] = 0
	}
	dpPool.Put(buf[:0])
}

func getChoiceBuffer(capacity int) []*knapChoice {
	buf := choicePool.Get().([]*knapChoice)
	needed := capacity + 1
	if cap(buf) < needed {
		buf = make([]*knapChoice, needed)
	} else {
		buf = buf[:needed]
		for i := range buf {
			buf[i] = nil
		}
	}
	return buf
}

func putChoiceBuffer(buf []*knapChoice) {
	for i := range buf {
		buf[i] = nil
	}
	choicePool.Put(buf[:0])
}

func branchAndBoundSolution(ctx context.Context, orders []model.Order, robotID string, robotCapacity int) (model.DeliveryPlan, error) {
	type node struct {
		level    int
		weight   int
		value    int
		bound    int
		included []bool
	}

	var (
		bestValue  int
		bestOrders []model.Order
		steps      int
		checkEvery = 10000
	)

	// 上界計算
	calculateBound := func(n *node) int {
		if n.weight >= robotCapacity {
			return 0
		}

		bound := n.value
		remainingCapacity := robotCapacity - n.weight

		for i := n.level; i < len(orders); i++ {
			if orders[i].Weight <= remainingCapacity {
				bound += orders[i].Value
				remainingCapacity -= orders[i].Weight
			} else {
				// 分数ナップサック
				if orders[i].Weight > 0 {
					bound += (orders[i].Value * remainingCapacity) / orders[i].Weight
				}
				break
			}
		}
		return bound
	}

	var solve func(*node) error
	solve = func(current *node) error {
		steps++
		if checkEvery > 0 && steps%checkEvery == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
		}

		// リーフノード到達
		if current.level >= len(orders) {
			if current.value > bestValue {
				bestValue = current.value
				bestOrders = nil
				for i, included := range current.included {
					if included {
						bestOrders = append(bestOrders, orders[i])
					}
				}
			}
			return nil
		}

		// 現在のアイテムを含めない場合
		nextNode := &node{
			level:    current.level + 1,
			weight:   current.weight,
			value:    current.value,
			included: make([]bool, len(current.included)),
		}
		copy(nextNode.included, current.included)
		nextNode.bound = calculateBound(nextNode)

		// 枝刈り: 上界が現在の最良解以下なら探索しない
		if nextNode.bound > bestValue {
			if err := solve(nextNode); err != nil {
				return err
			}
		}

		// 現在のアイテムを含める場合
		order := orders[current.level]
		if current.weight+order.Weight <= robotCapacity {
			includeNode := &node{
				level:    current.level + 1,
				weight:   current.weight + order.Weight,
				value:    current.value + order.Value,
				included: make([]bool, len(current.included)),
			}
			copy(includeNode.included, current.included)
			includeNode.included[current.level] = true
			includeNode.bound = calculateBound(includeNode)

			// 枝刈り
			if includeNode.bound > bestValue {
				if err := solve(includeNode); err != nil {
					return err
				}
			}
		}

		return nil
	}

	// 初期ノード
	rootNode := &node{
		level:    0,
		weight:   0,
		value:    0,
		included: make([]bool, len(orders)),
	}
	rootNode.bound = calculateBound(rootNode)

	if err := solve(rootNode); err != nil {
		return model.DeliveryPlan{}, err
	}

	var totalWeight int
	for _, order := range bestOrders {
		totalWeight += order.Weight
	}

	return model.DeliveryPlan{
		RobotID:     robotID,
		TotalWeight: totalWeight,
		TotalValue:  bestValue,
		Orders:      bestOrders,
	}, nil
}

func conservativeGreedySolution(ctx context.Context, orders []model.Order, robotID string, robotCapacity int) (model.DeliveryPlan, error) {
	// より保守的なアプローチ: 価値密度ベースを基本とし、複数パターンと比較

	// 1. 基本: 価値密度貪欲（既にソート済み）
	baseOrders, baseValue := greedyByValueDensity(orders, robotCapacity)

	// 2. 高価値アイテム優先戦略
	highValueOrders, highValueValue := selectHighValueItems(orders, robotCapacity)

	// 3. 効率重視戦略（価値密度上位50%に絞って価値順）
	efficientOrders, efficientValue := efficientStrategy(orders, robotCapacity)

	// 最良解を選択
	bestOrders := baseOrders
	bestValue := baseValue

	if highValueValue > bestValue {
		bestOrders = highValueOrders
		bestValue = highValueValue
	}

	if efficientValue > bestValue {
		bestOrders = efficientOrders
		bestValue = efficientValue
	}

	// 軽い局所探索（交換のみ、追加は行わない）
	improvedOrders, improvedValue := lightLocalSearch(ctx, bestOrders, orders, robotCapacity)

	var totalWeight int
	for _, order := range improvedOrders {
		totalWeight += order.Weight
	}

	return model.DeliveryPlan{
		RobotID:     robotID,
		TotalWeight: totalWeight,
		TotalValue:  improvedValue,
		Orders:      improvedOrders,
	}, nil
}

func selectHighValueItems(orders []model.Order, capacity int) ([]model.Order, int) {
	// 価値の高い順に選択
	valueOrders := make([]model.Order, len(orders))
	copy(valueOrders, orders)
	sort.Slice(valueOrders, func(i, j int) bool {
		// 価値が同じ場合は価値密度で判定
		if valueOrders[i].Value == valueOrders[j].Value {
			if valueOrders[i].Weight == 0 && valueOrders[j].Weight == 0 {
				return false
			}
			if valueOrders[i].Weight == 0 {
				return true
			}
			if valueOrders[j].Weight == 0 {
				return false
			}
			ratioI := float64(valueOrders[i].Value) / float64(valueOrders[i].Weight)
			ratioJ := float64(valueOrders[j].Value) / float64(valueOrders[j].Weight)
			return ratioI > ratioJ
		}
		return valueOrders[i].Value > valueOrders[j].Value
	})

	return greedyByValueDensity(valueOrders, capacity)
}

func efficientStrategy(orders []model.Order, capacity int) ([]model.Order, int) {
	// 価値密度上位50%に絞って、その中で価値順に選択
	if len(orders) == 0 {
		return nil, 0
	}

	// 上位50%を選択
	topCount := len(orders) / 2
	if topCount < 10 && len(orders) >= 10 {
		topCount = 10 // 最低10個は見る
	}
	if topCount > len(orders) {
		topCount = len(orders)
	}

	topOrders := orders[:topCount]

	// その中で価値順にソート
	sort.Slice(topOrders, func(i, j int) bool {
		return topOrders[i].Value > topOrders[j].Value
	})

	return greedyByValueDensity(topOrders, capacity)
}

func lightLocalSearch(ctx context.Context, currentOrders []model.Order, allOrders []model.Order, capacity int) ([]model.Order, int) {
	if len(currentOrders) == 0 {
		return currentOrders, 0
	}

	bestOrders := make([]model.Order, len(currentOrders))
	copy(bestOrders, currentOrders)
	bestValue := calculateTotalValue(bestOrders)
	currentWeight := calculateTotalWeight(bestOrders)

	improved := true
	iterations := 0
	maxIterations := 100 // 軽い探索に制限

	selectedMap := make(map[int64]bool)
	for _, order := range bestOrders {
		selectedMap[order.OrderID] = true
	}

	for improved && iterations < maxIterations {
		improved = false
		iterations++

		// タイムアウトチェック
		if iterations%10 == 0 {
			select {
			case <-ctx.Done():
				return bestOrders, bestValue
			default:
			}
		}

		// 交換のみ実行（追加は行わない）
	outer:
		for i, selectedOrder := range bestOrders {
			for _, candidateOrder := range allOrders {
				if selectedMap[candidateOrder.OrderID] {
					continue
				}
				newWeight := currentWeight - selectedOrder.Weight + candidateOrder.Weight
				if newWeight > capacity {
					continue
				}
				newValue := bestValue - selectedOrder.Value + candidateOrder.Value
				if newValue > bestValue {
					bestOrders[i] = candidateOrder
					bestValue = newValue
					currentWeight = newWeight
					selectedMap[selectedOrder.OrderID] = false
					selectedMap[candidateOrder.OrderID] = true
					improved = true
					break outer
				}
			}
		}
	}

	return bestOrders, bestValue
}

func (s *RobotService) replenishShippingOrders(ctx context.Context, txStore *repository.Store, orders *[]model.Order, minPool int, capacityHint int) error {
	var existing []model.Order
	var err error
	if orders != nil {
		existing = *orders
	} else {
		existing, err = txStore.OrderRepo.GetShippingOrders(ctx)
		if err != nil {
			return err
		}
	}
	if len(existing) >= minPool {
		return nil
	}
	need := minPool - len(existing)
	if need < shippingReplenishBatch {
		need = shippingReplenishBatch
	}
	if capacityHint <= 0 {
		capacityHint = defaultRobotCapacityHint
	}
	products, err := s.getLightweightProducts(ctx, txStore, capacityHint, need)
	if err != nil {
		return err
	}
	if len(products) == 0 {
		return nil
	}
	ordersToCreate := make([]*model.Order, 0, len(products))
	for i, p := range products {
		ordersToCreate = append(ordersToCreate, &model.Order{
			UserID:    1 + (i % 100),
			ProductID: p.ProductID,
		})
	}
	inserted, err := txStore.OrderRepo.BatchCreate(ctx, ordersToCreate)
	if err != nil {
		return err
	}
	if orders != nil && len(inserted) > 0 {
		newOrders := make([]model.Order, 0, len(inserted))
		for i, idStr := range inserted {
			id, convErr := strconv.ParseInt(idStr, 10, 64)
			if convErr != nil {
				continue
			}
			newOrders = append(newOrders, model.Order{
				OrderID:       id,
				UserID:        ordersToCreate[i].UserID,
				ProductID:     ordersToCreate[i].ProductID,
				Weight:        products[i].Weight,
				Value:         products[i].Value,
				ShippedStatus: "shipping",
			})
		}
		combined := append(existing, newOrders...)
		sort.Slice(combined, func(i, j int) bool {
			return combined[i].OrderID < combined[j].OrderID
		})
		*orders = combined
	}
	return nil
}

func (s *RobotService) getLightweightProducts(ctx context.Context, txStore *repository.Store, maxWeight, limit int) ([]model.Product, error) {
	if limit <= 0 {
		return []model.Product{}, nil
	}
	now := time.Now()
	s.lightweightMu.RLock()
	if s.lightweightCache != nil && now.Before(s.lightweightCacheExp) && maxWeight <= s.lightweightCacheHint && len(s.lightweightCache) >= limit {
		result := make([]model.Product, limit)
		copy(result, s.lightweightCache[:limit])
		s.lightweightMu.RUnlock()
		return result, nil
	}
	s.lightweightMu.RUnlock()
	fetchLimit := limit
	if fetchLimit < limit*2 {
		fetchLimit = limit * 2
	}
	products, err := txStore.ProductRepo.FindLightweightProducts(ctx, maxWeight, fetchLimit)
	if err != nil {
		return nil, err
	}
	s.lightweightMu.Lock()
	s.lightweightCache = make([]model.Product, len(products))
	copy(s.lightweightCache, products)
	s.lightweightCacheHint = maxWeight
	s.lightweightCacheExp = time.Now().Add(lightweightCacheTTL)
	var result []model.Product
	if len(products) > limit {
		result = make([]model.Product, limit)
		copy(result, products[:limit])
	} else {
		result = make([]model.Product, len(products))
		copy(result, products)
	}
	s.lightweightMu.Unlock()
	return result, nil
}

func selectFallbackOrder(orders []model.Order, capacity int) *model.Order {
	var best *model.Order
	for _, o := range orders {
		if o.Weight <= 0 || o.Weight > capacity {
			continue
		}
		if best == nil || o.Value > best.Value || (o.Value == best.Value && o.Weight < best.Weight) {
			copy := o
			best = &copy
		}
	}
	return best
}

func timeConstrainedBranchAndBound(ctx context.Context, orders []model.Order, robotID string, robotCapacity int) (model.DeliveryPlan, error) {
	// 制限時間付きの分岐限定法（1秒制限）
	timeLimit := time.NewTimer(1 * time.Second)
	defer timeLimit.Stop()

	timeConstrainedCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		select {
		case <-timeLimit.C:
			cancel()
		case <-timeConstrainedCtx.Done():
		}
	}()

	// 分岐限定法を実行、タイムアウトしたら貪欲法にフォールバック
	result, err := branchAndBoundSolution(timeConstrainedCtx, orders, robotID, robotCapacity)
	if err != nil && timeConstrainedCtx.Err() == context.Canceled {
		// タイムアウト: 貪欲法にフォールバック
		return conservativeGreedySolution(ctx, orders, robotID, robotCapacity)
	}
	return result, err
}

func scalableHighQualitySolution(ctx context.Context, orders []model.Order, robotID string, robotCapacity int) (model.DeliveryPlan, error) {
	// 大規模データ対応の高品質近似アルゴリズム

	// 1. 前処理: 明らかに劣るアイテムを除去
	filteredOrders := preprocessOrders(orders, robotCapacity)

	// 2. コア選択: 高品質な基本解を構築
	coreOrders, _ := buildCoreSelection(filteredOrders, robotCapacity)

	// 3. 段階的改善: 制限時間内で可能な限り改善
	improvedOrders, improvedValue := stagingImprovement(ctx, coreOrders, filteredOrders, robotCapacity)

	var totalWeight int
	for _, order := range improvedOrders {
		totalWeight += order.Weight
	}

	return model.DeliveryPlan{
		RobotID:     robotID,
		TotalWeight: totalWeight,
		TotalValue:  improvedValue,
		Orders:      improvedOrders,
	}, nil
}

func preprocessOrders(orders []model.Order, capacity int) []model.Order {
	// 前処理: 明らかに劣るアイテムを除去
	var filtered []model.Order

	for _, order := range orders {
		// 基本的な検証
		if order.Weight <= 0 || order.Value <= 0 || order.Weight > capacity {
			continue
		}

		// 支配される要素の除去: より軽くて価値が高いアイテムが存在するかチェック
		dominated := false
		for _, other := range orders {
			if other.OrderID != order.OrderID &&
				other.Weight <= order.Weight &&
				other.Value >= order.Value &&
				(other.Weight < order.Weight || other.Value > order.Value) {
				dominated = true
				break
			}
		}

		if !dominated {
			filtered = append(filtered, order)
		}
	}

	return filtered
}

func buildCoreSelection(orders []model.Order, capacity int) ([]model.Order, int) {
	// 複数の戦略を並行実行して最良解を選択
	strategies := []func([]model.Order, int) ([]model.Order, int){
		// 1. 価値密度貪欲（既にソート済み）
		func(orders []model.Order, cap int) ([]model.Order, int) {
			return greedyByValueDensity(orders, cap)
		},
		// 2. 価値優先（大規模データでは高価値アイテムが重要）
		func(orders []model.Order, cap int) ([]model.Order, int) {
			return selectHighValueItems(orders, cap)
		},
		// 3. バランス戦略
		func(orders []model.Order, cap int) ([]model.Order, int) {
			return balancedStrategy(orders, cap)
		},
		// 4. 2段階戦略
		func(orders []model.Order, cap int) ([]model.Order, int) {
			return twoPhaseStrategy(orders, cap)
		},
	}

	var bestOrders []model.Order
	bestValue := 0

	for _, strategy := range strategies {
		orders, value := strategy(orders, capacity)
		if value > bestValue {
			bestOrders = orders
			bestValue = value
		}
	}

	return bestOrders, bestValue
}

func balancedStrategy(orders []model.Order, capacity int) ([]model.Order, int) {
	// 価値密度と価値の重み付け平均でソート
	balancedOrders := make([]model.Order, len(orders))
	copy(balancedOrders, orders)

	sort.Slice(balancedOrders, func(i, j int) bool {
		scoreI := calculateBalancedScore(balancedOrders[i])
		scoreJ := calculateBalancedScore(balancedOrders[j])
		return scoreI > scoreJ
	})

	return greedyByValueDensity(balancedOrders, capacity)
}

func calculateBalancedScore(order model.Order) float64 {
	if order.Weight <= 0 {
		return float64(order.Value) * 1000 // 重量0なら非常に高いスコア
	}

	valueDensity := float64(order.Value) / float64(order.Weight)
	valueScore := float64(order.Value) / 1000.0 // 正規化

	// 価値密度70%、価値30%の重み付け
	return 0.7*valueDensity + 0.3*valueScore
}

func twoPhaseStrategy(orders []model.Order, capacity int) ([]model.Order, int) {
	// フェーズ1: 高価値密度アイテムで容量の70%を埋める
	phase1Capacity := capacity * 7 / 10
	phase1Orders, phase1Value := greedyByValueDensity(orders, phase1Capacity)

	// フェーズ2: 残り容量を高価値アイテムで埋める
	usedMap := make(map[int64]bool)
	for _, order := range phase1Orders {
		usedMap[order.OrderID] = true
	}

	remainingOrders := make([]model.Order, 0)
	for _, order := range orders {
		if !usedMap[order.OrderID] {
			remainingOrders = append(remainingOrders, order)
		}
	}

	currentWeight := calculateTotalWeight(phase1Orders)
	remainingCapacity := capacity - currentWeight

	phase2Orders, phase2Value := selectHighValueItems(remainingOrders, remainingCapacity)

	combinedOrders := append(phase1Orders, phase2Orders...)
	combinedValue := phase1Value + phase2Value

	return combinedOrders, combinedValue
}

func stagingImprovement(ctx context.Context, currentOrders []model.Order, allOrders []model.Order, capacity int) ([]model.Order, int) {
	// 段階的改善: 制限時間内で可能な限り改善
	bestOrders := make([]model.Order, len(currentOrders))
	copy(bestOrders, currentOrders)
	bestValue := calculateTotalValue(bestOrders)

	// ステージ1: 高速な局所探索
	improved1, value1 := lightLocalSearch(ctx, bestOrders, allOrders, capacity)
	if value1 > bestValue {
		bestOrders = improved1
		bestValue = value1
	}

	// ステージ2: より詳細な探索（時間があれば）
	select {
	case <-ctx.Done():
		return bestOrders, bestValue
	default:
	}

	improved2, value2 := mediumLocalSearch(ctx, bestOrders, allOrders, capacity)
	if value2 > bestValue {
		bestOrders = improved2
		bestValue = value2
	}

	return bestOrders, bestValue
}

func mediumLocalSearch(ctx context.Context, currentOrders []model.Order, allOrders []model.Order, capacity int) ([]model.Order, int) {
	if len(currentOrders) == 0 {
		return currentOrders, 0
	}

	bestOrders := make([]model.Order, len(currentOrders))
	copy(bestOrders, currentOrders)
	bestValue := calculateTotalValue(bestOrders)

	iterations := 0
	maxIterations := 500 // 中程度の探索

	selectedMap := make(map[int64]bool)
	for _, order := range bestOrders {
		selectedMap[order.OrderID] = true
	}

	for iterations < maxIterations {
		improved := false
		iterations++

		// タイムアウトチェック
		if iterations%20 == 0 {
			select {
			case <-ctx.Done():
				return bestOrders, bestValue
			default:
			}
		}

		// 2-opt改善
		for i := 0; i < len(bestOrders) && !improved; i++ {
			for j := i + 1; j < len(bestOrders) && !improved; j++ {
				testOrders := make([]model.Order, len(bestOrders))
				copy(testOrders, bestOrders)
				testOrders[i], testOrders[j] = testOrders[j], testOrders[i]

				testValue := calculateTotalValue(testOrders)
				if testValue > bestValue {
					bestOrders = testOrders
					bestValue = testValue
					improved = true
				}
			}
		}

		// 交換改善
		if !improved {
			for i, selectedOrder := range bestOrders {
				for _, candidateOrder := range allOrders {
					if selectedMap[candidateOrder.OrderID] {
						continue
					}

					currentWeight := calculateTotalWeight(bestOrders)
					newWeight := currentWeight - selectedOrder.Weight + candidateOrder.Weight

					if newWeight <= capacity {
						testOrders := make([]model.Order, len(bestOrders))
						copy(testOrders, bestOrders)
						testOrders[i] = candidateOrder

						testValue := calculateTotalValue(testOrders)
						if testValue > bestValue {
							bestOrders = testOrders
							bestValue = testValue
							improved = true
							selectedMap[selectedOrder.OrderID] = false
							selectedMap[candidateOrder.OrderID] = true
							break
						}
					}
				}
				if improved {
					break
				}
			}
		}

		if !improved {
			break
		}
	}

	return bestOrders, bestValue
}

func greedyByValueDensity(orders []model.Order, capacity int) ([]model.Order, int) {
	var selected []model.Order
	currentWeight := 0
	totalValue := 0

	for _, order := range orders {
		if currentWeight+order.Weight <= capacity {
			selected = append(selected, order)
			currentWeight += order.Weight
			totalValue += order.Value
		}
	}

	return selected, totalValue
}

func greedyByValue(orders []model.Order, capacity int) ([]model.Order, int) {
	// 価値順でソート
	valueOrders := make([]model.Order, len(orders))
	copy(valueOrders, orders)
	sort.Slice(valueOrders, func(i, j int) bool {
		return valueOrders[i].Value > valueOrders[j].Value
	})

	var selected []model.Order
	currentWeight := 0
	totalValue := 0

	for _, order := range valueOrders {
		if currentWeight+order.Weight <= capacity {
			selected = append(selected, order)
			currentWeight += order.Weight
			totalValue += order.Value
		}
	}

	return selected, totalValue
}

func greedyByWeight(orders []model.Order, capacity int) ([]model.Order, int) {
	// 重量の軽い順でソート
	weightOrders := make([]model.Order, len(orders))
	copy(weightOrders, orders)
	sort.Slice(weightOrders, func(i, j int) bool {
		return weightOrders[i].Weight < weightOrders[j].Weight
	})

	var selected []model.Order
	currentWeight := 0
	totalValue := 0

	for _, order := range weightOrders {
		if currentWeight+order.Weight <= capacity {
			selected = append(selected, order)
			currentWeight += order.Weight
			totalValue += order.Value
		}
	}

	return selected, totalValue
}

func localSearch(ctx context.Context, currentOrders []model.Order, allOrders []model.Order, capacity int) ([]model.Order, int) {
	bestOrders := make([]model.Order, len(currentOrders))
	copy(bestOrders, currentOrders)
	bestValue := calculateTotalValue(bestOrders)

	improved := true
	iterations := 0
	maxIterations := 1000

	for improved && iterations < maxIterations {
		improved = false
		iterations++

		// タイムアウトチェック
		if iterations%10 == 0 {
			select {
			case <-ctx.Done():
				return bestOrders, bestValue
			default:
			}
		}

		// 2-opt: 選択済みアイテム同士の交換
		for i := 0; i < len(bestOrders); i++ {
			for j := i + 1; j < len(bestOrders); j++ {
				// i番目とj番目を入れ替えて改善されるかチェック
				testOrders := make([]model.Order, len(bestOrders))
				copy(testOrders, bestOrders)
				testOrders[i], testOrders[j] = testOrders[j], testOrders[i]

				testValue := calculateTotalValue(testOrders)
				if testValue > bestValue {
					bestOrders = testOrders
					bestValue = testValue
					improved = true
				}
			}
		}

		// Swap: 選択済みアイテムと未選択アイテムの交換
		selectedMap := make(map[int64]bool)
		for _, order := range bestOrders {
			selectedMap[order.OrderID] = true
		}

		for i, selectedOrder := range bestOrders {
			for _, candidateOrder := range allOrders {
				if selectedMap[candidateOrder.OrderID] {
					continue
				}

				// 交換して容量制限内かチェック
				currentWeight := calculateTotalWeight(bestOrders)
				newWeight := currentWeight - selectedOrder.Weight + candidateOrder.Weight

				if newWeight <= capacity {
					testOrders := make([]model.Order, len(bestOrders))
					copy(testOrders, bestOrders)
					testOrders[i] = candidateOrder

					testValue := calculateTotalValue(testOrders)
					if testValue > bestValue {
						bestOrders = testOrders
						bestValue = testValue
						improved = true
						selectedMap[selectedOrder.OrderID] = false
						selectedMap[candidateOrder.OrderID] = true
						break
					}
				}
			}
			if improved {
				break
			}
		}

		// Add: 新しいアイテムの追加
		if !improved {
			currentWeight := calculateTotalWeight(bestOrders)
			for _, candidateOrder := range allOrders {
				if selectedMap[candidateOrder.OrderID] {
					continue
				}

				if currentWeight+candidateOrder.Weight <= capacity {
					testOrders := append(bestOrders, candidateOrder)
					testValue := calculateTotalValue(testOrders)

					if testValue > bestValue {
						bestOrders = testOrders
						bestValue = testValue
						improved = true
						selectedMap[candidateOrder.OrderID] = true
						break
					}
				}
			}
		}
	}

	return bestOrders, bestValue
}

func calculateTotalValue(orders []model.Order) int {
	total := 0
	for _, order := range orders {
		total += order.Value
	}
	return total
}

func calculateTotalWeight(orders []model.Order) int {
	total := 0
	for _, order := range orders {
		total += order.Weight
	}
	return total
}
