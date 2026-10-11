package service

// This file holds platform-admin user management: creating accounts with a
// chosen app role, granting/revoking app roles, and the super-admin gate
// that protects all of it.
//
// Two rules keep this safe:
//  1. Every method here first calls RequireSuperAdmin — the CALLER must
//     already be a super_admin (checked against the database, never the JWT).
//  2. The last super_admin account can never be demoted. Locking every admin
//     out would brick client management, so RemoveUserAppRole refuses it.

import (
	"context"
	"fmt"
	"strings"

	"github.com/clinicmanager/services/user/graph/model"
	"github.com/clinicmanager/services/user/models"
	"github.com/clinicmanager/shared/response"
)

// SuperAdminRole is the app role allowed into the platform-admin surface.
const SuperAdminRole = "super_admin"

// RequireSuperAdmin stops callers that are not logged in as a super_admin.
// Use it as the first line of every platform-only operation.
func (s *UserService) RequireSuperAdmin(ctx context.Context) error {
	callerID := s.currentUser(ctx)
	if callerID == "" {
		return response.Unauthorized("not authenticated")
	}
	ok, err := s.userRepo.HasAppRole(ctx, callerID, SuperAdminRole)
	if err != nil {
		return err
	}
	if !ok {
		return response.Forbidden("platform admin access required")
	}
	return nil
}

// appRoleByName finds an app role by name (e.g. "therapist" is NOT one —
// app roles are platform-wide: super_admin, app_admin, support, user).
func (s *UserService) appRoleByName(ctx context.Context, name string) (*models.AppRole, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return nil, response.Validation("app role is required")
	}
	roles, err := s.userRepo.FindAllAppRoles(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range roles {
		if r.Name == name {
			return r, nil
		}
	}
	return nil, response.Validation(fmt.Sprintf("unknown app role %q", name))
}

// hasAppRole reports whether a user currently holds an app role.
func (s *UserService) hasAppRole(ctx context.Context, userID, roleName string) (bool, error) {
	roles, err := s.userRepo.FindUserAppRoles(ctx, userID)
	if err != nil {
		return false, err
	}
	for _, r := range roles {
		if r.Name == roleName {
			return true, nil
		}
	}
	return false, nil
}

// countSuperAdmins counts accounts holding the super_admin role. Platform
// user lists stay small, so a scan is fine and avoids a custom query.
func (s *UserService) countSuperAdmins(ctx context.Context) (int, error) {
	users, err := s.userRepo.FindAllUsers(ctx)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, u := range users {
		ok, err := s.userRepo.HasAppRole(ctx, u.ID, SuperAdminRole)
		if err != nil {
			return 0, err
		}
		if ok {
			count++
		}
	}
	return count, nil
}

// CreatePlatformUser creates a login account with a chosen app role, e.g.
// onboarding a new client's first branch_admin-side super admin or a support
// teammate. Unlike invite acceptance, the account is usable immediately.
func (s *UserService) CreatePlatformUser(ctx context.Context, email, password, appRoleName string) (*model.User, error) {
	if err := s.RequireSuperAdmin(ctx); err != nil {
		return nil, err
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || !strings.Contains(email, "@") {
		return nil, response.Validation("a valid email is required")
	}
	if len(password) < 8 {
		return nil, response.Validation("password must be at least 8 characters")
	}
	role, err := s.appRoleByName(ctx, appRoleName)
	if err != nil {
		return nil, err
	}
	if existing, err := s.userRepo.FindUserByEmail(ctx, email); err != nil {
		return nil, err
	} else if existing != nil {
		return nil, response.Validation("email is already registered")
	}
	user, err := s.userRepo.CreateUser(ctx, email, password)
	if err != nil {
		return nil, err
	}
	if err := s.userRepo.AssignAppRole(ctx, user.ID, role.ID); err != nil {
		return nil, err
	}
	return s.FindUserByID(ctx, user.ID)
}

// SetUserAppRole grants an app role to an account (idempotent — granting a
// role the account already has is a no-op returning the current user).
func (s *UserService) SetUserAppRole(ctx context.Context, userID, appRoleName string) (*model.User, error) {
	if err := s.RequireSuperAdmin(ctx); err != nil {
		return nil, err
	}
	user, err := s.FindUserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	role, err := s.appRoleByName(ctx, appRoleName)
	if err != nil {
		return nil, err
	}
	if has, err := s.hasAppRole(ctx, userID, role.Name); err != nil {
		return nil, err
	} else if has {
		return user, nil
	}
	if err := s.userRepo.AssignAppRole(ctx, userID, role.ID); err != nil {
		return nil, err
	}
	return s.FindUserByID(ctx, userID)
}

// RemoveUserAppRole takes an app role away from an account. Demoting the
// LAST super_admin is refused — the platform would otherwise lock out all
// administrators with no way back in.
func (s *UserService) RemoveUserAppRole(ctx context.Context, userID, appRoleName string) (*model.User, error) {
	if err := s.RequireSuperAdmin(ctx); err != nil {
		return nil, err
	}
	role, err := s.appRoleByName(ctx, appRoleName)
	if err != nil {
		return nil, err
	}
	if role.Name == SuperAdminRole {
		if has, err := s.hasAppRole(ctx, userID, SuperAdminRole); err != nil {
			return nil, err
		} else if has {
			if count, err := s.countSuperAdmins(ctx); err != nil {
				return nil, err
			} else if count <= 1 {
				return nil, response.Validation("cannot remove the last super_admin")
			}
		}
	}
	if err := s.userRepo.RemoveAppRole(ctx, userID, role.ID); err != nil {
		return nil, err
	}
	return s.FindUserByID(ctx, userID)
}

// ListAppRoles returns every platform app role for role pickers in the UI.
func (s *UserService) ListAppRoles(ctx context.Context) ([]*model.AppRole, error) {
	if err := s.RequireSuperAdmin(ctx); err != nil {
		return nil, err
	}
	roles, err := s.userRepo.FindAllAppRoles(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*model.AppRole, 0, len(roles))
	for _, r := range roles {
		desc := r.Description
		out = append(out, &model.AppRole{
			ID:          fmt.Sprintf("%d", r.ID),
			Name:        r.Name,
			Description: &desc,
		})
	}
	return out, nil
}
