package service

// This file holds platform-admin operations: creating clients (tenants) and
// branches, toggling their status, and seeding a branch's default roles and
// permissions. These mirror the seed migration 02_02_tenant_data so a branch
// created at runtime looks exactly like a seeded one.
//
// Authorization lives OUTSIDE this service (in the GraphQL resolvers via
// UserService.RequireSuperAdmin). This service owns the tenants tables and
// must never touch the users tables — same rule as the rest of this package.

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/clinicmanager/services/tenant/db"
	"github.com/clinicmanager/services/tenant/graph/model"
	sharedctx "github.com/clinicmanager/shared/context"
)

// Tenant statuses. "disabled" hides the client from new work without
// deleting rows (assignments, history, and invites stay intact).
const (
	TenantStatusActive   = "active"
	TenantStatusDisabled = "disabled"
)

var validTenantStatuses = map[string]bool{TenantStatusActive: true, TenantStatusDisabled: true}

// slugFormat keeps slugs URL-safe: lowercase letters, numbers, hyphens.
var slugFormat = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// systemRoleSeed is the canonical role list, copied from the 02_02 seed
// migration. New branches get exactly these roles.
type systemRoleSeed struct {
	name        string
	description string
}

var systemRolesSeed = []systemRoleSeed{
	{"branch_admin", "Branch administrator with full access"},
	{"therapist", "Occupational therapist"},
	{"guardian", "Patient guardian/parent"},
	{"assistant_ot", "Assistant occupational therapist"},
	{"front_desk", "Front desk staff"},
	{"hr_manager", "HR manager"},
	{"executive", "Executive leadership"},
}

// permissionSeed is one (resource, action, scope) definition, copied from
// the 02_02 seed migration: 27 base permissions + 15 module-access ones.
type permissionSeed struct {
	resource    string
	action      string
	scope       string
	description string
}

var basePermissionsSeed = []permissionSeed{
	{"patient", "read", "branch", "View patient records"},
	{"patient", "write", "branch", "Create/edit patient records"},
	{"patient", "delete", "branch", "Delete patient records"},
	{"appointment", "read", "own", "View own appointments"},
	{"appointment", "read", "branch", "View branch appointments"},
	{"appointment", "write", "branch", "Create/edit appointments"},
	{"appointment", "delete", "branch", "Delete appointments"},
	{"soap_note", "read", "own", "View own SOAP notes"},
	{"soap_note", "read", "branch", "View branch SOAP notes"},
	{"soap_note", "write", "branch", "Create/edit SOAP notes"},
	{"soap_note", "delete", "branch", "Delete SOAP notes"},
	{"assessment", "read", "own", "View own assessments"},
	{"assessment", "write", "branch", "Create/edit assessments"},
	{"treatment_plan", "read", "own", "View own treatment plans"},
	{"treatment_plan", "write", "branch", "Create/edit treatment plans"},
	{"goal", "read", "own", "View own progress goals"},
	{"goal", "write", "branch", "Create/edit progress goals"},
	{"employee", "read", "branch", "View employee records"},
	{"employee", "write", "tenant", "Manage employee records"},
	{"billing", "read", "branch", "View billing records"},
	{"billing", "write", "tenant", "Manage billing"},
	{"referral", "read", "branch", "View referral records"},
	{"referral", "write", "branch", "Create/manage referrals"},
	{"message", "read", "own", "View own messages"},
	{"message", "write", "own", "Send messages"},
	{"report", "read", "tenant", "View reports"},
	{"config", "write", "tenant", "Manage tenant configuration"},
}

var accessPermissionsSeed = []permissionSeed{
	{"dashboard", "access", "branch", "Open dashboard"},
	{"patients", "access", "branch", "Open patient list"},
	{"patient-detail", "access", "branch", "Open patient detail"},
	{"schedule", "access", "branch", "Open schedule"},
	{"messaging", "access", "branch", "Open messaging"},
	{"billing", "access", "branch", "Open billing"},
	{"referrals", "access", "branch", "Open referrals"},
	{"staff", "access", "branch", "Open staff directory"},
	{"admin", "access", "branch", "Open admin hub"},
	{"admin.branch-profile", "access", "branch", "Manage branch profile"},
	{"admin.branch-address", "access", "branch", "Manage branch address"},
	{"admin.access-management", "access", "branch", "Manage access control"},
	{"admin.user-management", "access", "branch", "Manage users"},
	{"admin.appearance", "access", "branch", "Manage appearance"},
	{"settings", "access", "branch", "Open settings"},
}

