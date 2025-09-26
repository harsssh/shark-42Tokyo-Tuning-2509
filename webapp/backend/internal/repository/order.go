package repository

import (
	"backend/internal/model"
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/jmoiron/sqlx"
)

type OrderRepository struct {
	db DBTX
}

func NewOrderRepository(db DBTX) *OrderRepository {
	return &OrderRepository{db: db}
}

// 注文を作成し、生成された注文IDを返す
func (r *OrderRepository) Create(ctx context.Context, order *model.Order) (string, error) {
	query := `INSERT INTO orders (user_id, product_id, shipped_status, created_at) VALUES (?, ?, 'shipping', NOW())`
	result, err := r.db.ExecContext(ctx, query, order.UserID, order.ProductID)
	if err != nil {
		return "", err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d", id), nil
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
        WHERE o.shipped_status = 'shipping'
    `
	err := r.db.SelectContext(ctx, &orders, query)
	return orders, err
}

// 注文履歴一覧を取得
func (r *OrderRepository) ListOrders(ctx context.Context, userID int, req model.ListRequest) ([]model.Order, int, error) {
	// WHERE 句の構築
	conds := []string{"o.user_id = ?"}
	args := []any{userID}

	if s := strings.TrimSpace(req.Search); s != "" {
		if strings.ToLower(req.Type) == "prefix" {
			// 前方一致
			conds = append(conds, "p.name like ?")
			args = append(args, s+"%")
		} else {
			// 部分一致
			conds = append(conds, "MATCH(p.name) AGAINST (? IN BOOLEAN MODE)")
			args = append(args, "*"+s+"*")
		}
	}

	orderBy := buildOrderBy(req.SortField, req.SortOrder)

	query := fmt.Sprintf(`
		SELECT
			o.order_id,
			o.product_id,
			p.name          AS product_name,
			o.shipped_status,
			o.created_at,
			o.arrived_at,
			COUNT(*) OVER() AS total_count
		FROM orders o
		JOIN products p ON p.product_id = o.product_id
		WHERE %s
		%s
		LIMIT ? OFFSET ?`,
		strings.Join(conds, " AND "),
		orderBy,
	)

	// ページング引数
	argsWithPage := append(append([]interface{}{}, args...), req.PageSize, req.Offset)

	type row struct {
		OrderID       int64        `db:"order_id"`
		ProductID     int          `db:"product_id"`
		ProductName   string       `db:"product_name"`
		ShippedStatus string       `db:"shipped_status"`
		CreatedAt     sql.NullTime `db:"created_at"`
		ArrivedAt     sql.NullTime `db:"arrived_at"`
		Total         int          `db:"total_count"`
	}

	var rows []row
	if err := r.db.SelectContext(ctx, &rows, query, argsWithPage...); err != nil {
		return nil, 0, err
	}

	// total は COUNT(*) OVER() から取得。ページが空の場合のみ COUNT(*) をフォールバック
	total := 0
	if len(rows) > 0 {
		total = rows[0].Total
	} else {
		countQuery := fmt.Sprintf(`
			SELECT COUNT(*)
			FROM orders o
			JOIN products p ON p.product_id = o.product_id
			WHERE %s`,
			strings.Join(conds, " AND "),
		)
		if err := r.db.GetContext(ctx, &total, countQuery, args...); err != nil {
			return nil, 0, err
		}
		return []model.Order{}, total, nil
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
		return "ORDER BY o.shipped_status " + dir
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
