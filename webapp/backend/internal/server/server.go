package server

import (
	"backend/internal/db"
	"backend/internal/handler"
	"backend/internal/middleware"
	"backend/internal/repository"
	"backend/internal/service"
	"errors"
	"log"
	"net"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"
	pprotein "github.com/kaz/pprotein/integration"
)

type Server struct {
	Router *chi.Mux
}

func NewServer() (*Server, *sqlx.DB, error) {
	dbConn, err := db.InitDBConnection()
	if err != nil {
		return nil, nil, err
	}

	store := repository.NewStore(dbConn)

	authService := service.NewAuthService(store)
	orderService := service.NewOrderService(store)
	productService := service.NewProductService(store)
	robotService := service.NewRobotService(store)

	authHandler := handler.NewAuthHandler(authService)
	productHandler := handler.NewProductHandler(productService)
	orderHandler := handler.NewOrderHandler(orderService)
	robotHandler := handler.NewRobotHandler(robotService)

	userAuthMW := middleware.UserAuthMiddleware(store.SessionRepo)

	robotAPIKey := os.Getenv("ROBOT_API_KEY")
	if robotAPIKey == "" {
		log.Println("Warning: ROBOT_API_KEY is not set. Using default key 'test-robot-key'")
		robotAPIKey = "test-robot-key"
	}
	robotAuthMW := middleware.RobotAuthMiddleware(robotAPIKey)

	r := chi.NewRouter()

	r.Handle("/debug/*", pprotein.NewDebugHandler())

	r.Get("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	s := &Server{
		Router: r,
	}

	s.setupRoutes(authHandler, productHandler, orderHandler, robotHandler, userAuthMW, robotAuthMW)

	return s, dbConn, nil
}

func (s *Server) setupRoutes(
	authHandler *handler.AuthHandler,
	productHandler *handler.ProductHandler,
	orderHandler *handler.OrderHandler,
	robotHandler *handler.RobotHandler,
	userAuthMW func(http.Handler) http.Handler,
	robotAuthMW func(http.Handler) http.Handler,
) {
	s.Router.Post("/api/login", authHandler.Login)

	s.Router.Route("/api/v1", func(r chi.Router) {
		r.Use(userAuthMW)
		r.Post("/product", productHandler.List)
		r.Post("/product/post", productHandler.CreateOrders)
		r.Post("/orders", orderHandler.List)
		r.Get("/image", productHandler.GetImage)
	})

	s.Router.Route("/api/robot", func(r chi.Router) {
		r.Use(robotAuthMW)
		r.Get("/delivery-plan", robotHandler.GetDeliveryPlan)
		r.Patch("/orders/status", robotHandler.UpdateOrderStatus)
	})
}

func (s *Server) Run() {
	// pprotein 用
	tcpSrv := &http.Server{
		Addr:    ":8080",
		Handler: s.Router,
	}
	go func() {
		log.Printf("Starting server on tcp :8080")
		if err := tcpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("tcp server error: %v", err)
		}
	}()

	socketPath := os.Getenv("APP_SOCKET_PATH")
	if socketPath == "" {
		socketPath = "/var/run/app/app.sock"
	}
	_ = os.Remove(socketPath)

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		log.Fatalf("listen unix: %v", err)
	}
	if err := os.Chmod(socketPath, 0666); err != nil {
		log.Printf("chmod socket: %v", err)
	}

	unixSrv := &http.Server{
		Handler: s.Router,
	}

	log.Printf("Starting server on unix socket %s", socketPath)
	if err := unixSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server error: %v", err)
	}
}
