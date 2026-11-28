// Package main is the monolith entrypoint.
//
// The whole backend runs as ONE process on ONE port:
//   - POST /graphql        every domain (users, tenants, patients, booking, messaging)
//   - POST /register …     auth REST endpoints (no token needed)
//   - GET  /sse/messages   realtime chat stream
//   - GET  /health         "OK" when the process is alive
//
// Start here and read top to bottom. Each step is one function call, and each
// function lives in the file named next to it.
package main

import (
	// Embed the IANA timezone database so branch hours can be read in the
	// branch's local time even on a minimal alpine container (no tzdata pkg).
	_ "time/tzdata"
)

func main() {
	// 1. Read environment variables (PORT, DB_URL, JWT_SECRET, …).
	cfg := mustLoadConfig()

	// 2. Open the shared Postgres pool and the shared Redis client.
	//    Every domain uses these same two connections.
	db := mustConnectPostgres(cfg)
	defer db.Close()
	redisClient := mustConnectRedis(cfg)
	defer redisClient.Close()

	// 3. Build repositories (database queries only, one per domain).
	repos := newRepositories(db)

	// 4. Build services (business logic) on top of the repositories.
	services := newServices(cfg, repos, redisClient)

	// 5. Connect the user <-> tenant invite handshake directly in code.
	//    The old microservices did this over HTTP; here it is a function call.
	connectInviteFlow(cfg, services)

	// 6. Register routes, then wrap them in middleware (auth, limits, logs, CORS).
	httpServer := newHTTPServer(cfg, services, redisClient)

	// 7. Serve until Ctrl+C / SIGTERM, then shut down gracefully.
	runUntilShutdown(httpServer, cfg.Port)
}
