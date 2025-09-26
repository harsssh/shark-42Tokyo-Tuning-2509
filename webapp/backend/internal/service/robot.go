package service

import (
	"backend/internal/model"
	"backend/internal/repository"
	"backend/internal/service/utils"
	"context"
	"log"
)

type RobotService struct {
	store *repository.Store
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
	return &plan, nil
}

func (s *RobotService) UpdateOrderStatus(ctx context.Context, orderID int64, newStatus string) error {
	return utils.WithTimeout(ctx, func(ctx context.Context) error {
		return s.store.OrderRepo.UpdateStatuses(ctx, []int64{orderID}, newStatus)
	})
}

func selectOrdersForDelivery(ctx context.Context, orders []model.Order, robotID string, robotCapacity int) (model.DeliveryPlan, error) {
	n := len(orders)
	bestValue := 0
	var bestSet []model.Order
	steps := 0
	checkEvery := 16384

	var dfs func(i, curWeight, curValue int, curSet []model.Order) bool
	dfs = func(i, curWeight, curValue int, curSet []model.Order) bool {
		if curWeight > robotCapacity {
			return false
		}
		steps++
		if checkEvery > 0 && steps%checkEvery == 0 {
			select {
			case <-ctx.Done():
				return true
			default:
			}
		}
		if i == n {
			if curValue > bestValue {
				bestValue = curValue
				bestSet = append([]model.Order{}, curSet...)
			}
			return false
		}

		if dfs(i+1, curWeight, curValue, curSet) {
			return true
		}

		order := orders[i]
		return dfs(i+1, curWeight+order.Weight, curValue+order.Value, append(curSet, order))
	}

	canceled := dfs(0, 0, 0, nil)
	if canceled {
		return model.DeliveryPlan{}, ctx.Err()
	}

	var totalWeight int
	for _, o := range bestSet {
		totalWeight += o.Weight
	}

	return model.DeliveryPlan{
		RobotID:     robotID,
		TotalWeight: totalWeight,
		TotalValue:  bestValue,
		Orders:      bestSet,
	}, nil
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
	dp := make([]int, W+1)     // 重さ w 以下での最大価値
	choose := make([]int, W+1) // dp[w] を更新した「直近のアイテム index」、未設定は -1
	prevW := make([]int, W+1)  // そのときの遷移元の重さ（w - weight[i]）

	for i := range choose {
		choose[i] = -1
		prevW[i] = -1
	}

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
				choose[cw] = i
				prevW[cw] = cw - w
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
	var picked []model.Order
	for cur := bestW; cur > 0 && choose[cur] != -1; {
		idx := choose[cur]
		picked = append(picked, orders[idx])
		cur = prevW[cur]
	}

	return model.DeliveryPlan{
		RobotID:     robotID,
		TotalWeight: bestW,
		TotalValue:  bestV,
		Orders:      picked,
	}, nil
}
