package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/config"
	"github.com/akbarryyan/pg-aggregator-back/internal/handler"
	"github.com/akbarryyan/pg-aggregator-back/internal/middleware"
	"github.com/akbarryyan/pg-aggregator-back/internal/provider"
	"github.com/akbarryyan/pg-aggregator-back/internal/provider/cashi"
	"github.com/akbarryyan/pg-aggregator-back/internal/provider/sandbox"
	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
	"github.com/akbarryyan/pg-aggregator-back/internal/service"
	"github.com/akbarryyan/pg-aggregator-back/pkg/logger"

	"github.com/gorilla/mux"
	_ "github.com/lib/pq"
	"github.com/rs/cors"
)

func main() {
	logger.Init("info")
	logger.Info("Starting Payment Gateway Aggregator API...")

	cfg, err := config.Load()
	if err != nil {
		logger.Errorf("Failed to load config: %v", err)
		os.Exit(1)
	}

	db, err := initDB(cfg)
	if err != nil {
		logger.Errorf("Failed to connect to database: %v", err)
		os.Exit(1)
	}
	defer db.Close()

	paymentRepo := repository.NewPaymentRepository(db)
	merchantRepo := repository.NewMerchantRepository(db)
	merchantProviderConfigRepo := repository.NewMerchantProviderConfigRepository(db)
	webhookEventRepo := repository.NewWebhookEventRepository(db)
	callbackRepo := repository.NewMerchantCallbackRepository(db)
	apiKeyRepo := repository.NewMerchantAPIKeyRepository(db)
	merchantUserRepo := repository.NewMerchantUserRepository(db)
	adminRepo := repository.NewAdminRepository(db)
	cashiAdapter := cashi.NewCashiAdapter(
		cfg.Cashi.BaseURL,
		cfg.Cashi.APIKey,
		cfg.Cashi.SecretKey,
	)
	sandboxAdapter := sandbox.NewAdapter()

	providerRouter := provider.NewProviderRouter()
	providerRouter.RegisterProvider(cashiAdapter)
	providerRouter.RegisterProvider(sandboxAdapter)
	// Production QRIS routing uses real Cashi only
	providerRouter.RegisterPaymentMethodProvider("qris", cashiAdapter.GetName())

	paymentService := service.NewPaymentService(paymentRepo, merchantProviderConfigRepo, webhookEventRepo, providerRouter, cfg.App.URL).
		WithMerchantCallbackDeps(merchantRepo, callbackRepo).
		WithSandboxProvider(sandboxAdapter)
	providerRoutingService := service.NewProviderRoutingService(merchantProviderConfigRepo, providerRouter)
	authService := service.NewAuthService(adminRepo, cfg.Security.JWTSecret).
		WithMerchantAuth(merchantUserRepo, merchantRepo)
	apiKeyService := service.NewMerchantAPIKeyService(apiKeyRepo, merchantRepo)
	adminService := service.NewAdminService(merchantRepo, paymentRepo, webhookEventRepo, merchantProviderConfigRepo, providerRouter).
		WithCallbackRepo(callbackRepo)

	paymentHandler := handler.NewPaymentHandler(paymentService, cfg.App.FrontendURL)
	webhookHandler := handler.NewWebhookHandler(paymentService)
	providerRoutingHandler := handler.NewProviderRoutingHandler(providerRoutingService)
	authHandler := handler.NewAuthHandler(authService)
	adminHandler := handler.NewAdminHandler(adminService, paymentService, cfg.App.FrontendURL).
		WithAPIKeyService(apiKeyService).
		WithAuthService(authService)
	merchantHandler := handler.NewMerchantHandler(adminService, paymentService, apiKeyService, authService, cfg.App.FrontendURL)
	authMiddleware := middleware.NewAuthMiddleware(authService)
	merchantAPIAuth := middleware.NewMerchantAPIAuthMiddleware(apiKeyService)

	router := setupRouter(paymentHandler, webhookHandler, providerRoutingHandler, authHandler, adminHandler, merchantHandler, authMiddleware, merchantAPIAuth)

	c := cors.New(cors.Options{
		AllowedOrigins:   []string{cfg.App.FrontendURL},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Content-Type", "Authorization", "X-API-Key"},
		AllowCredentials: true,
	})

	handler := c.Handler(router)

	srv := &http.Server{
		Addr:         ":" + cfg.App.Port,
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		logger.Infof("Server is running on port %s", cfg.App.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Errorf("Server failed to start: %v", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("Shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		logger.Errorf("Server forced to shutdown: %v", err)
	}

	logger.Info("Server exited")
}

func initDB(cfg *config.Config) (*sql.DB, error) {
	db, err := sql.Open("postgres", cfg.DB.ConnectionString())
	if err != nil {
		return nil, fmt.Errorf("failed to open database connection: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	logger.Info("Database connection established")
	return db, nil
}

func setupRouter(
	paymentHandler *handler.PaymentHandler,
	webhookHandler *handler.WebhookHandler,
	providerRoutingHandler *handler.ProviderRoutingHandler,
	authHandler *handler.AuthHandler,
	adminHandler *handler.AdminHandler,
	merchantHandler *handler.MerchantHandler,
	authMiddleware *middleware.AuthMiddleware,
	merchantAPIAuth *middleware.MerchantAPIAuthMiddleware,
) *mux.Router {
	r := mux.NewRouter()

	api := r.PathPrefix("/api/v1").Subrouter()

	// Merchant dashboard login (public)
	api.HandleFunc("/auth/login", authHandler.LoginMerchant).Methods("POST")
	api.HandleFunc("/auth/me", authHandler.GetMerchantMe).Methods("GET")
	api.HandleFunc("/auth/profile", authHandler.UpdateMerchantProfile).Methods("PUT")
	api.HandleFunc("/auth/change-password", authHandler.ChangeMerchantPassword).Methods("POST")

	api.HandleFunc("/auth/admin/login", authHandler.LoginAdmin).Methods("POST")
	api.HandleFunc("/auth/admin/me", authHandler.GetMe).Methods("GET")
	api.HandleFunc("/auth/admin/profile", authHandler.UpdateProfile).Methods("PUT")
	api.HandleFunc("/auth/admin/change-password", authHandler.ChangePassword).Methods("POST")

	// Protected admin panel APIs
	adminAPI := api.PathPrefix("/admin").Subrouter()
	adminAPI.Use(authMiddleware.RequireAdmin)
	adminAPI.HandleFunc("/dashboard/summary", adminHandler.GetDashboardSummary).Methods("GET")
	adminAPI.HandleFunc("/dashboard/charts", adminHandler.GetDashboardCharts).Methods("GET")
	adminAPI.HandleFunc("/payments", adminHandler.ListPayments).Methods("GET")
	adminAPI.HandleFunc("/payments", adminHandler.CreatePayment).Methods("POST")
	adminAPI.HandleFunc("/payments/export", adminHandler.ExportPayments).Methods("GET")
	adminAPI.HandleFunc("/payments/{id}", adminHandler.GetPayment).Methods("GET")
	adminAPI.HandleFunc("/payments/{id}/events", adminHandler.ListPaymentEvents).Methods("GET")
	adminAPI.HandleFunc("/merchants", adminHandler.ListMerchants).Methods("GET")
	adminAPI.HandleFunc("/merchants", adminHandler.CreateMerchant).Methods("POST")
	adminAPI.HandleFunc("/merchants/export", adminHandler.ExportMerchants).Methods("GET")
	adminAPI.HandleFunc("/merchants/{id}", adminHandler.GetMerchant).Methods("GET")
	adminAPI.HandleFunc("/merchants/{id}", adminHandler.UpdateMerchant).Methods("PUT")
	adminAPI.HandleFunc("/merchants/{id}/status", adminHandler.SetMerchantActive).Methods("PUT")
	adminAPI.HandleFunc("/merchants/{id}/payments", adminHandler.ListMerchantPayments).Methods("GET")
	adminAPI.HandleFunc("/merchants/{id}/api-keys", adminHandler.ListMerchantAPIKeys).Methods("GET")
	adminAPI.HandleFunc("/merchants/{id}/api-keys", adminHandler.UpsertMerchantAPIKey).Methods("PUT", "POST")
	adminAPI.HandleFunc("/merchants/{id}/api-keys/{keyId}", adminHandler.DeleteMerchantAPIKey).Methods("DELETE")
	adminAPI.HandleFunc("/providers", adminHandler.ListProviders).Methods("GET")
	adminAPI.HandleFunc("/providers/{name}", adminHandler.GetProvider).Methods("GET")
	adminAPI.HandleFunc("/providers/{name}/health", adminHandler.UpdateProviderHealth).Methods("PUT")
	adminAPI.HandleFunc("/routing", adminHandler.ListRouting).Methods("GET")
	adminAPI.HandleFunc("/reconciliation", adminHandler.ListReconciliation).Methods("GET")
	adminAPI.HandleFunc("/reconciliation/check", adminHandler.CheckReconciliationBatch).Methods("POST")
	adminAPI.HandleFunc("/reconciliation/{id}/check", adminHandler.CheckReconciliationPayment).Methods("POST")
	adminAPI.HandleFunc("/merchants/{id}/provider-configs", adminHandler.ListMerchantProviderConfigs).Methods("GET")
	adminAPI.HandleFunc("/merchants/{id}/provider-configs", adminHandler.UpsertMerchantProviderConfig).Methods("POST", "PUT")
	adminAPI.HandleFunc("/merchants/{id}/provider-configs", adminHandler.DeleteMerchantProviderConfig).Methods("DELETE")
	adminAPI.HandleFunc("/notifications", adminHandler.ListNotifications).Methods("GET")
	adminAPI.HandleFunc("/callbacks", adminHandler.ListCallbacks).Methods("GET")
	adminAPI.HandleFunc("/callbacks/{id}/retry", adminHandler.RetryCallback).Methods("POST")
	adminAPI.HandleFunc("/payments/{id}/callbacks", adminHandler.ListPaymentCallbacks).Methods("GET")
	adminAPI.HandleFunc("/logs", adminHandler.ListLogs).Methods("GET")
	adminAPI.HandleFunc("/logs/export", adminHandler.ExportLogs).Methods("GET")
	adminAPI.HandleFunc("/logs/{id}", adminHandler.GetLog).Methods("GET")

	// Merchant dashboard APIs (JWT session)
	merchantDash := api.PathPrefix("/merchant").Subrouter()
	merchantDash.Use(authMiddleware.RequireMerchant)
	merchantDash.HandleFunc("/dashboard/summary", merchantHandler.GetDashboardSummary).Methods("GET")
	merchantDash.HandleFunc("/dashboard/charts", merchantHandler.GetDashboardCharts).Methods("GET")
	merchantDash.HandleFunc("/notifications", merchantHandler.ListNotifications).Methods("GET")
	merchantDash.HandleFunc("/callbacks", merchantHandler.ListCallbacks).Methods("GET")
	merchantDash.HandleFunc("/payments", merchantHandler.ListPayments).Methods("GET")
	merchantDash.HandleFunc("/payments/export", merchantHandler.ExportPayments).Methods("GET")
	merchantDash.HandleFunc("/payments", merchantHandler.CreatePayment).Methods("POST")
	merchantDash.HandleFunc("/payments/{id}", merchantHandler.GetPayment).Methods("GET")
	merchantDash.HandleFunc("/api-keys", merchantHandler.ListAPIKeys).Methods("GET")
	merchantDash.HandleFunc("/api-keys", merchantHandler.UpsertAPIKey).Methods("PUT", "POST")
	merchantDash.HandleFunc("/api-keys/{keyId}", merchantHandler.DeleteAPIKey).Methods("DELETE")
	merchantDash.HandleFunc("/business", merchantHandler.GetBusiness).Methods("GET")
	merchantDash.HandleFunc("/business", merchantHandler.UpdateBusiness).Methods("PUT")

	// Public checkout (no auth) — shareable payment link
	api.HandleFunc("/public/payments/by-reference/{reference}", paymentHandler.GetPaymentByReference).Methods("GET")

	// Merchant public API (API key required)
	merchantAPI := api.PathPrefix("").Subrouter()
	merchantAPI.Use(merchantAPIAuth.RequireMerchantAPIKey)
	merchantAPI.HandleFunc("/payments", paymentHandler.CreatePayment).Methods("POST")
	merchantAPI.HandleFunc("/payments/{id}", paymentHandler.GetPayment).Methods("GET")
	merchantAPI.HandleFunc("/payments/{id}/status", paymentHandler.GetPaymentStatus).Methods("GET")

	api.HandleFunc("/provider-webhooks/{providerName}", webhookHandler.HandleProviderWebhook).Methods("POST")
	api.HandleFunc("/merchants/{merchantID}/provider-configs", providerRoutingHandler.ListMerchantProviderConfigs).Methods("GET")
	api.HandleFunc("/merchants/{merchantID}/provider-configs", providerRoutingHandler.UpsertMerchantProviderConfig).Methods("POST", "PUT")
	api.HandleFunc("/merchants/{merchantID}/provider-configs", providerRoutingHandler.DeleteMerchantProviderConfig).Methods("DELETE")
	api.HandleFunc("/provider-healths", providerRoutingHandler.ListProviderHealths).Methods("GET")
	api.HandleFunc("/provider-healths/{providerName}", providerRoutingHandler.UpdateProviderHealth).Methods("PUT")

	api.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}).Methods("GET")

	return r
}
