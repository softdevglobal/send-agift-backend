package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

// Config holds process settings loaded from .env / environment.
type Config struct {
	AppPort         string
	DBHost          string
	DBPort          string
	DBUser          string
	DBPassword      string
	DBName          string
	DBSSLMode       string
	JWTSecret       string
	JWTExpiry       time.Duration
	BootstrapSecret string
	AWSRegion       string
	AWSAccessKeyID  string
	AWSSecretKey    string
	S3Bucket        string
}

// Load reads .env (if present) and required environment variables.
func Load() (*Config, error) {
	_ = godotenv.Load()

	cfg := &Config{
		AppPort:         envOr("APP_PORT", "8080"),
		DBHost:          envOr("DB_HOST", "localhost"),
		DBPort:          envOr("DB_PORT", "5432"),
		DBUser:          os.Getenv("DB_USER"),
		DBPassword:      os.Getenv("DB_PASSWORD"),
		DBName:          os.Getenv("DB_NAME"),
		DBSSLMode:       envOr("DB_SSLMODE", "disable"),
		JWTSecret:       os.Getenv("JWT_SECRET"),
		JWTExpiry:       minutesOr("JWT_EXPIRY_MINUTES", 24*60),
		BootstrapSecret: os.Getenv("BOOTSTRAP_SECRET"),
		AWSRegion:       envOr("AWS_REGION", "ap-southeast-2"),
		AWSAccessKeyID:  os.Getenv("AWS_ACCESS_KEY_ID"),
		AWSSecretKey:    os.Getenv("AWS_SECRET_ACCESS_KEY"),
		S3Bucket:        os.Getenv("S3_BUCKET"),
	}

	// if the JWT secret is not set, return an error
	if cfg.JWTSecret == "" {
		return nil, fmt.Errorf("JWT_SECRET is required")
	}
	// if the database user or name is not set, return an error
	if cfg.DBUser == "" || cfg.DBName == "" {
		return nil, fmt.Errorf("DB_USER and DB_NAME are required")
	}
	if cfg.S3Bucket == "" || cfg.AWSAccessKeyID == "" || cfg.AWSSecretKey == "" {
		return nil, fmt.Errorf("S3_BUCKET, AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY are required")
	}

	return cfg, nil // return the configuration
}

// DSN builds a Postgres connection string.
// This string is used to connect to the database 
func (c *Config) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		c.DBHost, c.DBPort, c.DBUser, c.DBPassword, c.DBName, c.DBSSLMode,
	)
}

// This function is used to get the value of an environment variable or return a fallback value
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// This function is used to get the value of an environment variable or return a fallback value
func minutesOr(key string, fallback int) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return time.Duration(fallback) * time.Minute
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return time.Duration(fallback) * time.Minute
	}
	return time.Duration(n) * time.Minute
}
