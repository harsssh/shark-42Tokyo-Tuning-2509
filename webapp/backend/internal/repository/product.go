package repository

import (
	"backend/internal/model"
	"context"
	"fmt"
	"github.com/samber/lo"
)

type ProductRepository struct {
	db DBTX
}

func NewProductRepository(db DBTX) *ProductRepository {
	return &ProductRepository{db: db}
}

// 商品一覧を全件取得し、アプリケーション側でページング処理を行う
func (r *ProductRepository) ListProducts(ctx context.Context, userID int, req model.ListRequest) ([]model.Product, int, error) {
	var args []any

	condPart := ""
	if req.Search != "" {
		condPart += " WHERE name LIKE ? OR description LIKE ?"
		searchPattern := "%" + req.Search + "%"
		args = append(args, searchPattern, searchPattern)
	}

	query := fmt.Sprintf(`
		SELECT product_id, name, value, weight, image, description, COUNT(*) OVER() AS total_count
		FROM products 
		%s
		ORDER BY %s %s, product_id ASC
		LIMIT ? OFFSET ?
	`, condPart, req.SortField, req.SortOrder)
	args = append(args, req.PageSize, req.Offset)

	type Row struct {
		model.Product
		TotalCount int `db:"total_count"`
	}
	var rows []Row
	err := r.db.SelectContext(ctx, &rows, query, args...)
	if err != nil {
		return nil, 0, err
	}

	if len(rows) == 0 {
		return []model.Product{}, 0, nil
	}

	pagedProducts := lo.Map(rows, func(r Row, _ int) model.Product {
		return r.Product
	})
	total := rows[0].TotalCount

	return pagedProducts, total, nil
}
