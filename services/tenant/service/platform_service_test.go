package service_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/clinicmanager/services/tenant/db"
	"github.com/clinicmanager/services/tenant/graph/model"
)

func testTenant() *db.BunTenant {
	return &db.BunTenant{ID: "t1", Name: "Clinic", Slug: "clinic", Status: "active"}
}

func TestCreateTenant(t *testing.T) {
	f := &fakeRepo{}
	svc, _, _ := newSvc(f)

	tenant, err := svc.CreateTenant(context.Background(), "New Clinic", "new-clinic", nil)
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if tenant.Name != "New Clinic" || tenant.Slug != "new-clinic" || tenant.Status != "active" {
		t.Fatalf("tenant fields wrong: %+v", tenant)
	}
	if _, err := uuid.Parse(tenant.ID); err != nil {
		t.Fatalf("tenant ID is not a UUID: %q", tenant.ID)
	}
	if f.createdTenant != tenant {
		t.Fatal("expected tenant to be persisted via CreateTenant")
	}
}

func TestCreateTenantValidation(t *testing.T) {
	svc, _, _ := newSvc(&fakeRepo{})

	if _, err := svc.CreateTenant(context.Background(), "  ", "ok-slug", nil); err == nil {
		t.Fatal("expected error for blank name")
	}
	if _, err := svc.CreateTenant(context.Background(), "Name", "Bad Slug!", nil); err == nil {
		t.Fatal("expected error for non-slug characters")
	}
	upper, err := svc.CreateTenant(context.Background(), "Name", "UPPER", nil)
	if err != nil {
		t.Fatalf("uppercase slug should normalize, not fail: %v", err)
	}
	if upper.Slug != "upper" {
		t.Fatalf("slug not normalized to lowercase: %q", upper.Slug)
	}

	f := &fakeRepo{tenantBySlug: testTenant()}
	svc, _, _ = newSvc(f)
	if _, err := svc.CreateTenant(context.Background(), "Other", "clinic", nil); err == nil {
		t.Fatal("expected error for duplicate slug")
	}
}

func TestSetTenantStatus(t *testing.T) {
	f := &fakeRepo{tenantByID: testTenant()}
	svc, _, _ := newSvc(f)

	tenant, err := svc.SetTenantStatus(context.Background(), "t1", "disabled")
	if err != nil {
		t.Fatalf("SetTenantStatus: %v", err)
	}
	if tenant.Status != "disabled" {
		t.Fatalf("status not flipped: %+v", tenant)
	}
	if _, err := svc.SetTenantStatus(context.Background(), "t1", "archived"); err == nil {
		t.Fatal("expected error for unknown status")
	}

	f.tenantByID = nil
	if _, err := svc.SetTenantStatus(context.Background(), "missing", "active"); err == nil {
		t.Fatal("expected error for unknown tenant")
	}
}

func TestListTenantsSearchAndPaging(t *testing.T) {
	all := []*db.BunTenant{
		{ID: "t1", Name: "Alpha Clinic", Slug: "alpha"},
		{ID: "t2", Name: "Beta Clinic", Slug: "beta"},
		{ID: "t3", Name: "Gamma Care", Slug: "gamma"},
	}
	f := &fakeRepo{tenants: all}
	svc, _, _ := newSvc(f)

	got, err := svc.ListTenants(context.Background(), "clinic", 10, 0)
	if err != nil || len(got) != 2 {
		t.Fatalf("search expected 2, got %d (%v)", len(got), err)
	}
	got, err = svc.ListTenants(context.Background(), "", 1, 1)
	if err != nil || len(got) != 1 || got[0].ID != "t2" {
		t.Fatalf("paging broken: %+v %v", got, err)
	}
	got, err = svc.ListTenants(context.Background(), "", 10, 99)
	if err != nil || len(got) != 0 {
		t.Fatalf("offset past end must be empty: %+v %v", got, err)
	}
}

func TestCreateBranchSeedsDefaults(t *testing.T) {
	f := &fakeRepo{tenantByID: testTenant()}
	svc, _, _ := newSvc(f)

	branch, err := svc.CreateBranch(context.Background(), "t1", "New Branch", "Asia/Manila", nil)
	if err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if f.createdBranch != branch {
		t.Fatal("expected branch to be persisted via CreateBranch")
	}
	if branch.TenantID != "t1" || branch.Timezone != "Asia/Manila" || !branch.IsActive {
		t.Fatalf("branch fields wrong: %+v", branch)
	}
	// Seeding: 6 missing roles created (therapist stub already exists in fake),
	// 103 permission definitions (27 base + 15 module-access + 61 module-level),
	// and the empty therapist role gets mapped.
	if f.createdRole == nil {
		t.Fatal("expected missing system roles to be created")
	}
	if len(f.createdPerms) != 103 {
		t.Fatalf("expected 103 permission definitions, got %d", len(f.createdPerms))
	}
	if len(f.replacedPerms) == 0 {
		t.Fatal("expected default mapping for the permission-less therapist role")
	}
}

