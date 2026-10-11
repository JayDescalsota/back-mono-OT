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
		Port:                 env["PORT"],
		DBURL:                env["DB_URL"],
		JWTSecret:            env["JWT_SECRET"],
		BaseURL:              strings.TrimRight(env["BASE_URL"], "/"),
		FrontendURL:          frontendURL,
		RedisAddr:            env["REDIS_ADDR"],
		RedisPassword:        env["REDIS_PASSWORD"],
		MessageEncryptionKey: env["MESSAGE_ENCRYPTION_KEY"],
	}
}
