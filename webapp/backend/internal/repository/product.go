package repository

import (
	"backend/internal/model"
	"context"
	"fmt"
	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/samber/lo"
	"log"
	"strings"
	"sync"
)

var ProductListCountCacheSize = 64

type productRepoState struct {
	once           sync.Once
	listCountCache *lru.Cache[string, int]
}

func (s *productRepoState) initListCountCache() *lru.Cache[string, int] {
	s.once.Do(func() {
		s.listCountCache = lo.Must(lru.New[string, int](ProductListCountCacheSize))
	})
	return s.listCountCache
}

type ProductRepository struct {
	db             DBTX
	listCountCache *lru.Cache[string, int] // listCountCache key: search -> total_count
}

func newProductRepository(db DBTX, state *productRepoState) *ProductRepository {
	return &ProductRepository{db: db, listCountCache: state.initListCountCache()}
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
		where = "WHERE name LIKE ? OR description LIKE ?"
		pattern := "%" + s + "%"
		args = append(args, pattern, pattern)
	}

	// 総件数
	var total int
	totalCacheKey := req.Search
	if v, ok := r.listCountCache.Get(totalCacheKey); ok {
		total = v
	} else {
		// キャッシュにない場合はDBから取得してキャッシュに保存
		countSQL := "SELECT COUNT(1) FROM products " + where
		if err := r.db.GetContext(ctx, &total, countSQL, args...); err != nil {
			return nil, 0, err
		}
		r.listCountCache.Add(totalCacheKey, total)
		log.Printf("ListProducts: listCountCache len=%d\n", r.listCountCache.Len())
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
