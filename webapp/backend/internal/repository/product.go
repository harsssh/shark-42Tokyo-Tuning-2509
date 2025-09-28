package repository

import (
	"backend/internal/model"
	"context"
	"sort"
	"strings"
	"sync"
)

type ProductRepository struct {
	db              DBTX
	allProductCache *[]model.Product
	mu              sync.RWMutex
}

// ExecTx した先で product のキャッシュは使わない想定
func NewProductRepository(db DBTX) *ProductRepository {
	return &ProductRepository{db: db, allProductCache: new([]model.Product)}
}

func (r *ProductRepository) WarmUp(ctx context.Context) error {
	const q = `
		SELECT product_id, name, value, weight, image, description
		FROM products`
	err := r.db.SelectContext(ctx, r.allProductCache, q)

	return err
}

// 商品一覧（検索・ソート・ページングをメモリ内で実施）
func (r *ProductRepository) ListProducts(
	ctx context.Context,
	userID int,
	req model.ListRequest,
) ([]model.Product, int, error) {
	r.mu.Lock()
	needLoad := len(*r.allProductCache) == 0
	if needLoad {
		if err := r.WarmUp(ctx); err != nil {
			r.mu.Unlock()
			return nil, 0, err
		}
	}
	r.mu.Unlock()

	// 作業用にスライスをコピー（元配列の順序を汚さないため）
	r.mu.RLock()
	items := make([]model.Product, len(*r.allProductCache))
	copy(items, *r.allProductCache)
	r.mu.RUnlock()

	// LIKE 相当の検索（MySQL 既定の大文字小文字非区別に寄せて簡易的に小文字化）
	// 元の実装から type は考慮されていない
	if s := strings.TrimSpace(req.Search); s != "" {
		needle := strings.ToLower(s)
		dst := items[:0]
		for _, p := range items {
			if strings.Contains(strings.ToLower(p.Name), needle) ||
				strings.Contains(strings.ToLower(p.Description), needle) {
				dst = append(dst, p)
			}
		}
		items = dst
	}

	total := len(items)

	// ソート
	field := strings.ToLower(strings.TrimSpace(req.SortField))
	order := strings.ToUpper(strings.TrimSpace(req.SortOrder))
	if order != "ASC" && order != "DESC" {
		order = "ASC"
	}
	desc := order == "DESC"

	less, ok := sortLessFunc(field, desc)
	if !ok {
		// 指定が不正または未指定なら product_id 昇順を既定
		less, _ = sortLessFunc("product_id", false)
	}
	sort.Slice(items, func(i, j int) bool { return less(items[i], items[j]) })

	// ページング（安全にスライス）
	if req.PageSize <= 0 {
		req.PageSize = 20
	}
	if req.Offset < 0 {
		req.Offset = 0
	}
	start := req.Offset
	if start > total {
		start = total
	}
	end := start + req.PageSize
	if end > total {
		end = total
	}
	page := items[start:end]

	return page, total, nil
}

func sortLessFunc(field string, desc bool) (func(a, b model.Product) bool, bool) {
	switch field {
	case "product_id", "id":
		if desc {
			return func(a, b model.Product) bool { return a.ProductID > b.ProductID }, true
		}
		return func(a, b model.Product) bool { return a.ProductID < b.ProductID }, true
	case "name":
		if desc {
			return func(a, b model.Product) bool { return a.Name > b.Name }, true
		}
		return func(a, b model.Product) bool { return a.Name < b.Name }, true
	case "value":
		if desc {
			return func(a, b model.Product) bool { return a.Value > b.Value }, true
		}
		return func(a, b model.Product) bool { return a.Value < b.Value }, true
	case "weight":
		if desc {
			return func(a, b model.Product) bool { return a.Weight > b.Weight }, true
		}
		return func(a, b model.Product) bool { return a.Weight < b.Weight }, true
	default:
		return nil, false
	}
}
