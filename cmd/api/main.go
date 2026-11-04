package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/akbarryyan/pg-aggregator-back/internal/config"
	"github.com/akbarryyan/pg-aggregator-back/internal/handler"
	"github.com/akbarryyan/pg-aggregator-back/internal/middleware"
	"github.com/akbarryyan/pg-aggregator-back/internal/netsafe"
	"github.com/akbarryyan/pg-aggregator-back/internal/provider"
	"github.com/akbarryyan/pg-aggregator-back/internal/provider/cashi"
	"github.com/akbarryyan/pg-aggregator-back/internal/provider/sandbox"
	"github.com/akbarryyan/pg-aggregator-back/internal/repository"
	"github.com/akbarryyan/pg-aggregator-back/internal/scheduler"
	"github.com/akbarryyan/pg-aggregator-back/internal/service"
	"github.com/akbarryyan/pg-aggregator-back/pkg/logger"

	"github.com/gorilla/mux"
	_ "github.com/lib/pq"
	"github.com/rs/cors"
)

func main() {
	configPath := flag.String("config", ".config.toml", "path to the TOML config file")
	healthcheck := flag.Bool("healthcheck", false, "probe the local /api/v1/health endpoint and exit 0 (healthy) or 1; used by the Docker healthcheck")
	flag.Parse()

	if *healthcheck {
		cfg, err := config.Load(*configPath)
		if err != nil {
			os.Exit(1) // logger is not initialised: stay silent, the exit code is the signal
		}
		os.Exit(runHealthcheck(cfg.App.Port, &http.Client{Timeout: healthcheckClientTimeout}))
	}

	logger.Init("info")
	logger.Info("Starting Payment Gateway Aggregator API...")

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Errorf("Failed to load config: %v", err)
		os.Exit(1)
	}
	logger.SetLevel(levelForEnvironment(cfg.App.Environment))

	// Our own API and frontend hosts resolve to public IPs, so the SSRF IP
	// guard can't stop a merchant from pointing a callback at us.
	netsafe.SetBlockedHosts([]string{cfg.App.URL, cfg.App.FrontendURL})

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
	idempotencyRepo := repository.NewIdempotencyRepository(db)
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

	paymentLinkRepo := repository.NewPaymentLinkRepository(db)

	paymentService := service.NewPaymentService(paymentRepo, merchantProviderConfigRepo, webhookEventRepo, providerRouter, cfg.App.URL).
		WithMerchantCallbackDeps(merchantRepo, callbackRepo).
		WithSandboxProvider(sandboxAdapter).
		WithIdempotency(idempotencyRepo)
	paymentLinkService := service.NewPaymentLinkService(paymentLinkRepo, paymentRepo, paymentService)
	authService := service.NewAuthService(adminRepo, cfg.Security.JWTSecret).
		WithMerchantAuth(merchantUserRepo, merchantRepo)
	apiKeyService := service.NewMerchantAPIKeyService(apiKeyRepo, merchantRepo)
	// AdminService (the original god-object) has been fully split, per
	// project backlog item #9, into these cohesive services — it no longer
	// exists as a type.
	adminPaymentService := service.NewAdminPaymentService(paymentRepo, merchantRepo)
	adminMerchantService := service.NewAdminMerchantService(merchantRepo, merchantProviderConfigRepo, adminPaymentService).WithProviderRegistry(providerRouter)
	adminProviderService := service.NewAdminProviderService(paymentRepo, merchantProviderConfigRepo, providerRouter)
	adminDashboardService := service.NewAdminDashboardService(merchantRepo, paymentRepo, webhookEventRepo, adminPaymentService)
	adminNotificationService := service.NewAdminNotificationService(paymentRepo, webhookEventRepo).
		WithCallbackRepo(callbackRepo)
	adminLogService := service.NewAdminLogService(paymentRepo, webhookEventRepo)
	adminCallbackService := service.NewAdminCallbackService(callbackRepo)

	paymentHandler := handler.NewPaymentHandler(paymentService, cfg.App.FrontendURL)
	webhookHandler := handler.NewWebhookHandler(paymentService)
	authHandler := handler.NewAuthHandler(authService)
	adminHandler := handler.NewAdminHandler(paymentService, cfg.App.FrontendURL).
		WithAPIKeyService(apiKeyService).
		WithAuthService(authService).
		WithMerchantAndPaymentServices(adminPaymentService, adminMerchantService).
		WithProviderService(adminProviderService).
		WithReportingServices(adminDashboardService, adminNotificationService, adminLogService, adminCallbackService)
	merchantHandler := handler.NewMerchantHandler(paymentService, apiKeyService, authService, cfg.App.FrontendURL).
		WithMerchantAndPaymentServices(adminPaymentService, adminMerchantService).
		WithReportingServices(adminDashboardService, adminNotificationService, adminCallbackService)
	paymentLinkHandler := handler.NewPaymentLinkHandler(paymentLinkService, cfg.App.FrontendURL)
	authMiddleware := middleware.NewAuthMiddleware(authService)
	merchantAPIAuth := middleware.NewMerchantAPIAuthMiddleware(apiKeyService)

	// Rate limiting on public endpoints (per system-design.md's security
	// rules): login is the classic brute-force target so it gets a tight
	// budget; webhook/checkout-polling/merchant-API get a looser one just
	// to blunt flooding, since they're otherwise legitimate high-frequency
	// traffic. sensitiveRateLimiter sits between the two: applied to
	// already-authenticated account-mutation actions (change password,
	// create/rotate/revoke API keys, regenerate webhook secret) that were
	// previously unlimited — a stolen/leaked token could otherwise hammer
	// them with no throttling at all. apiRateLimiter covers the remaining
	// authenticated API traffic (admin panel, merchant dashboard, and
	// POST /payments by API key) with its own bucket, so polling on the public
	// limiter can't starve payment creation. Per-client-IP, in-process — see
	// internal/middleware/rate_limit.go. Budgets and the trusted reverse
	// proxies (Caddy on the VPS) come from [rate_limit] in .config.toml.
	trustedProxies, err := cfg.RateLimit.TrustedProxyPrefixes()
	if err != nil {
		// Validate already parsed these, so this only fires if the two drift.
		logger.Errorf("Invalid rate limit config: %v", err)
		os.Exit(1)
	}
	authRateLimiter := newRateLimiter(cfg.RateLimit.Auth, trustedProxies)
	publicRateLimiter := newRateLimiter(cfg.RateLimit.Public, trustedProxies)
	sensitiveRateLimiter := newRateLimiter(cfg.RateLimit.Sensitive, trustedProxies)
	apiRateLimiter := newRateLimiter(cfg.RateLimit.API, trustedProxies)

	// Background jobs: expire unpaid payments past their deadline, send
	// queued merchant callbacks (and retry failed ones) every few seconds,
	// re-check recent pending/expired production payments with the provider
	// (reconcile-payments), and evict idle rate-limit buckets. A single
	// in-process ticker is enough at this stage (see internal/scheduler) — no
	// Redis/Asynq needed until the API runs as more than one instance. Each
	// job is tracked so shutdown can wait for in-flight work to finish.
	bgCtx, cancelBackgroundJobs := context.WithCancel(context.Background())
	defer cancelBackgroundJobs()
	var jobs sync.WaitGroup
	startJob := func(interval time.Duration, name string, task func(context.Context) error) {
		jobs.Add(1)
		go func() {
			defer jobs.Done()
			scheduler.RunPeriodic(bgCtx, interval, name, task)
		}()
	}
	startJob(time.Minute, "expire-payments", paymentService.ExpirePayments)
	startJob(time.Hour, "purge-idempotency-keys", paymentService.PurgeIdempotencyKeys)
	startJob(service.CallbackDeliveryInterval, "deliver-merchant-callbacks", paymentService.DeliverMerchantCallbacks)
	startJob(service.ReconcileInterval, "reconcile-payments", paymentService.ReconcileRecentPayments)
	startJob(10*time.Minute, "rate-limiter-cleanup", func(ctx context.Context) error {
		authRateLimiter.Cleanup(30 * time.Minute)
		publicRateLimiter.Cleanup(30 * time.Minute)
		sensitiveRateLimiter.Cleanup(30 * time.Minute)
		apiRateLimiter.Cleanup(30 * time.Minute)
		return nil
	})

	router := setupRouter(paymentHandler, webhookHandler, authHandler, adminHandler, merchantHandler, paymentLinkHandler, authMiddleware, merchantAPIAuth, authRateLimiter, publicRateLimiter, sensitiveRateLimiter, apiRateLimiter, db.PingContext)

	c := cors.New(cors.Options{
		AllowedOrigins:   []string{cfg.App.FrontendURL},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Content-Type", "Authorization", "X-API-Key", "Idempotency-Key"},
		ExposedHeaders:   []string{"Idempotent-Replayed"},
		AllowCredentials: true,
	})

	// 1 MiB cap on every request body.
	handler := middleware.LimitBody(1 << 20)(c.Handler(router))

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.App.Port),
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,

		ReadHeaderTimeout: 5 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}

	go func() {
		logger.Infof("Server is running on port %d", cfg.App.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Errorf("Server failed to start: %v", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("Shutting down server...")
	cancelBackgroundJobs()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		logger.Errorf("Server forced to shutdown: %v", err)
	}

	if !waitForJobs(&jobs, 15*time.Second) {
		logger.Warnf("Background jobs did not finish within 15s; unsent callbacks will be re-claimed after restart")
	}

	logger.Info("Server exited")
}

// Connection pool sizing for the single-instance deployment (see DEPLOY.md:
// the API must stay one replica because background jobs have no leader lock).
const (
	dbMaxOpenConns    = 25
	dbMaxIdleConns    = 5
	dbConnMaxLifetime = 5 * time.Minute
)

func initDB(cfg *config.Config) (*sql.DB, error) {
	db, err := sql.Open("postgres", cfg.DB.ConnectionString())
	if err != nil {
		return nil, fmt.Errorf("failed to open database connection: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	db.SetMaxOpenConns(dbMaxOpenConns)
	db.SetMaxIdleConns(dbMaxIdleConns)
	db.SetConnMaxLifetime(dbConnMaxLifetime)

	logger.Info("Database connection established")
	return db, nil
}

func newRateLimiter(rule config.RateLimitRule, trustedProxies []netip.Prefix) *middleware.IPRateLimiter {
	return middleware.NewIPRateLimiter(float64(rule.RequestsPerMinute), rule.Burst).
		WithTrustedProxies(trustedProxies)
}

func setupRouter(
	paymentHandler *handler.PaymentHandler,
	webhookHandler *handler.WebhookHandler,
	authHandler *handler.AuthHandler,
	adminHandler *handler.AdminHandler,
	merchantHandler *handler.MerchantHandler,
	paymentLinkHandler *handler.PaymentLinkHandler,
	authMiddleware *middleware.AuthMiddleware,
	merchantAPIAuth *middleware.MerchantAPIAuthMiddleware,
	authRateLimiter *middleware.IPRateLimiter,
	publicRateLimiter *middleware.IPRateLimiter,
	sensitiveRateLimiter *middleware.IPRateLimiter,
	apiRateLimiter *middleware.IPRateLimiter,
	healthCheck func(context.Context) error,
) http.Handler {
	r := mux.NewRouter()

	api := r.PathPrefix("/api/v1").Subrouter()

	// Merchant dashboard login (public) — rate limited, classic brute-force target.
	api.Handle("/auth/login", authRateLimiter.Limit(http.HandlerFunc(authHandler.LoginMerchant))).Methods("POST")
	api.Handle("/auth/register", authRateLimiter.Limit(http.HandlerFunc(authHandler.RegisterMerchant))).Methods("POST")
	// Handler-authenticated account routes cost 1-2 DB queries per request, so
	// they share the per-IP api bucket (spec 3.5) instead of staying unlimited.
	api.Handle("/auth/logout", apiRateLimiter.Limit(http.HandlerFunc(authHandler.LogoutMerchant))).Methods("POST")
	api.Handle("/auth/me", apiRateLimiter.Limit(http.HandlerFunc(authHandler.GetMerchantMe))).Methods("GET")
	api.Handle("/auth/profile", apiRateLimiter.Limit(http.HandlerFunc(authHandler.UpdateMerchantProfile))).Methods("PUT")
	// change-password is auth-gated (JWT parsed inside the handler) but was
	// previously unlimited — a leaked/stolen token could hammer it with no
	// throttling at all.
	api.Handle("/auth/change-password", sensitiveRateLimiter.Limit(http.HandlerFunc(authHandler.ChangeMerchantPassword))).Methods("POST")

	api.Handle("/auth/admin/login", authRateLimiter.Limit(http.HandlerFunc(authHandler.LoginAdmin))).Methods("POST")
	api.Handle("/auth/admin/logout", apiRateLimiter.Limit(http.HandlerFunc(authHandler.LogoutAdmin))).Methods("POST")
	api.Handle("/auth/admin/me", apiRateLimiter.Limit(http.HandlerFunc(authHandler.GetMe))).Methods("GET")
	api.Handle("/auth/admin/profile", apiRateLimiter.Limit(http.HandlerFunc(authHandler.UpdateProfile))).Methods("PUT")
	api.Handle("/auth/admin/change-password", sensitiveRateLimiter.Limit(http.HandlerFunc(authHandler.ChangePassword))).Methods("POST")

	// Protected admin panel APIs
	adminAPI := api.PathPrefix("/admin").Subrouter()
	// Use() order is execution order: throttle before the (DB-backed) auth
	// check so forged tokens can't be used to hammer the database.
	adminAPI.Use(apiRateLimiter.Limit)
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
	// API key create/rotate/revoke — sensitive account-mutation actions,
	// previously unlimited once past JWT auth.
	adminAPI.Handle("/merchants/{id}/api-keys", sensitiveRateLimiter.Limit(http.HandlerFunc(adminHandler.UpsertMerchantAPIKey))).Methods("PUT", "POST")
	adminAPI.Handle("/merchants/{id}/api-keys/{keyId}", sensitiveRateLimiter.Limit(http.HandlerFunc(adminHandler.DeleteMerchantAPIKey))).Methods("DELETE")
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
	merchantDash.Use(apiRateLimiter.Limit)
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
	// API key create/rotate/revoke and webhook-secret rotation — sensitive
	// account-mutation actions, previously unlimited once past JWT auth.
	merchantDash.Handle("/api-keys", sensitiveRateLimiter.Limit(http.HandlerFunc(merchantHandler.UpsertAPIKey))).Methods("PUT", "POST")
	merchantDash.Handle("/api-keys/{keyId}", sensitiveRateLimiter.Limit(http.HandlerFunc(merchantHandler.DeleteAPIKey))).Methods("DELETE")
	merchantDash.HandleFunc("/business", merchantHandler.GetBusiness).Methods("GET")
	merchantDash.HandleFunc("/business", merchantHandler.UpdateBusiness).Methods("PUT")
	merchantDash.HandleFunc("/webhook-secret", merchantHandler.GetWebhookSecret).Methods("GET")
	merchantDash.Handle("/webhook-secret/regenerate", sensitiveRateLimiter.Limit(http.HandlerFunc(merchantHandler.RegenerateWebhookSecret))).Methods("POST")
	merchantDash.HandleFunc("/payment-links", paymentLinkHandler.ListPaymentLinks).Methods("GET")
	merchantDash.HandleFunc("/payment-links", paymentLinkHandler.CreatePaymentLink).Methods("POST")
	merchantDash.HandleFunc("/payment-links/{id}", paymentLinkHandler.GetPaymentLink).Methods("GET")
	merchantDash.HandleFunc("/payment-links/{id}", paymentLinkHandler.UpdatePaymentLink).Methods("PUT")
	merchantDash.HandleFunc("/payment-links/{id}/status", paymentLinkHandler.SetPaymentLinkActive).Methods("PUT")
	merchantDash.HandleFunc("/payment-links/{id}/payments", paymentLinkHandler.ListPaymentLinkPayments).Methods("GET")

	// Public checkout (no auth) — shareable payment link, polled by the checkout page.
	api.Handle("/public/payments/by-reference/{reference}", publicRateLimiter.Limit(http.HandlerFunc(paymentHandler.GetPaymentByReference))).Methods("GET")

	// Public Payment Link resolve + pay (no auth) — a reusable link; every
	// checkout here spawns a fresh one-time Payment (see PaymentLinkService).
	api.Handle("/public/payment-links/{slug}", publicRateLimiter.Limit(http.HandlerFunc(paymentLinkHandler.GetPublicPaymentLink))).Methods("GET")
	api.Handle("/public/payment-links/{slug}/pay", publicRateLimiter.Limit(http.HandlerFunc(paymentLinkHandler.InitiatePaymentLinkCheckout))).Methods("POST")

	// Merchant public API (API key required). Each route is wrapped
	// limiter-first, then the (DB-hitting) API key auth, so a flood of bad
	// keys can't be used to hammer the database. Payment creation has its own
	// apiRateLimiter bucket, separate from the publicRateLimiter shared by
	// status polling, so polling can't exhaust a merchant's create budget.
	api.Handle("/payments", apiRateLimiter.Limit(merchantAPIAuth.RequireMerchantAPIKey(http.HandlerFunc(paymentHandler.CreatePayment)))).Methods("POST")
	api.Handle("/payments/{id}", publicRateLimiter.Limit(merchantAPIAuth.RequireMerchantAPIKey(http.HandlerFunc(paymentHandler.GetPayment)))).Methods("GET")
	api.Handle("/payments/{id}/status", publicRateLimiter.Limit(merchantAPIAuth.RequireMerchantAPIKey(http.HandlerFunc(paymentHandler.GetPaymentStatus)))).Methods("GET")

	// Provider webhook (e.g. Cashi) — signature-verified downstream, but still
	// rate limited per source IP to blunt flooding/DoS attempts.
	api.Handle("/provider-webhooks/{providerName}", publicRateLimiter.Limit(http.HandlerFunc(webhookHandler.HandleProviderWebhook))).Methods("POST")

	// Readiness: pings the database. Rate limited like the other public routes
	// since it costs a DB round trip.
	api.Handle("/health", publicRateLimiter.Limit(newHealthHandler(healthCheck))).Methods("GET")

	// Wraps the whole router (not mux.Router.Use, which gorilla/mux does
	// NOT run for unmatched routes) so every response — including a 404 for
	// an unknown path — carries a correlation ID (project backlog #10).
	return middleware.RequestID(r)
}