func TestCreateBranchValidation(t *testing.T) {
	f := &fakeRepo{} // tenantByID nil
	svc, _, _ := newSvc(f)
	if _, err := svc.CreateBranch(context.Background(), "missing", "B", "", nil); err == nil {
		t.Fatal("expected error for unknown tenant")
	}

	f.tenantByID = testTenant()
	if _, err := svc.CreateBranch(context.Background(), "t1", "  ", "", nil); err == nil {
		t.Fatal("expected error for blank branch name")
	}
}

func TestSeedBranchDefaultsSkipsMappedRoles(t *testing.T) {
	perms := []*db.BunTenantPermission{{ID: "p1", Resource: "patient", Action: "read"}}
	f := &fakeRepo{permsByRole: map[string][]*db.BunTenantPermission{"r1": perms}}
	svc, _, _ := newSvc(f)

	roles, err := svc.SeedBranchDefaults(context.Background(), "b1")
	if err != nil {
		t.Fatalf("SeedBranchDefaults: %v", err)
	}
	if len(roles) != 7 {
		t.Fatalf("expected all 7 system roles back, got %d", len(roles))
	}
	// Therapist already mapped in the fake: must not be re-mapped.
	for _, id := range f.replacedPerms {
		if id == "p1" {
			t.Fatal("must not remap a role that already has permissions")
		}
	}
	// But the other six roles were created fresh with empty perms → mapped.
	if len(f.replacedPerms) == 0 {
		t.Fatal("expected mapping for freshly created roles")
	}
}

func TestSeedBranchDefaultsBranchNotFound(t *testing.T) {
	f := &fakeRepo{findBranch: func(ctx context.Context, id string) (*db.BunBranch, error) {
		return nil, nil
	}}
	svc, _, _ := newSvc(f)
	if _, err := svc.SeedBranchDefaults(context.Background(), "nope"); err == nil {
		t.Fatal("expected error for unknown branch")
	}
}

func TestSetBranchActive(t *testing.T) {
	f := &fakeRepo{}
	svc, _, _ := newSvc(f)

	branch, err := svc.SetBranchActive(context.Background(), "b1", false)
	if err != nil {
		t.Fatalf("SetBranchActive: %v", err)
	}
	if branch.IsActive {
		t.Fatal("branch should be disabled")
	}
	if !strings.Contains(branch.ID, "b1") {
		t.Fatalf("wrong branch returned: %+v", branch)
	}
}

func TestAssignUserToBranch(t *testing.T) {
	f := &fakeRepo{byUserTenant: []*model.TenantUserAssignment{testAssignment()}}
	svc, _, _ := newSvc(f)

	got, err := svc.AssignUserToBranch(context.Background(), "u1", "b1", "r1")
	if err != nil {
		t.Fatalf("AssignUserToBranch: %v", err)
	}
	// Fake upsert records the write; fake read-back finds testAssignment (b1/r1).
	if f.upserted == nil || f.upserted.userID != "u1" || f.upserted.branchID != "b1" ||
		f.upserted.tenantID != "t1" || f.upserted.roleID != "r1" {
		t.Fatalf("assignment not written: %+v", f.upserted)
	}
	if got == nil || got.Branch.ID != "b1" {
		t.Fatalf("assignment not read back: %+v", got)
	}
}

func TestAssignUserToBranchGuards(t *testing.T) {
	svc, _, _ := newSvc(&fakeRepo{})
	if _, err := svc.AssignUserToBranch(context.Background(), "  ", "b1", "r1"); err == nil {
		t.Fatal("expected error for blank user")
	}

	f := &fakeRepo{findBranch: func(ctx context.Context, id string) (*db.BunBranch, error) {
		return nil, nil
	}}
	svc, _, _ = newSvc(f)
	if _, err := svc.AssignUserToBranch(context.Background(), "u1", "nope", "r1"); err == nil {
		t.Fatal("expected error for unknown branch")
	}

	other := testRole()
	other.BranchID = strptr("other-branch")
	f = &fakeRepo{findRole: func(ctx context.Context, id string) (*db.BunTenantRole, error) {
		return other, nil
	}}
	svc, _, _ = newSvc(f)
	if _, err := svc.AssignUserToBranch(context.Background(), "u1", "b1", "r1"); err == nil {
		t.Fatal("expected error for role from another branch")
	}
}

func TestRemoveAssignment(t *testing.T) {
	f := &fakeRepo{assignment: testAssignment()}
	svc, _, _ := newSvc(f)

	if err := svc.RemoveAssignment(context.Background(), "a1"); err != nil {
		t.Fatalf("RemoveAssignment: %v", err)
	}
	if f.deletedAssignment != "a1" {
		t.Fatalf("assignment not deleted: %q", f.deletedAssignment)
	}

	f.assignment = nil
	if err := svc.RemoveAssignment(context.Background(), "missing"); err == nil {
		t.Fatal("expected error for unknown assignment")
	}
}