// modulePermissionsSeed holds the read/write/delete counterparts (plus the
// access entries missing for nested pages) for every frontend module in
// web/src/lib/permissions.ts MODULES. The access-management UI saves levels
// as resource:action refs (view=access+read, editor=+write, full=+delete),
// so every combination it can send must resolve to a definition here.
var modulePermissionsSeed = []permissionSeed{
	{"dashboard", "read", "branch", "View dashboard"},
	{"patients", "read", "branch", "View patient directory"},
	{"patients", "write", "branch", "Manage patient directory"},
	{"patients", "delete", "branch", "Remove patients"},
	{"patient-detail", "read", "branch", "View patient details"},
	{"patient-detail", "write", "branch", "Edit patient details"},
	{"patient-detail", "delete", "branch", "Remove patient details"},
	{"patient-detail.soap", "access", "branch", "Open SOAP notes"},
	{"patient-detail.soap", "read", "branch", "View SOAP notes"},
	{"patient-detail.soap", "write", "branch", "Edit SOAP notes"},
	{"patient-detail.soap", "delete", "branch", "Remove SOAP notes"},
	{"patient-detail.goals", "access", "branch", "Open goals"},
	{"patient-detail.goals", "read", "branch", "View goals"},
	{"patient-detail.goals", "write", "branch", "Edit goals"},
	{"patient-detail.goals", "delete", "branch", "Remove goals"},
	{"patient-detail.treatment_plan", "access", "branch", "Open treatment plan"},
	{"patient-detail.treatment_plan", "read", "branch", "View treatment plan"},
	{"patient-detail.treatment_plan", "write", "branch", "Edit treatment plan"},
	{"patient-detail.treatment_plan", "delete", "branch", "Remove treatment plan"},
	{"patient-detail.assessments", "access", "branch", "Open assessments"},
	{"patient-detail.assessments", "read", "branch", "View assessments"},
	{"patient-detail.assessments", "write", "branch", "Edit assessments"},
	{"patient-detail.assessments", "delete", "branch", "Remove assessments"},
	{"schedule", "read", "branch", "View schedule"},
	{"schedule", "write", "branch", "Manage schedule"},
	{"schedule", "delete", "branch", "Remove appointments"},
	{"messaging", "read", "branch", "View messages"},
	{"messaging", "write", "branch", "Send messages"},
	{"messaging", "delete", "branch", "Remove messages"},
	{"billing", "delete", "branch", "Remove billing records"},
	{"referrals", "read", "branch", "View referrals"},
	{"referrals", "write", "branch", "Manage referrals"},
	{"referrals", "delete", "branch", "Remove referrals"},
	{"staff", "read", "branch", "View staff directory"},
	{"staff", "write", "branch", "Manage staff directory"},
	{"staff", "delete", "branch", "Remove staff records"},
	{"admin", "read", "branch", "View admin hub"},
	{"admin", "write", "branch", "Manage admin hub"},
	{"admin", "delete", "branch", "Remove admin resources"},
	{"admin.branch-profile", "read", "branch", "View branch profile"},
	{"admin.branch-profile", "write", "branch", "Edit branch profile"},
	{"admin.branch-profile", "delete", "branch", "Remove branch profile"},
	{"admin.branch-address", "read", "branch", "View branch address"},
	{"admin.branch-address", "write", "branch", "Edit branch address"},
	{"admin.branch-address", "delete", "branch", "Remove branch address"},
	{"admin.access-management", "read", "branch", "View access management"},
	{"admin.access-management", "write", "branch", "Edit access management"},
	{"admin.access-management", "delete", "branch", "Remove access roles"},
	{"admin.user-management", "read", "branch", "View user management"},
	{"admin.user-management", "write", "branch", "Edit user management"},
	{"admin.user-management", "delete", "branch", "Remove users"},
	{"admin.appearance", "read", "branch", "View appearance settings"},
	{"admin.appearance", "write", "branch", "Edit appearance settings"},
	{"admin.appearance", "delete", "branch", "Reset appearance settings"},
	{"settings", "read", "branch", "View settings"},
	{"settings", "write", "branch", "Edit settings"},
	{"settings", "delete", "branch", "Reset settings"},
}

