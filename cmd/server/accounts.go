package main

import (
	"context"

	"github.com/clinicmanager/services/tenant/service"
	userservice "github.com/clinicmanager/services/user/service"
)

// inProcessAccounts lets the tenant service create user accounts by calling
// the auth service directly. It implements tenantservice.UserAccountClient,
// which used to be an HTTP client pointed at the user microservice.
type inProcessAccounts struct {
	auth *userservice.AuthService
}

// AcceptInvite creates the invited staff account and returns the same
// credentials the old user-service HTTP endpoint used to return.
func (c *inProcessAccounts) AcceptInvite(ctx context.Context, token, name, password string) (*service.UserCredentials, error) {
	account, err := c.auth.AcceptInvite(ctx, token, name, password)
	if err != nil {
		return nil, err
	}
	return &service.UserCredentials{
		UserID:       account.User.ID,
		Token:        account.Token,
		RefreshToken: account.RefreshToken,
		Email:        account.User.Email,
	}, nil
}
