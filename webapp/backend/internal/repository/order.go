package repository

import (
	"backend/internal/model"
	"context"
	"database/sql"
	"fmt"
	"github.com/samber/lo"
	"strings"
	"sync"

	"github.com/jmoiron/sqlx"
)

const (
	// 本来はモデルにあるべきそう
	shippedStatusEnumShipping   = 2
	shippedStatusEnumDelivering = 1
	shippedStatusEnumCompleted  = 0
)

type orderRepoState struct {
	// 更新のたびにインクリメントされるバージョン（配送中一覧キャッシュ用）
	shippingOrdersVersion int64

	// GetShippingOrders の結果キャッシュ（参照返却前提）
	shippingOrdersCache []model.Order

	// user_id のみの COUNT(*) キャッシュ
	countByUser map[int]int

	mu sync.RWMutex
}

type OrderRepository struct {
	db    DBTX
	state *orderRepoState
}

func newOrderRepository(db DBTX, state *orderRepoState) *OrderRepository {
	state.mu.Lock()
	if state.countByUser == nil {
		state.countByUser = make(map[int]int)
	}
	state.mu.Unlock()
	return &OrderRepository{
		db:    db,
		state: state,
	}
}

func (r *OrderRepository) GetShippingOrdersVersion(ctx context.Context) (int64, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	return r.state.shippingOrdersVersion, nil
}

func (r *OrderRepository) onUpdateOrders(userIDs ...int) {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()

	r.state.shippingOrdersVersion++
	r.state.shippingOrdersCache = nil

	if len(userIDs) == 0 {
		r.state.countByUser = make(map[int]int)
		return
	}

	for _, uid := range lo.Uniq(userIDs) {
		delete(r.state.countByUser, uid)
	}
}

func (r *OrderRepository) BatchCreate(ctx context.Context, orders []*model.Order) ([]string, error) {
	if len(orders) == 0 {
		return []string{}, nil
	}

	txx, ok := r.db.(*sqlx.Tx)
	if !ok {
		return nil, fmt.Errorf("BatchCreate must be called within a transaction")
	}

	query := `INSERT INTO orders (user_id, product_id, shipped_status, created_at) VALUES (:user_id, :product_id, 'shipping', NOW())`
	result, err := txx.NamedExecContext(ctx, query, orders)
	if err != nil {
		return nil, err
	}

	userIDs := lo.Map(orders, func(o *model.Order, _ int) int {
		return o.UserID
	})
	r.onUpdateOrders(userIDs...)

	// このロジック大丈夫?
	var insertedIDs []string
	lastID, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	for i := int64(0); i < rowsAffected; i++ {
		insertedIDs = append(insertedIDs, fmt.Sprintf("%d", lastID+i))
	}

	return insertedIDs, nil
}

// 複数の注文IDのステータスを一括で更新
// 主に配送ロボットが注文を引き受けた際に一括更新をするために使用
func (r *OrderRepository) UpdateStatuses(ctx context.Context, orderIDs []int64, newStatus string) error {
	if len(orderIDs) == 0 {
		return nil
	}
	query, args, err := sqlx.In("UPDATE orders SET shipped_status = ? WHERE order_id IN (?)", newStatus, orderIDs)
	if err != nil {
		return err
	}
	query = r.db.Rebind(query)
	_, err = r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}

	// わざわざ select するの遅いかも
	var userIDs []int
	q2, a2, err2 := sqlx.In("SELECT DISTINCT user_id FROM orders WHERE order_id IN (?)", orderIDs)
	if err2 == nil {
		q2 = r.db.Rebind(q2)
		if err3 := r.db.SelectContext(ctx, &userIDs, q2, a2...); err3 == nil && len(userIDs) > 0 {
			r.onUpdateOrders(userIDs...)
			return nil
		}
	}

	// フォールバック（取得失敗時や対象ユーザー不明時は全クリア）
	r.onUpdateOrders()
	return nil
}

// 配送中(shipped_status_code: shipping)の注文一覧を取得（参照返却・バージョン連動キャッシュ）
func (r *OrderRepository) GetShippingOrders(ctx context.Context) ([]model.Order, error) {
	r.state.mu.RLock()
	if cache := r.state.shippingOrdersCache; cache != nil {
		out := cache
		r.state.mu.RUnlock()
		return out, nil
	}
	localVer := r.state.shippingOrdersVersion
	r.state.mu.RUnlock()

	var orders []model.Order
	const query = `
        SELECT
            o.order_id,
            p.weight,
            p.value
        FROM orders o
        JOIN products p ON o.product_id = p.product_id
        WHERE o.shipped_status_code = ?
    `
	if err := r.db.SelectContext(ctx, &orders, query, shippedStatusEnumShipping); err != nil {
		return nil, err
	}

	r.state.mu.Lock()
	if r.state.shippingOrdersVersion == localVer && r.state.shippingOrdersCache == nil {
		r.state.shippingOrdersCache = orders
	}
	r.state.mu.Unlock()

	return orders, nil
}

