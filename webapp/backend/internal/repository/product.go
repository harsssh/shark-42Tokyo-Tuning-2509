package repository

import (
	"backend/internal/model"
	"context"
	"fmt"
	"strings"
)

type ProductRepository struct {
	db DBTX
}

func NewProductRepository(db DBTX) *ProductRepository {
	return &ProductRepository{db: db}
}

// 商品一覧を全件取得し、アプリケーション側でページング処理を行う
// type の考慮どこいった??
func (r *ProductRepository) ListProducts(
	ctx context.Context,
	userID int,
	req model.ListRequest,
) ([]model.Product, int, error) {
	where := ""
	args := make([]interface{}, 0, 1)

	if s := strings.TrimSpace(req.Search); s != "" {
		where = "WHERE MATCH(name, description) AGAINST (? IN BOOLEAN MODE)"
		args = append(args, "*"+s+"*")
	}

	// 総件数
	var total int
	countSQL := "SELECT COUNT(1) FROM products " + where
	if err := r.db.GetContext(ctx, &total, countSQL, args...); err != nil {
		return nil, 0, err
	}

	// データ取得（ORDER BY の列名・並び順をそのまま埋め込む）
	query := fmt.Sprintf(`
		SELECT product_id, name, value, weight, image, description
		FROM products
		%s
		ORDER BY %s %s, product_id ASC
		LIMIT ? OFFSET ?`,
		where, req.SortField, req.SortOrder,
	)

	dataArgs := append(args, req.PageSize, req.Offset)

	var products []model.Product
	if err := r.db.SelectContext(ctx, &products, query, dataArgs...); err != nil {
		return nil, 0, err
	}

	return products, total, nil
}
