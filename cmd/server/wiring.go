package main

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/uptrace/bun"

	bookingrepo "github.com/clinicmanager/services/booking/repository"
	bookingservice "github.com/clinicmanager/services/booking/service"
	messagingrepo "github.com/clinicmanager/services/messaging/repository"
	messagingservice "github.com/clinicmanager/services/messaging/service"
	patientrepo "github.com/clinicmanager/services/patient/repository"
	patientservice "github.com/clinicmanager/services/patient/service"
	tenantrepo "github.com/clinicmanager/services/tenant/repository"
	tenantservice "github.com/clinicmanager/services/tenant/service"
	userrepo "github.com/clinicmanager/services/user/repository"
	userservice "github.com/clinicmanager/services/user/service"
	sharedCtx "github.com/clinicmanager/shared/context"
	sharedDB "github.com/clinicmanager/shared/db"
)

// Token lifetimes for auth sessions. Short access tokens limit damage if
// leaked; long refresh tokens keep users signed in for a week.
const (
	accessTokenTTL  = 15 * time.Minute
	refreshTokenTTL = 7 * 24 * time.Hour
)

// Repositories groups every domain's database access in one place.
// Rule: ONLY repositories may touch the database. Services and GraphQL
// resolvers never see *bun.DB — they go through these.
type Repositories struct {
	Users     *userrepo.UserRepo
	Tenants   *tenantrepo.TenantRepo
	Patients  *patientrepo.PatientRepository
	Bookings  *bookingrepo.BookingRepository
	Messaging *messagingrepo.MessagingRepository
}

// newRepositories builds one repository per domain on the shared DB pool.
// All domains read/write the same Postgres database; table prefixes
// (users_*, tenant_*, booking_*, …) keep their rows apart.
func newRepositories(db *bun.DB) Repositories {
	dbSet := sharedDB.NewDBSet(db)
	return Repositories{
		Users:     userrepo.NewUserRepo(dbSet),
		Tenants:   tenantrepo.NewTenantRepo(dbSet),
		Patients:  patientrepo.NewPatientRepository(dbSet),
		Bookings:  bookingrepo.NewBookingRepository(dbSet),
		Messaging: messagingrepo.NewMessagingRepository(dbSet),
	}
}

// Services groups every domain's business logic in one place.
// Each service only talks to its own repository (plus Redis for cache).
type Services struct {
	Auth      *userservice.AuthService
	Users     *userservice.UserService
	Tenants   *tenantservice.TenantService
	Patients  *patientservice.PatientService
	Bookings  *bookingservice.BookingService
	Messaging *messagingservice.MessagingService
}

// newServices builds the business-logic layer on top of the repositories.
// Constructor arguments are the service's collaborators — nothing else.
func newServices(cfg Config, repos Repositories, redisClient *redis.Client) Services {
	auth := userservice.NewAuthService(repos.Users, cfg.JWTSecret, SMTPMailer{}, cfg.BaseURL, accessTokenTTL, refreshTokenTTL)
	auth.FrontendURL = cfg.FrontendURL
	return Services{
		Auth:      auth,
		Users:     userservice.NewUserService(repos.Users, sharedCtx.UserIDFromCtx, redisClient),
		Tenants:   tenantservice.NewTenantService(repos.Tenants, redisClient),
		Patients:  patientservice.NewPatientService(repos.Patients, redisClient),
		Bookings:  bookingservice.NewBookingService(repos.Bookings, redisClient),
		Messaging: messagingservice.NewMessagingService(repos.Messaging, redisClient, messagingservice.NewEncryptor(cfg.MessageEncryptionKey)),
	}
}

// connectInviteFlow wires the two directions of the staff-invite handshake
// as plain function calls:
//
//	tenant --(who was invited?)--> auth : InviteEmailLookup
//	auth   --(create the account)--> tenant : UserAccounts.AcceptInvite
//
// The old microservices did both hops over HTTP between containers. Because
// everything now lives in one process, they call each other directly — no
// URLs, no network, nothing to misconfigure.
func connectInviteFlow(cfg Config, services Services) {
	services.Tenants.Mailer = SMTPMailer{}
	services.Tenants.FrontendURL = cfg.FrontendURL
	services.Tenants.UserAccounts = &inProcessAccounts{auth: services.Auth}

	services.Auth.InviteEmailLookup = func(ctx context.Context, token string) (string, bool) {
		invite, err := services.Tenants.InviteByToken(ctx, token)
		if err != nil || invite == nil {
			return "", false
		}
		return invite.Email, true
	}
}
