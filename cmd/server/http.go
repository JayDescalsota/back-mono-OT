package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"

	"github.com/clinicmanager/graph"
	"github.com/clinicmanager/graph/generated"
	gwratelimit "github.com/clinicmanager/services/gateway/middleware"
	sharedCtx "github.com/clinicmanager/shared/context"
	"github.com/clinicmanager/shared/logger"
	"github.com/clinicmanager/shared/middleware"
)

// newHTTPServer registers every route, then wraps them in middleware.
// Read the wrapping bottom-up: the LAST wrapper listed is the FIRST to run.
//
//	withCORS → recovery → logging → rate limit → auth context → routes
//
// So a request passes CORS preflight, then crash protection, then logging,
// then abuse protection, then (optional) login detection, then your handler.
func newHTTPServer(cfg Config, services Services, redisClient *redis.Client) *http.Server {
	mux := http.NewServeMux()

	// Liveness probe. Load balancers and docker healthchecks hit this.
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	// The whole GraphQL API (all domains). The Tenant wrapper reads the
	// x-tenant-id / x-branch-id headers into the request context so every
	// resolver knows which clinic the caller belongs to.
	resolver := graph.NewResolver(
		services.Auth,
		services.Users,
		services.Tenants,
		services.Patients,
		services.Bookings,
		services.Messaging,
		services.Payments,
	)
	graphqlServer := handler.NewDefaultServer(generated.NewExecutableSchema(generated.Config{Resolvers: resolver}))
	mux.Handle("/graphql", sharedCtx.Tenant(graphqlServer))

	// Auth REST endpoints (no login needed — this is HOW you log in).
	// The Tenant wrapper is included so signup/invite calls can also carry
	// tenant headers when the frontend has them.
	mux.Handle("POST /register", sharedCtx.Tenant(registerHandler(services.Auth)))
	mux.Handle("POST /login", sharedCtx.Tenant(loginHandler(services.Auth)))
	mux.Handle("POST /invite-accept", sharedCtx.Tenant(acceptInviteHandler(services.Auth)))
	mux.Handle("GET /verify", sharedCtx.Tenant(verifyHandler(services.Auth)))
	mux.Handle("POST /change-password", sharedCtx.Tenant(changePasswordHandler(services.Auth)))
	mux.Handle("POST /logout", sharedCtx.Tenant(logoutHandler(services.Auth)))
	mux.Handle("POST /logout-all", sharedCtx.Tenant(logoutAllHandler(services.Auth)))
	mux.Handle("POST /refresh-token", sharedCtx.Tenant(refreshTokenHandler(services.Auth)))
	mux.Handle("POST /forgot-password", sharedCtx.Tenant(forgotPasswordHandler(services.Auth)))
	mux.Handle("POST /reset-password", sharedCtx.Tenant(resetPasswordHandler(services.Auth)))

	// PayMongo webhooks. PayMongo cannot log in, so this route is public but
	// every delivery is signature-verified inside the payment service before
	// anything is applied.
	mux.HandleFunc("POST /webhooks/paymongo", paymongoWebhookHandler(services.Payments))

	// Realtime chat stream. Browsers hold this connection open and receive
	// one event per new message. ?userId=… decides whose messages to send.
	mux.HandleFunc("GET /sse/messages", func(w http.ResponseWriter, r *http.Request) {
		sseHandler(w, r, redisClient)
	})

	var app http.Handler = mux
	app = withAuthContext(app, cfg.JWTSecret)
	app = gwratelimit.NewRateLimiter(100, time.Minute).Limit(app)
	app = middleware.LoggingMiddleware(app)
	app = middleware.RecoveryMiddleware(app)
	app = withCORS(app)

	// Timeouts stop one slow client from holding a connection forever.
	return &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      app,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
}

// runUntilShutdown serves requests, then waits for Ctrl+C (or SIGTERM from
// Docker) and gives in-flight requests a moment to finish before exiting.
func runUntilShutdown(server *http.Server, port string) {
	go func() {
		logger.Info(context.Background(), "monolith listening", "port", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error(context.Background(), "server stopped unexpectedly", "error", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
	<-quit

	logger.Info(context.Background(), "shutting down, finishing in-flight requests")
	if err := server.Shutdown(context.Background()); err != nil {
		logger.Error(context.Background(), "graceful shutdown failed", "error", err)
	}
}

// authClaims is the shape of OUR login tokens: just a user ID plus the
// standard expiry/issued-at fields. Created in the auth service, read here.
type authClaims struct {
	UserID string `json:"userId"`
	jwt.RegisteredClaims
}

// validateToken parses the "Authorization: Bearer <token>" header.
// It returns nil when the header is missing or the token is invalid —
// GraphQL stays open to anonymous callers and each resolver decides whether
// it needs a logged-in user.
func validateToken(r *http.Request, secret string) *authClaims {
	header := r.Header.Get("Authorization")
	tokenString := strings.TrimPrefix(header, "Bearer ")
	if tokenString == header {
		return nil // header missing, or not a Bearer token
	}
	token, err := jwt.ParseWithClaims(tokenString, &authClaims{}, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil
	}
	claims, ok := token.Claims.(*authClaims)
	if !ok || !token.Valid {
		return nil
	}
	return claims
}

// withAuthContext detects a logged-in user (when a valid token is present)
// and stores their ID in the request context, where services read it back
// via context.UserIDFromCtx. No token = anonymous request, which is fine
// for public routes like /login and /verify.
func withAuthContext(next http.Handler, secret string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if claims := validateToken(r, secret); claims != nil && claims.UserID != "" {
			r.Header.Set("x-user-id", claims.UserID)
			r = r.WithContext(sharedCtx.SetUserID(r.Context(), claims.UserID))
		}
		next.ServeHTTP(w, r)
	})
}

// withCORS answers browser preflight (OPTIONS) requests and stamps every
// response with the headers browsers require for cross-origin calls from
// the frontend. The frontend origin is echoed back; credentials are allowed
// because the UI sends the Authorization header.
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		} else {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, x-tenant-id, x-branch-id, x-user-id, x-app-id, apollo-require-preflight, x-apollo-operation-name")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Max-Age", "86400")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}
