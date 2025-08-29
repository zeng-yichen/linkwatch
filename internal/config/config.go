package config

import (
	"os"
	"strconv"
	"time"
)

// Config holds the application's configuration values.
type Config struct {
	DatabaseDriver string
	DatabaseURL    string
	CheckInterval  time.Duration
	MaxConcurrency int
	HTTPTimeout    time.Duration
	ShutdownGrace  time.Duration
	HTTPPort       string
}

// Load loads configuration from environment variables with sane defaults.
func Load() *Config {
	return &Config{
		DatabaseDriver: getEnv("DATABASE_DRIVER", "sqlite"),
		DatabaseURL:    getEnv("DATABASE_URL", "linkwatch.db"),
		CheckInterval:  getEnvDuration("CHECK_INTERVAL", 15*time.Second),
		MaxConcurrency: getEnvInt("MAX_CONCURRENCY", 8),
		HTTPTimeout:    getEnvDuration("HTTP_TIMEOUT", 5*time.Second),
		ShutdownGrace:  getEnvDuration("SHUTDOWN_GRACE", 10*time.Second),
		HTTPPort:       getEnv("HTTP_PORT", "8080"),
	}
}

// Helper function to get an environment variable or return a default value.
func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}

// Helper function to get an environment variable as an integer.
func getEnvInt(key string, fallback int) int {
	if valueStr, exists := os.LookupEnv(key); exists {
		if value, err := strconv.Atoi(valueStr); err == nil {
			return value
		}
	}
	return fallback
}

// Helper function to get an environment variable as a time.Duration.
func getEnvDuration(key string, fallback time.Duration) time.Duration {
	if valueStr, exists := os.LookupEnv(key); exists {
		if value, err := time.ParseDuration(valueStr); err == nil {
			return value
		}
	}
	return fallback
}
