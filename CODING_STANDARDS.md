# Backend Coding Standards (Go + gqlgen + Bun)

The backend is a **monolith**: one Go process, one port (`:4000`), one Postgres
database, one Redis. Every domain (users, tenants, patients, booking,
messaging) lives in the same binary and talks to the others through plain
function calls — never HTTP.

**New here? Read in this order:**
1. `cmd/server/main.go` — the 7-step startup story (config → DB → repos → services → routes → serve).
2. `cmd/server/wiring.go` — how repositories and services are connected.
3. `cmd/server/http.go` — routes and middleware (read the order comment).
4. `graph/schema.graphqls` — the whole API in one file.
5. `graph/schema.resolvers.go` + `graph/helpers.go` — thin resolvers calling services.
6. `services/<domain>/service/` — the actual business logic.

Every domain follows a strict **Resolver → Service → Repository** three-layer separation. Each layer has one responsibility and depends only on the layer below it.

```
┌──────────────────────────┐
│      graph/*.resolvers   │  ← Thinnest possible — auth check, call service, return
├──────────────────────────┤
│  services/<domain>/      │  ← Business logic, validation, orchestration
│  service/                │
├──────────────────────────┤
│  services/<domain>/      │  ← DB queries only (Bun), struct-injected *bun.DB
│  repository/             │
└──────────────────────────┘
```

**Rules:**

- Resolver NEVER calls repository directly.
- Service NEVER sees `*bun.DB` — only repository interfaces.
- Repository NEVER contains business logic, only SQL/queries.

---

## 1. Directory Layout

```
back/
├── cmd/server/                      # Monolith entrypoint — start reading here
│   ├── main.go                      # 7-step startup: config → DB → repos → services → routes → serve
│   ├── config.go                    # Config struct + env loading (one place for all env vars)
│   ├── infra.go                     # Postgres + Redis connections (one shared pool each)
│   ├── wiring.go                    # Repositories + Services structs, invite handshake wiring
│   ├── http.go                      # Routes, middleware (auth/CORS/limits/logs), graceful shutdown
│   ├── handlers.go                  # Auth REST handlers (thin: decode → service call → respond)
│   ├── sse.go                       # GET /sse/messages realtime chat stream
│   ├── accounts.go                  # In-process user↔tenant invite adapter
│   └── mailer.go                    # Shared SMTP mailer
│
├── graph/                           # The ONE unified GraphQL API
│   ├── schema.graphqls              # All domains, one schema (no federation)
│   ├── resolver.go                  # Resolver holding every domain service
│   ├── schema.resolvers.go          # Thin resolvers (generated stubs, filled in)
│   └── helpers.go                   # Shared resolver helpers (kept OUT of the generated file!)
│
├── services/<domain>/               # One folder per domain (user, tenant, patient, booking, messaging)
│   ├── db/                          # Bun table structs (Postgres tables live here)
│   ├── repository/                  # SQL queries only, *bun.DB injected in struct
│   ├── service/                     # Business logic, depends on repository interfaces
│   └── graph/model/                 # Generated gqlgen models reused by repo/service layers
│
├── migrations/                      # Single ordered set: 01_01_user_init … 05_01_messaging_init
│
└── shared/                          # Code every domain may use
    ├── auth/auth.go                 # JWT, bcrypt, token utilities
    ├── response/response.go         # SuccessResponse and Error structures
    ├── httpx/                       # Reusable REST abstractions
    │   ├── decoder.go               # Generic JSON decoder
    │   ├── encoder.go               # JSON response writers (OK, Created)
    │   └── errors.go                # HTTP Status mapping and 5xx logging
    ├── logger/logger.go             # slog wrapper (Text for Dev, JSON for Prod)
    ├── db/pool.go                   # Bun DB connection helper
    └── middleware/
        ├── logging.go               # Global HTTP request logging middleware
        └── context.go               # Tenant/branch context types
```

> ⚠️ Keep helpers OUT of `graph/schema.resolvers.go`: gqlgen rewrites that file
> on every `generate` and drops anything that is not a resolver method.
> Shared code belongs in `graph/helpers.go`, which gqlgen never touches.

---

## 2. Three-Layer Rules

### Resolver (`graph/schema.resolvers.go`)

- 2-4 lines per method.
- Extracts claims/auth from context.
- Calls ONE service method.
- NEVER does validation, NEVER calls repository.

```go
// ✅ Correct
func (r *mutationResolver) Login(ctx context.Context, input model.LoginInput) (*model.AuthPayload, error) {
    return r.AuthService.Login(ctx, input.Email, input.Password)
}
```

### Service (`service/*.go`)

- All business logic, validation, and orchestration.
- Receives repository interface via constructor injection.
- NEVER imports `*bun.DB` or any database driver.
- Returns domain models or error.