// rolePermissionRule decides which permissions a system role gets, mirroring
// the 02_02 seed mapping. Returning ("*", "*") means "every permission".
func rolePermissionRule(roleName string) (resources []string, actions []string, all bool) {
	switch roleName {
	case "branch_admin", "executive":
		return nil, nil, true
	case "therapist":
		return []string{"patient", "appointment", "soap_note", "assessment", "treatment_plan", "goal"}, []string{"read", "write"}, false
	case "guardian":
		return []string{"patient"}, []string{"read"}, false
	case "front_desk":
		return []string{"patient", "appointment", "billing"}, []string{"read", "write"}, false
	case "assistant_ot":
		return []string{"patient", "appointment"}, []string{"read", "write"}, false
	case "hr_manager":
		return []string{"billing", "referral"}, []string{"read", "write"}, false
	default:
		return nil, nil, false
	}
}

// CreateTenant registers a new client. The slug is the client's URL-safe
// handle and must be unique across all tenants.
func (s *TenantService) CreateTenant(ctx context.Context, name, slug string, domain *string) (*db.BunTenant, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("client name is required")
	}
	slug = strings.ToLower(strings.TrimSpace(slug))
	if slug == "" {
		return nil, fmt.Errorf("slug is required")
	}
	if !slugFormat.MatchString(slug) {
		return nil, fmt.Errorf("slug must be lowercase letters, numbers, and hyphens only")
	}
	if existing, err := s.tenantRepo.FindTenantBySlug(ctx, slug); err != nil {
		return nil, err
	} else if existing != nil {
		return nil, fmt.Errorf("slug %q is already taken", slug)
	}
	tenant := &db.BunTenant{
		ID:     uuid.NewString(),
		Name:   name,
		Slug:   slug,
		Domain: domain,
		Status: TenantStatusActive,
	}
	if ct := sharedctx.FromContext(ctx); ct.UserID != "" {
		tenant.CreatedBy = &ct.UserID
		tenant.UpdatedBy = &ct.UserID
	}
	if err := s.tenantRepo.CreateTenant(ctx, tenant); err != nil {
		return nil, err
	}
	return tenant, nil
}

