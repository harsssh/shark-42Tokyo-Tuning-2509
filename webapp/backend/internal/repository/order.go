package repository

import (
	"backend/internal/model"
	"context"
	"database/sql"
	"fmt"
	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/samber/lo"
	"strings"
	"sync"

	"github.com/jmoiron/sqlx"
)

const (
	orderListCountCacheSize = 128
	// 本来はモデルにあるべきそう
	shippedStatusEnumShipping   = 2
	shippedStatusEnumDelivering = 1
	shippedStatusEnumCompleted  = 0
)

type orderCountCacheKey struct {
	userID        int
	searchPattern string
}

type orderRepoState struct {
	shippingOrdersVersion int64
	countCache            *lru.Cache[orderCountCacheKey, int]
	mu                    sync.RWMutex
}

type OrderRepository struct {
	db    DBTX
	state *orderRepoState
}

func newOrderRepository(db DBTX, state *orderRepoState) *OrderRepository {
	if state == nil {
		state = &orderRepoState{}
	}

	state.mu.Lock()
	if state.countCache == nil {
		state.countCache = lo.Must(lru.New[orderCountCacheKey, int](orderListCountCacheSize))
	}
	state.mu.Unlock()

	return &OrderRepository{
		db:    db,
		state: state,
	}
}

func NewOrderRepository(db DBTX) *OrderRepository {
	return newOrderRepository(db, &orderRepoState{})
}

func (r *OrderRepository) GetShippingOrdersVersion(ctx context.Context) (int64, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	return r.state.shippingOrdersVersion, nil
}

func (r *OrderRepository) onUpdateOrders() {
	r.state.mu.Lock()
	r.state.shippingOrdersVersion++
	r.state.mu.Unlock()

	r.state.countCache.Purge()
}

func (r *OrderRepository) getCachedOrderCount(key orderCountCacheKey) (int, bool) {
	r.state.mu.RLock()
	cache := r.state.countCache
	r.state.mu.RUnlock()

	if cache == nil {
		return 0, false
	}
	return cache.Get(key)
}

func (r *OrderRepository) setCachedOrderCount(key orderCountCacheKey, total int) {
	r.state.mu.RLock()
	cache := r.state.countCache
	r.state.mu.RUnlock()
	if cache == nil {
		return
	}
	cache.Add(key, total)
}

// ダメだったら Create を復旧する
func (r *OrderRepository) BatchCreate(ctx context.Context, orders []*model.Order) ([]string, error) {
	if len(orders) == 0 {
		return []string{}, nil
	}

	// NOTE: 良くないキャスト
	txx, ok := r.db.(*sqlx.Tx)
	if !ok {
		return nil, fmt.Errorf("BatchCreate must be called within a transaction")
	}

	// named exec で insert する
	query := `INSERT INTO orders (user_id, product_id, shipped_status, created_at) VALUES (:user_id, :product_id, 'shipping', NOW())`
	result, err := txx.NamedExecContext(ctx, query, orders)
	if err != nil {
		return nil, err
	}

	r.onUpdateOrders()

	// NOTE: 結構怖い
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

	if err == nil {
		r.onUpdateOrders()
	}

	return err
}

// 配送中(shipped_status:shipping)の注文一覧を取得
func (r *OrderRepository) GetShippingOrders(ctx context.Context) ([]model.Order, error) {
	var orders []model.Order
	query := `
        SELECT
            o.order_id,
            p.weight,
            p.value
        FROM orders o
        JOIN products p ON o.product_id = p.product_id
        WHERE o.shipped_status_code = ?
    `
	err := r.db.SelectContext(ctx, &orders, query, shippedStatusEnumShipping)

	return orders, err
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

	// 件数の取得。検索条件がなければ orders のみでカウントして余計な JOIN を避ける
	countQuery := lo.Ternary(searchApplied,
		fmt.Sprintf(`
            SELECT COUNT(*)
            FROM orders o
            JOIN products p ON p.product_id = o.product_id
            WHERE %s`,
			strings.Join(conds, " AND "),
		),
		"SELECT COUNT(*) FROM orders o WHERE o.user_id = ?",
	)
	countArgs := lo.Ternary(searchApplied,
		[]any{userID, searchPattern},
		[]any{userID},
	)

	cacheKey := orderCountCacheKey{
		userID:        userID,
		searchPattern: searchPattern,
	}
	total, cached := r.getCachedOrderCount(cacheKey)
	if !cached {
		if err := r.db.GetContext(ctx, &total, countQuery, countArgs...); err != nil {
			return nil, 0, err
		}
		r.setCachedOrderCount(cacheKey, total)
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
