package config

import (
	"fmt"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

type Config struct {
	App      AppConfig
	DB       DBConfig
	Redis    RedisConfig
	Cashi    CashiConfig
	Security SecurityConfig
}

type AppConfig struct {
	Name        string
	Environment string
	Port        string
	URL         string
	FrontendURL string
}

type DBConfig struct {
	Host     string
	Port     string
	User     string
	Password string
	Name     string
	SSLMode  string
}

type RedisConfig struct {
	Host     string
	Port     string
	Password string
}

type CashiConfig struct {
	// BaseURL e.g. https://cashi.id (see docs/cashi-api.md)
	BaseURL string
	// APIKey sent as x-api-key header on create-order / check-status
	APIKey string
	// SecretKey used only to verify webhook HMAC signatures
	SecretKey string
}

type SecurityConfig struct {
	JWTSecret string
}

func Load() (*Config, error) {
	if err := godotenv.Load(); err != nil {
		return nil, fmt.Errorf("error loading .env file: %w", err)
	}

	config := &Config{
		App: AppConfig{
			Name:        getEnv("APP_NAME", "pg-aggregator"),
			Environment: getEnv("APP_ENV", "development"),
			Port:        getEnv("APP_PORT", "8080"),
			URL:         getEnv("APP_URL", ""),
			FrontendURL: getEnv("FRONTEND_URL", ""),
		},
		DB: DBConfig{
			Host:     getEnv("DB_HOST", "localhost"),
			Port:     getEnv("DB_PORT", "5432"),
			User:     getEnv("DB_USER", "postgres"),
			Password: getEnv("DB_PASSWORD", "postgres"),
			Name:     getEnv("DB_NAME", "pg_aggregator"),
			SSLMode:  getEnv("DB_SSLMODE", "disable"),
		},
		Redis: RedisConfig{
			Host:     getEnv("REDIS_HOST", "localhost"),
			Port:     getEnv("REDIS_PORT", "6379"),
			Password: getEnv("REDIS_PASSWORD", ""),
		},
		Cashi: CashiConfig{
			BaseURL:   getEnv("CASHI_BASE_URL", "https://cashi.id"),
			APIKey:    getEnv("CASHI_API_KEY", ""),
			SecretKey: getEnv("CASHI_SECRET_KEY", ""),
		},
		Security: SecurityConfig{
			JWTSecret: getEnv("JWT_SECRET", "change-this-secret"),
		},
	}

	if err := config.Validate(); err != nil {
		return nil, err
	}

	return config, nil
}

func (c *Config) Validate() error {
	if c.App.Name == "" {
		return fmt.Errorf("APP_NAME is required")
	}

	if c.App.URL == "" {
		return fmt.Errorf("APP_URL is required")
	}

	if c.App.FrontendURL == "" {
		return fmt.Errorf("FRONTEND_URL is required")
	}

	if c.DB.Host == "" || c.DB.Name == "" {
		return fmt.Errorf("database configuration is incomplete")
	}

	if c.App.Environment == "production" {
		if c.Cashi.APIKey == "" || c.Cashi.SecretKey == "" {
			return fmt.Errorf("Cashi credentials are required in production")
		}
		if c.Security.JWTSecret == "change-this-secret" {
			return fmt.Errorf("JWT_SECRET must be changed in production")
		}
	}

	return nil
}

func (c *DBConfig) ConnectionString() string {
	return fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		c.Host, c.Port, c.User, c.Password, c.Name, c.SSLMode,
	)
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvAsInt(key string, defaultValue int) int {
	valueStr := getEnv(key, "")
	if value, err := strconv.Atoi(valueStr); err == nil {
		return value
	}
	return defaultValue
}

func getEnvAsBool(key string, defaultValue bool) bool {
	valueStr := getEnv(key, "")
	if value, err := strconv.ParseBool(valueStr); err == nil {
		return value
	}
	return defaultValue
}
