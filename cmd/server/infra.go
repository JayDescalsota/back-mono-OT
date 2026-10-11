package main

import (
	"context"
	"os"

	"github.com/redis/go-redis/v9"
	"github.com/uptrace/bun"

	sharedCache "github.com/clinicmanager/shared/cache"
	sharedDB "github.com/clinicmanager/shared/db"
	"github.com/clinicmanager/shared/logger"
)

// mustConnectPostgres opens the ONE shared database pool and stops the
// process when the database is unreachable. The pool is shared by every
// repository — domains stay separate through table prefixes, not connections.
func mustConnectPostgres(cfg Config) *bun.DB {
	db, err := sharedDB.NewDB(cfg.DBURL)
	if err != nil {
		logger.Error(context.Background(), "cannot reach postgres, is it running?", "error", err)
		os.Exit(1)
	}
	return db
}

// mustConnectRedis opens the shared Redis client (sessions, query cache,
// and the chat message stream) and stops the process when unreachable.
func mustConnectRedis(cfg Config) *redis.Client {
	client, err := sharedCache.NewRedisClient(cfg.RedisAddr, cfg.RedisPassword)
	if err != nil {
		logger.Error(context.Background(), "cannot reach redis, is it running?", "error", err)
		os.Exit(1)
	}
	return client
}
