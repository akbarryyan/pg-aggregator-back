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

	"pg-aggregator/internal/config"
	"pg-aggregator/internal/handler"
	"pg-aggregator/internal/provider/klikqris"
	"pg-aggregator/internal/repository"
	"pg-aggregator/internal/service"
	"pg-aggregator/pkg/logger"

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

	klikqrisAdapter := klikqris.NewKlikQrisAdapter(
		cfg.KlikQris.BaseURL,
		cfg.KlikQris.APIKey,
		cfg.KlikQris.SecretKey,
		cfg.KlikQris.MerchantID,
	)

	paymentService := service.NewPaymentService(paymentRepo, klikqrisAdapter)

	paymentHandler := handler.NewPaymentHandler(paymentService)
	webhookHandler := handler.NewWebhookHandler(paymentService)

	router := setupRouter(paymentHandler, webhookHandler)

	c := cors.New(cors.Options{
		AllowedOrigins:   []string{cfg.App.FrontendURL},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Content-Type", "Authorization"},
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

func setupRouter(paymentHandler *handler.PaymentHandler, webhookHandler *handler.WebhookHandler) *mux.Router {
	r := mux.NewRouter()

	api := r.PathPrefix("/api/v1").Subrouter()

	api.HandleFunc("/payments", paymentHandler.CreatePayment).Methods("POST")
	api.HandleFunc("/payments/{id}", paymentHandler.GetPayment).Methods("GET")
	api.HandleFunc("/payments/{id}/status", paymentHandler.GetPaymentStatus).Methods("GET")

	api.HandleFunc("/provider-webhooks/klikqris", webhookHandler.HandleKlikQrisWebhook).Methods("POST")

	api.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}).Methods("GET")

	return r
}