// 注文履歴一覧を取得
func (r *OrderRepository) ListOrders(ctx context.Context, userID int, req model.ListRequest) ([]model.Order, int, error) {
	// WHERE 句の構築
	conds := []string{"o.user_id = ?"}
	args := []any{userID}

	var (
		searchApplied bool
		searchPattern string
	)

	if s := strings.TrimSpace(req.Search); s != "" {
		searchApplied = true
		searchType := strings.ToLower(req.Type)
		if searchType == "prefix" {
			// 前方一致
			searchPattern = s + "%"
		} else {
			// 部分一致
			searchType = "partial"
			searchPattern = "%" + s + "%"
		}
		conds = append(conds, "p.name LIKE ?")
		args = append(args, searchPattern)
	}

	var total int
	if !searchApplied {
		r.state.mu.RLock()
		cached, ok := r.state.countByUser[userID]
		r.state.mu.RUnlock()
		if ok {
			total = cached
		} else {
			const countQuery = "SELECT COUNT(*) FROM orders o WHERE o.user_id = ?"
			if err := r.db.GetContext(ctx, &total, countQuery, userID); err != nil {
				return nil, 0, err
			}
			r.state.mu.Lock()
			r.state.countByUser[userID] = total
			r.state.mu.Unlock()
		}
	} else {
		countQuery := fmt.Sprintf(`
            SELECT COUNT(*)
            FROM orders o
            JOIN products p ON p.product_id = o.product_id
            WHERE %s`, strings.Join(conds, " AND "),
		)
		if err := r.db.GetContext(ctx, &total, countQuery, userID, searchPattern); err != nil {
			return nil, 0, err
		}
	}

	if total == 0 {
		return []model.Order{}, 0, nil
	}

	orderBy := buildOrderBy(req.SortField, req.SortOrder)

	query := fmt.Sprintf(`
        SELECT
            o.order_id,
            o.product_id,
            p.name          AS product_name,
            o.shipped_status,
            o.created_at,
            o.arrived_at
        FROM orders o
        JOIN products p ON p.product_id = o.product_id
        WHERE %s
        %s
        LIMIT ? OFFSET ?`,
		strings.Join(conds, " AND "),
		orderBy,
	)

	// ページング引数
	argsWithPage := append(append([]any{}, args...), req.PageSize, req.Offset)

	type row struct {
		OrderID       int64        `db:"order_id"`
		ProductID     int          `db:"product_id"`
		ProductName   string       `db:"product_name"`
		ShippedStatus string       `db:"shipped_status"`
		CreatedAt     sql.NullTime `db:"created_at"`
		ArrivedAt     sql.NullTime `db:"arrived_at"`
	}

	var rows []row
	if err := r.db.SelectContext(ctx, &rows, query, argsWithPage...); err != nil {
		return nil, 0, err
	}

	orders := make([]model.Order, 0, len(rows))
	for _, r := range rows {
		orders = append(orders, model.Order{
			OrderID:       r.OrderID,
			ProductID:     r.ProductID,
			ProductName:   r.ProductName,
			ShippedStatus: r.ShippedStatus,
			CreatedAt:     r.CreatedAt.Time,
			ArrivedAt:     r.ArrivedAt,
		})
	}

	return orders, total, nil
}

func buildOrderBy(field, order string) string {
	dir := "ASC"
	if strings.ToUpper(order) == "DESC" {
		dir = "DESC"
	}
	switch field {
	case "product_name":
		return "ORDER BY p.name " + dir
	case "created_at":
		return "ORDER BY o.created_at " + dir
	case "shipped_status":
		return "ORDER BY o.shipped_status_code " + dir
	case "arrived_at":
		// ASC: NULLS FIRST, DESC: NULLS LAST（既存仕様どおり）
		if dir == "DESC" {
			return "ORDER BY (o.arrived_at IS NULL) ASC, o.arrived_at DESC"
		}
		return "ORDER BY (o.arrived_at IS NULL) DESC, o.arrived_at ASC"
	case "order_id":
		fallthrough
	default:
		return "ORDER BY o.order_id " + dir
	}
}
