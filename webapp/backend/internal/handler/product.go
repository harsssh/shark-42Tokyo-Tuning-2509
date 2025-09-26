package handler

import (
	"backend/internal/middleware"
	"backend/internal/model"
	"backend/internal/service"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path"
	"path/filepath"
	"strings"
)

const (
	PRODUCT_PAGE_DEFAULT       = 1
	PRODUCT_PAGE_SIZE_DEFAULT  = 20
	PRODUCT_SORT_FIELD_DEFAULT = "product_id"
	PRODUCT_SORT_ORDER_DEFAULT = "asc"
)

type ProductHandler struct {
	ProductSvc *service.ProductService
}

func NewProductHandler(svc *service.ProductService) *ProductHandler {
	return &ProductHandler{ProductSvc: svc}
}

// 商品一覧を取得
func (h *ProductHandler) List(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserFromContext(r.Context())
	if !ok {
		http.Error(w, "User not found in context", http.StatusInternalServerError)
		return
	}

	var req model.ListRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Page <= 0 {
		req.Page = PRODUCT_PAGE_DEFAULT
	}
	if req.PageSize <= 0 {
		req.PageSize = PRODUCT_PAGE_SIZE_DEFAULT
	}
	if req.SortField == "" {
		req.SortField = PRODUCT_SORT_FIELD_DEFAULT
	}
	if req.SortOrder == "" {
		req.SortOrder = PRODUCT_SORT_ORDER_DEFAULT
	}
	req.Offset = (req.Page - 1) * req.PageSize

	products, total, err := h.ProductSvc.FetchProducts(r.Context(), userID, req)
	if err != nil {
		log.Printf("Failed to fetch products for user %d: %v", userID, err)
		http.Error(w, "Failed to fetch products", http.StatusInternalServerError)
		return
	}

	resp := struct {
		Data  []model.Product `json:"data"`
		Total int             `json:"total"`
	}{
		Data:  products,
		Total: total,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// 注文を作成
func (h *ProductHandler) CreateOrders(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserFromContext(r.Context())
	if !ok {
		http.Error(w, "User not found in context", http.StatusInternalServerError)
		return
	}

	var req model.CreateOrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	insertedOrderIDs, err := h.ProductSvc.CreateOrders(r.Context(), userID, req.Items)
	if err != nil {
		log.Printf("Failed to create orders: %v", err)
		http.Error(w, "Failed to process order request", http.StatusInternalServerError)
		return
	}

	response := map[string]interface{}{
		"message":   "Orders created successfully",
		"order_ids": insertedOrderIDs,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(response)
}

func (h *ProductHandler) GetImage(w http.ResponseWriter, r *http.Request) {
	fmt.Printf("画像リクエスト受信: %s\n", r.URL.String())
	imagePath := r.URL.Query().Get("path")
	if imagePath == "" {
		fmt.Println("画像パスが空です")
		http.Error(w, "画像パスが指定されていません", http.StatusBadRequest)
		return
	}

	imagePath = filepath.Clean(imagePath)
	if filepath.IsAbs(imagePath) || strings.Contains(imagePath, "..") {
		fmt.Printf("無効なパス: %s\n", imagePath)
		http.Error(w, "無効なパスです", http.StatusBadRequest)
		return
	}

	// nginx でキャッシュを無効化しており、画像の取得が毎回行われるので、レギュレーションに違反しない
	accelURI := path.Join("/_protected/images", imagePath)
	w.Header().Set("X-Accel-Redirect", accelURI)

	w.WriteHeader(http.StatusOK)
}