// ListTenants returns all clients, optionally filtered by a case-insensitive
// name/slug search. Tenant counts stay small, so filtering and paging happen
// in memory rather than in SQL.
func (s *TenantService) ListTenants(ctx context.Context, search string, limit, offset int) ([]*db.BunTenant, error) {
	all, err := s.tenantRepo.ListTenants(ctx)
	if err != nil {
		return nil, err
	}
	search = strings.ToLower(strings.TrimSpace(search))
	filtered := all[:0:0]
	for _, t := range all {
		if search != "" &&
			!strings.Contains(strings.ToLower(t.Name), search) &&
			!strings.Contains(strings.ToLower(t.Slug), search) {
			continue
		}
		filtered = append(filtered, t)
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= len(filtered) {
		return []*db.BunTenant{}, nil
	}
	filtered = filtered[offset:]
	if limit > 0 && limit < len(filtered) {
		filtered = filtered[:limit]
	}
	return filtered, nil
}

// SetTenantStatus flips a client between active and disabled. Disabled
// clients keep every row (branches, assignments, history) — nothing is deleted.
func (s *TenantService) SetTenantStatus(ctx context.Context, id, status string) (*db.BunTenant, error) {
	status = strings.ToLower(strings.TrimSpace(status))
	if !validTenantStatuses[status] {
		return nil, fmt.Errorf("status must be %q or %q", TenantStatusActive, TenantStatusDisabled)
	}
	tenant, err := s.tenantRepo.FindTenantByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if tenant == nil {
		return nil, fmt.Errorf("client not found")
	}
	tenant.Status = status
	if ct := sharedctx.FromContext(ctx); ct.UserID != "" {
		tenant.UpdatedBy = &ct.UserID
	}
	if err := s.tenantRepo.UpdateTenant(ctx, id, tenant); err != nil {
		return nil, err
	}
	s.cacheDel(ctx, "tenant:tenant:"+id)
	return tenant, nil
}

// CreateBranch adds a branch to a client and seeds its default roles and
// permissions, so the branch is usable immediately (same contents as the
// 02_02 seed migration produces for seeded branches).
func (s *TenantService) CreateBranch(ctx context.Context, tenantID, name, timezone string, phone *string) (*db.BunBranch, error) {
	tenant, err := s.tenantRepo.FindTenantByID(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if tenant == nil {
		return nil, fmt.Errorf("client not found")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("branch name is required")
	}
	timezone = strings.TrimSpace(timezone)
	if timezone == "" {
		timezone = "UTC"
	}
	branch := &db.BunBranch{
		ID:       uuid.NewString(),
		TenantID: tenantID,
		Name:     name,
		Timezone: timezone,
		Phone:    phone,
		IsActive: true,
	}
	if ct := sharedctx.FromContext(ctx); ct.UserID != "" {
		branch.CreatedBy = &ct.UserID
		branch.UpdatedBy = &ct.UserID
	}
	if err := s.tenantRepo.CreateBranch(ctx, branch); err != nil {
		return nil, err
	}
	if _, err := s.SeedBranchDefaults(ctx, branch.ID); err != nil {
		return nil, err
	}
	return branch, nil
}

// SetBranchActive enables or disables a branch. Disabled branches keep every
// row — staff assignments and history stay intact.
func (s *TenantService) SetBranchActive(ctx context.Context, id string, isActive bool) (*db.BunBranch, error) {
	branch, err := s.tenantRepo.FindBranchByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if branch == nil {
		return nil, fmt.Errorf("branch not found")
	}
	branch.IsActive = isActive
	if err := s.tenantRepo.UpdateBranch(ctx, id, branch); err != nil {
		return nil, err
	}
	s.cacheDel(ctx, "tenant:branch:"+id)
	return branch, nil
}

// ListBranchesByTenant returns every branch of one client.
func (s *TenantService) ListBranchesByTenant(ctx context.Context, tenantID string) ([]*db.BunBranch, error) {
	return s.tenantRepo.FindBranchesByTenant(ctx, tenantID)
}

// RemoveAssignment deletes a branch assignment, fully detaching the account
// from the branch. Prefer this over deactivation when the association itself
// was a mistake (wrong person, wrong branch); the person keeps their login
// and every other assignment untouched.
func (s *TenantService) RemoveAssignment(ctx context.Context, id string) error {
	existing, err := s.tenantRepo.FindAssignmentByID(ctx, id)
	if err != nil {
		return err
	}
	if existing == nil {
		return fmt.Errorf("assignment not found")
	}
	return s.tenantRepo.DeleteAssignment(ctx, id)
}

// AssignUserToBranch puts an existing account straight onto a branch role.
// Unlike InviteUser there is no email round-trip: platform admins onboarding
// a new client already verified the person, so the assignment is immediate.
// The role must belong to the branch (same rule as invites).
func (s *TenantService) AssignUserToBranch(ctx context.Context, userID, branchID, roleID string) (*model.TenantUserAssignment, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, fmt.Errorf("user is required")
	}
	branch, err := s.tenantRepo.FindBranchByID(ctx, branchID)
	if err != nil {
		return nil, err
	}
	if branch == nil {
		return nil, fmt.Errorf("branch not found")
	}
	role, err := s.tenantRepo.FindRoleByID(ctx, roleID)
	if err != nil {
		return nil, err
	}
	if role == nil {
		return nil, fmt.Errorf("role not found")
	}
	if err := checkRoleForBranch(role, branch); err != nil {
		return nil, err
	}
	var assignedBy *string
	if ct := sharedctx.FromContext(ctx); ct.UserID != "" {
		assignedBy = &ct.UserID
	}
	if err := s.tenantRepo.UpsertAssignment(ctx, userID, branchID, branch.TenantID, roleID, assignedBy); err != nil {
		return nil, err
	}
	// Read back the full assignment (with branch/tenant/role names) for the response.
	byUser, err := s.tenantRepo.FindAssignmentsByUserAndTenant(ctx, userID, branch.TenantID)
	if err != nil {
		return nil, err
	}
	for _, a := range byUser {
		if a.Branch.ID == branchID && a.Role.ID == roleID {
			return a, nil
		}
	}
	return nil, fmt.Errorf("assignment not found after write")
}

// ensurePermissionDefinitions creates any missing permission definitions
// for the branch (base + module-access + module-level seeds) and returns
// the branch's full definition list. Idempotent: existing definitions are
// reused, so it is safe to call on any branch, new or old — including right
// before resolving permission refs for a role save.
func (s *TenantService) ensurePermissionDefinitions(ctx context.Context, branch *db.BunBranch) ([]*db.BunTenantPermission, error) {
	var actor *string
	if ct := sharedctx.FromContext(ctx); ct.UserID != "" {
		actor = &ct.UserID
	}
	existing, err := s.tenantRepo.ListPermissions(ctx, branch.ID)
	if err != nil {
		return nil, err
	}
	havePerm := map[string]*db.BunTenantPermission{}
	for _, p := range existing {
		havePerm[p.Resource+"\x00"+p.Action+"\x00"+p.Scope] = p
	}
	ensurePerm := func(seed permissionSeed) (*db.BunTenantPermission, error) {
		key := seed.resource + "\x00" + seed.action + "\x00" + seed.scope
		if p, ok := havePerm[key]; ok {
			return p, nil
		}
		desc := seed.description
		perm := &db.BunTenantPermission{
			ID:          uuid.NewString(),
			Resource:    seed.resource,
			Action:      seed.action,
			Scope:       seed.scope,
			Description: &desc,
			TenantID:    &branch.TenantID,
			BranchID:    &branch.ID,
			CreatedBy:   actor,
			UpdatedBy:   actor,
		}
		if err := s.tenantRepo.CreatePermission(ctx, perm); err != nil {
			return nil, err
		}
		havePerm[key] = perm
		return perm, nil
	}
	allPerms := []*db.BunTenantPermission{}
	for _, seed := range basePermissionsSeed {
		p, err := ensurePerm(seed)
		if err != nil {
			return nil, err
		}
		allPerms = append(allPerms, p)
	}
	for _, seed := range accessPermissionsSeed {
		p, err := ensurePerm(seed)
		if err != nil {
			return nil, err
		}
		allPerms = append(allPerms, p)
	}
	for _, seed := range modulePermissionsSeed {
		p, err := ensurePerm(seed)
		if err != nil {
			return nil, err
		}
		allPerms = append(allPerms, p)
	}
	return allPerms, nil
}

// SeedBranchDefaults makes sure a branch has the 7 system roles, all 99
// permission definitions, and the default role→permission mapping. It only
// fills gaps: roles that already have permissions are left untouched, so
// re-running it never wipes custom access setups. Safe to call on any
// branch, new or old. Returns the branch's system roles afterwards.
func (s *TenantService) SeedBranchDefaults(ctx context.Context, branchID string) ([]*db.BunTenantRole, error) {
	branch, err := s.tenantRepo.FindBranchByID(ctx, branchID)
	if err != nil {
		return nil, err
	}
	if branch == nil {
		return nil, fmt.Errorf("branch not found")
	}

	roles, err := s.tenantRepo.ListRolesByBranch(ctx, branchID)
	if err != nil {
		return nil, err
	}
	roleByName := map[string]*db.BunTenantRole{}
	for _, r := range roles {
		roleByName[r.Name] = r
	}
	var actor *string
	if ct := sharedctx.FromContext(ctx); ct.UserID != "" {
		actor = &ct.UserID
	}

	// 1. Create the 7 system roles when missing.
	for _, seed := range systemRolesSeed {
		if _, ok := roleByName[seed.name]; ok {
			continue
		}
		desc := seed.description
		role := &db.BunTenantRole{
			ID:           uuid.NewString(),
			Name:         seed.name,
			Description:  &desc,
			TenantID:     &branch.TenantID,
			BranchID:     &branchID,
			IsSystemRole: true,
			IsActive:     true,
			CreatedBy:    actor,
			UpdatedBy:    actor,
		}
		if err := s.tenantRepo.CreateRole(ctx, role); err != nil {
			return nil, err
		}
		roleByName[seed.name] = role
	}

	// 2. Create missing permission definitions (idempotent; safe on any branch).
	allPerms, err := s.ensurePermissionDefinitions(ctx, branch)
	if err != nil {
		return nil, err
	}

	// 3. Map default permissions onto roles that have none yet.
	// Roles with existing permissions are skipped to preserve custom setups.
	result := make([]*db.BunTenantRole, 0, len(systemRolesSeed))
	for _, seed := range systemRolesSeed {
		role := roleByName[seed.name]
		result = append(result, role)
		current, err := s.tenantRepo.FindPermissionsByRole(ctx, role.ID)
		if err != nil {
			return nil, err
		}
		if len(current) > 0 {
			continue
		}
		ids := defaultPermissionIDs(seed.name, allPerms)
		if err := s.tenantRepo.ReplaceRolePermissions(ctx, role.ID, ids); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// defaultPermissionIDs picks the permission IDs a system role starts with,
// following the same rules as the 02_02 seed migration.
func defaultPermissionIDs(roleName string, perms []*db.BunTenantPermission) []string {
	resources, actions, all := rolePermissionRule(roleName)
	if all {
		ids := make([]string, 0, len(perms))
		for _, p := range perms {
			ids = append(ids, p.ID)
		}
		return ids
	}
	in := func(list []string, v string) bool {
		for _, s := range list {
			if s == v {
				return true
			}
		}
		return false
	}
	ids := []string{}
	for _, p := range perms {
		if in(resources, p.Resource) && in(actions, p.Action) {
			ids = append(ids, p.ID)
		}
	}
	return ids
}
