package main

import (
	"context"
	"os"
	"strings"

	"github.com/clinicmanager/shared/logger"
	"github.com/clinicmanager/shared/setting"
)

// Config holds every environment variable the monolith needs.
// Values come from the environment (or the .env file in local Docker setups).
type Config struct {
	// Port the single HTTP server listens on (e.g. "4000").
	Port string
	// DBURL is the ONE shared Postgres database. Table prefixes
	// (users_*, tenant_*, booking_*, …) keep domains from colliding.
	DBURL string
	// JWTSecret signs access/refresh tokens. Must be identical for
	// issuing (auth service) and verifying (middleware) — that is why
	// it lives in one shared config.
	JWTSecret string
	// BaseURL is the public backend URL used inside emails (e.g. "http://localhost").
	BaseURL string
	// FrontendURL is linked from invite emails (defaults to local dev UI).
	FrontendURL string
	// RedisAddr is the shared Redis used for sessions, cache, and SSE streams.
	RedisAddr string
	// RedisPassword is usually empty for local development.
	RedisPassword string
	// MessageEncryptionKey encrypts chat message keys end-to-end.
	MessageEncryptionKey string

	// Payment gateway (PayMongo). Optional: the app boots without it, but
	// top-ups fail until PAYMONGO_SECRET_KEY is set.
	PayMongoSecretKey     string
	PayMongoWebhookSecret string
	// PayMongoPaymentMethods is a CSV of preferred method types
	// (e.g. "card,gcash,qrph"); intersected with account capabilities.
	PayMongoPaymentMethods []string
	// PayMongoSuccessURL / PayMongoCancelURL override the checkout return
	// URLs (useful with a tunnel in dev); default to FRONTEND_URL + paths.
	PayMongoSuccessURL string
	PayMongoCancelURL  string
}

// requiredKeys lists env vars with no safe default. If any is missing we
// stop immediately with a clear error instead of failing mysteriously later.
var requiredKeys = []string{
	"PORT",
	"DB_URL",
	"JWT_SECRET",
	"BASE_URL",
	"REDIS_ADDR",
	"MESSAGE_ENCRYPTION_KEY",
}

// mustLoadConfig reads the environment and stops the process when a
// required variable is missing. Optional vars fall back to dev defaults.
func mustLoadConfig() Config {
	env, missing := setting.LoadAndValidateEnv(requiredKeys)
	if missing != nil {
		logger.Error(context.Background(), "missing required environment variables", "missing", missing)
		os.Exit(1)
	}

	frontendURL := os.Getenv("FRONTEND_URL")
	if frontendURL == "" {
		frontendURL = "http://localhost:4200"
	}

	return Config{
		Port:                   env["PORT"],
		DBURL:                  env["DB_URL"],
		JWTSecret:              env["JWT_SECRET"],
		BaseURL:                strings.TrimRight(env["BASE_URL"], "/"),
		FrontendURL:            frontendURL,
		RedisAddr:              env["REDIS_ADDR"],
		RedisPassword:          env["REDIS_PASSWORD"],
		MessageEncryptionKey:   env["MESSAGE_ENCRYPTION_KEY"],
		PayMongoSecretKey:      os.Getenv("PAYMONGO_SECRET_KEY"),
		PayMongoWebhookSecret:  os.Getenv("PAYMONGO_WEBHOOK_SECRET"),
		PayMongoPaymentMethods: splitCSV(os.Getenv("PAYMONGO_PAYMENT_METHODS")),
		PayMongoSuccessURL:     os.Getenv("PAYMONGO_SUCCESS_URL"),
		PayMongoCancelURL:      os.Getenv("PAYMONGO_CANCEL_URL"),
	}
}

// splitCSV parses a comma-separated env value into trimmed non-empty items.
func splitCSV(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}
