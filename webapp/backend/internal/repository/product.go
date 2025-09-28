package repository

import (
	"backend/internal/model"
	"context"
	"sort"
	"strings"
	"sync"
)

type ProductRepository struct {
	db DBTX

	mu          sync.RWMutex
	cache       []model.Product
	cacheLoaded bool
}

func NewProductRepository(db DBTX) *ProductRepository {
	return &ProductRepository{db: db}
}

// 内部: 初回のみ DB から全件を読み込みキャッシュを構築
func (r *ProductRepository) loadAllProducts(ctx context.Context) error {
	// まずは RLock で軽量チェック
	r.mu.RLock()
	loaded := r.cacheLoaded
	r.mu.RUnlock()
	if loaded {
		return nil
	}

	// ダブルチェックロッキングで重複ロードを防止
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cacheLoaded {
		return nil
	}

	const q = `
		SELECT product_id, name, value, weight, image, description
		FROM products
		ORDER BY product_id ASC
	`
	var items []model.Product
	if err := r.db.SelectContext(ctx, &items, q); err != nil {
		return err
	}

	r.cache = items
	r.cacheLoaded = true
	return nil
}

// キャッシュを明示的に破棄（再読込は次回 ListProducts 呼び出し時）
func (r *ProductRepository) InvalidateCache() {
	r.mu.Lock()
	r.cache = nil
	r.cacheLoaded = false
	r.mu.Unlock()
}

type sortField int

const (
	sortByID sortField = iota
	sortByName
	sortByValue
	sortByWeight
)

func parseSortField(f string) sortField {
	switch strings.ToLower(strings.TrimSpace(f)) {
	case "product_id", "id":
		return sortByID
	case "name":
		return sortByName
	case "value":
		return sortByValue
	case "weight":
		return sortByWeight
	default:
		return sortByID
	}
}

func isDesc(order string) bool {
	return strings.EqualFold(strings.TrimSpace(order), "desc")
}

// 商品一覧をキャッシュから取得し、アプリケーション側で検索・ソート・ページング
func (r *ProductRepository) ListProducts(ctx context.Context, userID int, req model.ListRequest) ([]model.Product, int, error) {
	// 初回のみ DB からロード
	if err := r.loadAllProducts(ctx); err != nil {
		return nil, 0, err
	}

	// キャッシュのスナップショットを取得（破壊的変更を避けるためコピー）
	r.mu.RLock()
	items := make([]model.Product, len(r.cache))
	copy(items, r.cache)
	r.mu.RUnlock()

	// 検索（name / description に対する部分一致）
	if s := strings.TrimSpace(req.Search); s != "" {
		needle := strings.ToLower(s)
		filtered := items[:0]
		for _, p := range items {
			if strings.Contains(strings.ToLower(p.Name), needle) ||
				strings.Contains(strings.ToLower(p.Description), needle) {
				filtered = append(filtered, p)
			}
		}
		items = filtered
	}

	// ソート（既定は product_id ASC、同値時の安定性は product_id 昇順）
	sf := parseSortField(req.SortField)
	desc := isDesc(req.SortOrder)

	sort.SliceStable(items, func(i, j int) bool {
		var cmp int
		switch sf {
		case sortByName:
			switch {
			case items[i].Name < items[j].Name:
				cmp = -1
			case items[i].Name > items[j].Name:
				cmp = 1
			default:
				cmp = 0
			}
		case sortByValue:
			switch {
			case items[i].Value < items[j].Value:
				cmp = -1
			case items[i].Value > items[j].Value:
				cmp = 1
			default:
				cmp = 0
			}
		case sortByWeight:
			switch {
			case items[i].Weight < items[j].Weight:
				cmp = -1
			case items[i].Weight > items[j].Weight:
				cmp = 1
			default:
				cmp = 0
			}
		default: // sortByID
			switch {
			case items[i].ProductID < items[j].ProductID:
				cmp = -1
			case items[i].ProductID > items[j].ProductID:
				cmp = 1
			default:
				cmp = 0
			}
		}

		if cmp == 0 {
			// 同値時は常に product_id 昇順で安定化
			return items[i].ProductID < items[j].ProductID
		}
		if desc {
			return cmp > 0
		}
		return cmp < 0
	})

	// ページング
	total := len(items)
	offset := req.Offset
	if offset < 0 {
		offset = 0
	}
	pageSize := req.PageSize
	if pageSize < 0 {
		pageSize = 0
	}

	start := offset
	if start > total {
		start = total
	}
	end := offset + pageSize
	if end > total {
		end = total
	}
	if start > end {
		start = end
	}

	paged := items[start:end]
	return paged, total, nil
}