```go
// ✅ Correct
type AuthService struct {
    userRepo  UserRepository   // ← interface, not concrete type
    jwtSecret string
}

func (s *AuthService) Login(ctx context.Context, email, password string) (*model.AuthPayload, error) {
    // validation
    // logic
    // call repository
}
```

### Repository (`repository/*.go`)

- Struct with injected `*bun.DB` — never passed as parameter.
- One file per aggregate root (not per table).
- Methods: `FindByID`, `FindByEmail`, `Create`, `Update`, `Delete`.
- Bun models stay inside repository — never exported.
- Always check for `sql.ErrNoRows` to distinguish "not found" from DB errors.

```go
// ✅ Correct
func (r *UserRepo) FindByID(ctx context.Context, id string) (*BunUser, error) {
    user := new(BunUser)
    err := r.db.NewSelect().Model(user).Where("id = ?", id).Scan(ctx)
    if err != nil {
        if err == sql.ErrNoRows {
            return nil, nil
        }
        return nil, err
    }
    return user, nil
}
```

---

## 3. Injection Chain

```
cmd/server/main.go
  │
  ├── db := mustConnectPostgres(cfg)          # infra.go — one shared pool
  │
  ├── repos    := newRepositories(db)         # wiring.go — DB only enters here
  │
  ├── services := newServices(cfg, repos, redis)  # wiring.go — logic on top of repos
  │
  └── graph.NewResolver(services.Auth, services.Users, …)
         │
         └── Injected into gqlgen Config → Resolvers
```

Dependencies flow down: `main.go` wires everything, injects `*bun.DB` only into repositories, repositories into services, services into the single resolver.

---

## 4. Cross-Domain Communication

Domains share one database and one process. Table prefixes (`users_*`,
`tenant_*`, `booking_*`, …) keep rows apart; plain function calls connect logic:

- **auth-svc** owns `users` table (credentials, auth operations)
- **tenant-svc** owns invites/assignments, asks auth to create accounts via `UserAccounts`
- **auth-svc** verifies invite tokens via `InviteEmailLookup` back into tenant

Both directions are wired in `connectInviteFlow` (`cmd/server/wiring.go`) —
no URLs, no HTTP, nothing to misconfigure. If you add a new cross-domain
dependency, add it there (or inject the needed service directly) instead of
inventing a network call.

JWT tokens contain only `userId`. Tenant/branch context travels as HTTP
headers (`x-tenant-id`, `x-branch-id`) and is read into the request context
by `shared/context.Tenant` (see `cmd/server/http.go`).

---

## 5. Repository Pattern

### 5a. Repository struct

```go
// repository/user_repo.go
package repository

type BunUser struct {
    bun.BaseModel `bun:"table:users"`
    ID            string     `bun:"id,pk"`
    Email         string     `bun:"email,notnull,unique"`
    PasswordHash  string     `bun:"password_hash,notnull"`
    IsActive      bool       `bun:"is_active,default:true"`
    CreatedAt     time.Time  `bun:"created_at"`
    UpdatedAt     time.Time  `bun:"updated_at"`
}

type UserRepo struct {
    db *bun.DB
}

func NewUserRepo(db *bun.DB) *UserRepo {
    return &UserRepo{db: db}
}
```

### 5b. Error handling — check sql.ErrNoRows

```go
func (r *UserRepo) FindByID(ctx context.Context, id string) (*BunUser, error) {
    user := new(BunUser)
    err := r.db.NewSelect().Model(user).Where("id = ?", id).Scan(ctx)
    if err != nil {
        if err == sql.ErrNoRows {
            return nil, nil
        }
        return nil, err
    }
    return user, nil
}
```

### 5c. Repository test (sqlmock)

```go
// repository/user_repo_test.go
package repository_test

func newMockRepo(t *testing.T) (*repository.UserRepo, sqlmock.Sqlmock) {
    t.Helper()
    db, mock, err := sqlmock.New()
    repo := repository.NewUserRepo(bunDB)
    return repo, mock
}
```

---

## 6. Repository Interface

Services depend on an interface, not the concrete `*bun.DB`-holding struct:

```go
// service/auth_service.go
type UserRepository interface {
    FindByID(ctx context.Context, id string) (*repository.BunUser, error)
    FindByEmail(ctx context.Context, email string) (*repository.BunUser, error)
    Create(ctx context.Context, email, password, name string) (*repository.BunUser, error)
    UpdateLastLogin(ctx context.Context, userID string) error
}
```

---

## 7. Current User Resolution

User-svc resolves the current user via a `CurrentUserFn` function injected at startup:

