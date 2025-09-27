package service

import (
	"backend/internal/model"
	"backend/internal/repository"
	"backend/internal/service/utils"
	"context"
	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/samber/lo"
	"log"
)

const planCacheSize = 1024

type planCacheKey struct {
	ordersVersion int64
	capacity      int
}

type RobotService struct {
	store     *repository.Store
	planCache *lru.Cache[planCacheKey, model.DeliveryPlan]
}

func NewRobotService(store *repository.Store) *RobotService {
	return &RobotService{store: store, planCache: lo.Must(lru.New[planCacheKey, model.DeliveryPlan](planCacheSize))}
}

func (s *RobotService) GenerateDeliveryPlan(ctx context.Context, robotID string, capacity int) (*model.DeliveryPlan, error) {
	var plan model.DeliveryPlan

	cacheKey := planCacheKey{
		ordersVersion: lo.Must(s.store.OrderRepo.GetShippingOrdersVersion(ctx)),
		capacity:      capacity,
	}
	if v, ok := s.planCache.Get(cacheKey); ok {
		v.RobotID = robotID
		return &v, nil
	}

	err := utils.WithTimeout(ctx, func(ctx context.Context) error {
		return s.store.ExecTx(ctx, func(txStore *repository.Store) error {

			orders, err := txStore.OrderRepo.GetShippingOrders(ctx)
			if err != nil {
				return err
			}
			plan, err = bestSelectOrdersForDelivery(ctx, orders, robotID, capacity)
			if err != nil {
				return err
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

	// 元のバージョンをキーにキャッシュすることで、配送ステータス更新後の再計算を促す
	s.planCache.Add(cacheKey, plan)

	return &plan, nil
}

func (s *RobotService) UpdateOrderStatus(ctx context.Context, orderID int64, newStatus string) error {
	return utils.WithTimeout(ctx, func(ctx context.Context) error {
		return s.store.OrderRepo.UpdateStatuses(ctx, []int64{orderID}, newStatus)
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
		return model.DeliveryPlan{RobotID: robotID}, nil
	}

	W := robotCapacity
	type knapChoice struct {
		orderIndex int
		prev       *knapChoice
	}

	dp := make([]int, W+1)              // 重さ w 以下での最大価値
	choices := make([]*knapChoice, W+1) // dp[w] を構成する最後の選択

	// orders は 100k 件, W は 100k 件が上限?
	// TODO: 10^10 回ループする可能性があるので、タイムアウトの考慮が必要?
	for i, o := range orders {
		w, v := o.Weight, o.Value
		if w <= 0 || v < 0 {
			// 一応 validation
			continue
		}
		if w > W {
			continue
		}
		for cw := W; cw >= w; cw-- {
			alt := dp[cw-w] + v
			if alt > dp[cw] {
				dp[cw] = alt
				choices[cw] = &knapChoice{orderIndex: i, prev: choices[cw-w]}
			}
		}
	}

	// 最良価値の重さを特定
	bestW, bestV := 0, 0
	for w := 0; w <= W; w++ {
		if dp[w] > bestV {
			bestV = dp[w]
			bestW = w
		}
	}

	// 経路復元
	var (
		picked      []model.Order
		totalWeight int
		totalValue  int
	)
	for node := choices[bestW]; node != nil; node = node.prev {
		order := orders[node.orderIndex]
		picked = append(picked, order)
		totalWeight += order.Weight
		totalValue += order.Value
	}

	return model.DeliveryPlan{
		RobotID:     robotID,
		TotalWeight: totalWeight,
		TotalValue:  totalValue,
		Orders:      picked,
	}, nil
}
