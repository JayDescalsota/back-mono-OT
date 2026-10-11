package graph

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/clinicmanager/graph/model"
	usermodel "github.com/clinicmanager/services/user/graph/model"
)

// toSvc converts a monolith graph model input into the sibling service
// graph model input. Shapes are generated from identical schema fragments,
// so a JSON round-trip is exact.
func toSvc[To any](in any) (To, error) {
	var out To
	raw, err := json.Marshal(in)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, err
	}
	return out, nil
}

func mustToSvc[To any](in any) To {
	out, err := toSvc[To](in)
	if err != nil {
		panic(err)
	}
	return out
}

// fromSvc converts a service graph model output into the monolith model.
func fromSvc[To any](in any) (To, error) {
	var out To
	raw, err := json.Marshal(in)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, err
	}
	return out, nil
}

// enrichUser fills assignments + full profile address for a monolith User
// converted from the user service model.
func (r *Resolver) enrichUser(ctx context.Context, u *model.User) (*model.User, error) {
	if u == nil {
		return nil, nil
	}
	aa, err := r.TenantService.GetAssignmentsByUser(ctx, u.ID)
	if err == nil && aa != nil {
		conv, err := fromSvc[[]*model.TenantUserAssignment](aa)
		if err == nil {
			u.Assignments = conv
		}
	}
	if u.Assignments == nil {
		u.Assignments = []*model.TenantUserAssignment{}
	}
	if u.Profile != nil && u.Profile.Address != nil && u.Profile.Address.ID != "" {
		if full, err := r.TenantService.GetAddressByID(ctx, u.Profile.Address.ID); err == nil && full != nil {
			u.Profile.Address = full
		}
	}
	return u, nil
}

func (r *Resolver) userFromSvc(ctx context.Context, svcUser *usermodel.User) (*model.User, error) {
	if svcUser == nil {
		return nil, nil
	}
	u, err := fromSvc[*model.User](svcUser)
	if err != nil {
		return nil, err
	}
	return r.enrichUser(ctx, u)
}

func (r *Resolver) practitionerUser(ctx context.Context, id *string) (*model.User, error) {
	if id == nil || *id == "" {
		return nil, nil
	}
	su, err := r.UserService.FindUserByID(ctx, *id)
	if err != nil || su == nil {
		// Fall back to an ID-only stub (federation-era behavior) so
		// appointment queries keep working when the account is gone.
		return &model.User{ID: *id, Assignments: []*model.TenantUserAssignment{}}, nil
	}
	return r.userFromSvc(ctx, su)
}

// requirePlatformAdmin gates platform-admin operations on the super_admin
// app role (checked against the database, never the JWT). Call it as the
// first line of every platform-only resolver.
func (r *Resolver) requirePlatformAdmin(ctx context.Context) error {
	return r.UserService.RequireSuperAdmin(ctx)
}

// matchUserSearch reports whether a user matches a free-text search against
// their email or profile name (case-insensitive, partial).
func matchUserSearch(u *model.User, search string) bool {
	needle := strings.ToLower(strings.TrimSpace(search))
	haystacks := []string{strings.ToLower(u.Email)}
	if u.Profile != nil {
		fullName := u.Profile.FirstName
		if u.Profile.LastName != nil {
			fullName = strings.TrimSpace(fullName + " " + *u.Profile.LastName)
		}
		haystacks = append(haystacks, strings.ToLower(u.Profile.FirstName), strings.ToLower(fullName))
	}
	for _, h := range haystacks {
		if h != "" && strings.Contains(h, needle) {
			return true
		}
	}
	return false
}

// hasAppRoleName reports whether a user holds an app role by name.
func hasAppRoleName(u *model.User, roleName string) bool {
	for _, role := range u.AppRoles {
		if role.Name == roleName {
			return true
		}
	}
	return false
}