```go
// service/user_service.go
type CurrentUserFn func(ctx context.Context) string

type UserService struct {
    userRepo    UserRepository
    currentUser CurrentUserFn
}
```

The middleware extracts `userId` from the JWT and puts it in context. `CurrentUserFn` reads it back. This avoids importing middleware packages into the service layer.

---

## 8. Testing

**Test each layer independently:**

| Layer          | How                                                 | What to cover                                         |
| -------------- | --------------------------------------------------- | ----------------------------------------------------- |
| **Repository** | sqlmock (mock `*sql.DB`)                            | Every query path: found, not found, error, edge cases |
| **Service**    | Mock repository (hand-written interface or structs) | Validation, error mapping, happy path                 |
| **Resolver**   | NOT tested (thin wrappers, 2-4 lines)               | Covered by integration tests later                    |

**Coverage target:** 90%+ for `repository/` package only (enforced in CI).

```sh
go test ./... -count=1

go test ./... -coverprofile=coverage.out -covermode=atomic -count=1
go tool cover -func=coverage.out
```

---

## 9. Adding a New Domain

1. Create `services/<domain>/` with `db/`, `repository/`, `service/` (same shape as the others).
2. Add its tables to `graph/schema.graphqls` — types, queries, mutations.
3. Add its migration as the next numbered file in `migrations/`.
4. Write `repository/<entity>_repo.go` — Bun struct methods.
5. Write `repository/<entity>_repo_test.go` — sqlmock tests (90%+ coverage).
6. Define repository interface in `service/<entity>_service.go`.
7. Write `service/<entity>_service.go` — business logic calling repository via interface.
8. Write `service/<entity>_service_test.go` — mock repo, test all validation paths.
9. Run `go run github.com/99designs/gqlgen generate` — stubs appear in `graph/schema.resolvers.go`.
10. Fill in the stubs — thin wrappers calling your service.
11. Update `cmd/server/wiring.go` — add repo + service to the structs.
12. Update `graph/resolver.go` if the resolver needs the new service.
13. Run `go test ./...` — verify all tests pass.

---

## 10. REST Architecture & Session Security

For the REST auth endpoints (served by the monolith, logic in the user service), follow these strict architectural and security conventions:

### 10a. Request / Response Abstraction (`httpx`)

- Handlers should not contain low-level JSON parsing, writing, or header configuration.
- Use `httpx.Decode[T](r)` to parse request payloads into defined DTO structs.
- Use `httpx.OK`, `httpx.Created`, or `httpx.Error` to send formatted JSON envelopes.
- Register endpoints with `mux.Handle` or `mux.HandleFunc` depending on whether they are wrapped by middleware.

### 10b. Session-Backed Token Management

- **Stateless Tokens with Stateful Revocation**: The Access Token is a JWT and contains a `sessionId` claim.
- **Active Session Check**: Routes protected by `middleware.AuthMiddleware` query the database via a callback to confirm the `sessionId` is not revoked (`revoked = false`). This provides instantaneous token revocation on logout.
- **Refresh Token Rotation (RTR)**: Every call to `/refresh-token` generates a new refresh token and revokes the old database session, returning a new rotated token. This mitigates replay attacks if a refresh token is leaked.
- **Separate TTLs**:
  - `accessTokenTTL` (e.g. 15 minutes) controls short-lived client tokens.
  - `refreshTokenTTL` (e.g. 7 days) defines the session's overall database lifetime.

---

## 11. Summary Checklist

- [ ] Three layers: Resolver/Handler → Service → Repository
- [ ] Resolver/Handler is thin (delegates logic to service, returns response via helpers)
- [ ] Service has all validation and business logic
- [ ] Repository uses struct methods — not free functions
- [ ] `*bun.DB` injected into repository struct, never passed as parameter
- [ ] Repository checks `sql.ErrNoRows` for not-found cases
- [ ] Handler never imports `*bun.DB` or `repository/` directly
- [ ] Service never imports `*bun.DB` — depends on repository interface
- [ ] Service tested with mock repository (all validation paths)
- [ ] Repository tested with sqlmock (every query path)
- [ ] Repository coverage 90%+
- [ ] `main.go` wires the full chain: config → DB/Redis → repos → services → routes
- [ ] New env vars go in the `Config` struct (`cmd/server/config.go`), never scattered `os.Getenv` calls
- [ ] JWT auth via `withAuthContext` in `cmd/server/http.go`
- [ ] User context resolved via `CurrentUserFn`, not direct middleware import
- [ ] REST API endpoints utilize `shared/httpx` for JSON decode/encode and mapping
- [ ] Session-backed route verification handles active revocation checks inside middleware callback
- [ ] Refresh token refresh endpoints rotate refresh keys (RTR) on every access token issue
