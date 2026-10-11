package graph

import (
	bookingservice "github.com/clinicmanager/services/booking/service"
	messagingservice "github.com/clinicmanager/services/messaging/service"
	patientservice "github.com/clinicmanager/services/patient/service"
	tenantservice "github.com/clinicmanager/services/tenant/service"
	userservice "github.com/clinicmanager/services/user/service"
)

// Resolver holds all domain services for the monolith single executable schema.
type Resolver struct {
	AuthService      *userservice.AuthService
	UserService      *userservice.UserService
	TenantService    *tenantservice.TenantService
	PatientService   *patientservice.PatientService
	BookingService   *bookingservice.BookingService
	MessagingService *messagingservice.MessagingService
}

func NewResolver(
	auth *userservice.AuthService,
	user *userservice.UserService,
	tenant *tenantservice.TenantService,
	patient *patientservice.PatientService,
	booking *bookingservice.BookingService,
	messaging *messagingservice.MessagingService,
) *Resolver {
	return &Resolver{
		AuthService:      auth,
		UserService:      user,
		TenantService:    tenant,
		PatientService:   patient,
		BookingService:   booking,
		MessagingService: messaging,
	}
}
