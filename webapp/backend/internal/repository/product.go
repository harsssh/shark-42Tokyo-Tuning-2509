package repository

import (
	"backend/internal/model"
	"context"
	"fmt"
)

type ProductRepository struct {
	db DBTX
}

func NewProductRepository(db DBTX) *ProductRepository {
	return &ProductRepository{db: db}
}

// 商品一覧を全件取得し、アプリケーション側でページング処理を行う
func (r *ProductRepository) ListProducts(ctx context.Context, userID int, req model.ListRequest) ([]model.Product, int, error) {
	var products []model.Product
	var total int

	baseQuery := `
		FROM products
	`
	var args []any

	// 検索条件
	if req.Search != "" {
		baseQuery += " WHERE MATCH(name, description) AGAINST (? IN BOOLEAN MODE)"
		searchPattern := "*" + req.Search + "*"
		args = append(args, searchPattern)
	}

	// 件数をカウント
	countQuery := "SELECT COUNT(*)" + baseQuery
	if err := r.db.GetContext(ctx, &total, countQuery, args...); err != nil {
		return nil, 0, err
	}

	// ページング付きデータ取得
	dataQuery := fmt.Sprintf(
		`SELECT product_id, name, value, weight, image, description %s
		 ORDER BY %s %s, product_id ASC
		 LIMIT %d OFFSET %d`,
		baseQuery, req.SortField, req.SortOrder, req.PageSize, req.Offset,
	)

	if err := r.db.SelectContext(ctx, &products, dataQuery, args...); err != nil {
		return nil, 0, err
	}

	return products, total, nil
}
