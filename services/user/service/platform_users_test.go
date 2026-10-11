package service_test

// Tests for platform-admin user management. fakeUserRepo implements
// repository.UserRepository with hooks only for the methods these tests
// exercise; everything else returns zero values.

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/clinicmanager/services/user/models"
	"github.com/clinicmanager/services/user/repository"
	"github.com/clinicmanager/services/user/service"
)

type fakeUserRepo struct {
	callerIsAdmin bool
	roles         []*models.AppRole
	users         []*models.User
	userRoles     map[string][]*models.AppRole
	userByEmail   map[string]*models.User
	userByID      map[string]*models.User
	created       *models.User
	assigned      [][2]string
	removed       [][2]string
}

func platformRole(id int, name string) *models.AppRole {
	return &models.AppRole{ID: id, Name: name, Description: name + " role"}
}

func platformUser(id, email string) *models.User {
	return &models.User{ID: id, Email: email, IsActive: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
}

func newPlatformSvc(f *fakeUserRepo, callerID string) *service.UserService {
	return service.NewUserService(f, func(ctx context.Context) string { return callerID }, nil)
}

func (f *fakeUserRepo) FindUserByID(ctx context.Context, id string) (*models.User, error) {
	return f.userByID[id], nil
}
func (f *fakeUserRepo) FindUserByEmail(ctx context.Context, email string) (*models.User, error) {
	return f.userByEmail[strings.ToLower(email)], nil
}
func (f *fakeUserRepo) HasAppRole(ctx context.Context, userID, roleName string) (bool, error) {
	if userID == "admin1" {
		return f.callerIsAdmin && roleName == "super_admin", nil
	}
	for _, r := range f.userRoles[userID] {
		if r.Name == roleName {
			return true, nil
		}
	}
	return false, nil
}
func (f *fakeUserRepo) FindUserAppRoles(ctx context.Context, userID string) ([]*models.AppRole, error) {
	return f.userRoles[userID], nil
}
func (f *fakeUserRepo) GetAppRoleByID(ctx context.Context, id int) (*models.AppRole, error) {
	return nil, nil
}
func (f *fakeUserRepo) FindAllUsers(ctx context.Context) ([]*models.User, error) {
	return f.users, nil
}
func (f *fakeUserRepo) FindUsersByIDs(ctx context.Context, ids []string) ([]*models.User, error) {
	return nil, nil
}
func (f *fakeUserRepo) FindAllAppRoles(ctx context.Context) ([]*models.AppRole, error) {
	return f.roles, nil
}
func (f *fakeUserRepo) CreateUser(ctx context.Context, email, password string) (*models.User, error) {
	f.created = platformUser("new-id", email)
	f.userByID["new-id"] = f.created
	return f.created, nil
}
func (f *fakeUserRepo) AssignAppRole(ctx context.Context, userID string, appRoleID int) error {
	for _, r := range f.roles {
		if r.ID == appRoleID {
			f.userRoles[userID] = append(f.userRoles[userID], r)
		}
	}
	f.assigned = append(f.assigned, [2]string{userID, strconv.Itoa(appRoleID)})
	return nil
}
func (f *fakeUserRepo) RemoveAppRole(ctx context.Context, userID string, appRoleID int) error {
	kept := f.userRoles[userID][:0:0]
	for _, r := range f.userRoles[userID] {
		if r.ID != appRoleID {
			kept = append(kept, r)
		}
	}
	f.userRoles[userID] = kept
	f.removed = append(f.removed, [2]string{userID, strconv.Itoa(appRoleID)})
	return nil
}
func (f *fakeUserRepo) FindProfileByUserID(ctx context.Context, userID string) (*models.UserProfile, error) {
	return nil, nil
}

// Unused interface surface — zero values.
func (f *fakeUserRepo) Register(ctx context.Context, email, password, validationToken string) (*models.User, error) {
	return nil, nil
}
func (f *fakeUserRepo) UpdateLastLogin(ctx context.Context, userID string) error { return nil }
func (f *fakeUserRepo) FindUserByValidationToken(ctx context.Context, token string) (*models.User, error) {
	return nil, nil
}
func (f *fakeUserRepo) MarkUserAsValidated(ctx context.Context, userID string) error { return nil }
func (f *fakeUserRepo) UpdatePassword(ctx context.Context, userID, newPassword string) error {
	return nil
}
func (f *fakeUserRepo) SetPasswordResetToken(ctx context.Context, userID, token string, expiresAt time.Time) error {
	return nil
}
func (f *fakeUserRepo) FindUserByPasswordResetToken(ctx context.Context, token string) (*models.User, error) {
	return nil, nil
}
func (f *fakeUserRepo) ResetPassword(ctx context.Context, userID, newPassword string) error {
	return nil
}
func (f *fakeUserRepo) CreateSession(ctx context.Context, session *models.Session) error {
	return nil
}
func (f *fakeUserRepo) FindSessionByID(ctx context.Context, id int64) (*models.Session, error) {
	return nil, nil
}
func (f *fakeUserRepo) FindSessionByToken(ctx context.Context, token string) (*models.Session, error) {
	return nil, nil
}
func (f *fakeUserRepo) FindSessionsByUser(ctx context.Context, userID string) ([]*models.Session, error) {
	return nil, nil
}
func (f *fakeUserRepo) RevokeSession(ctx context.Context, sessionID int64) error { return nil }
func (f *fakeUserRepo) RevokeAllSessionsForUser(ctx context.Context, userID string) error {
	return nil
}
func (f *fakeUserRepo) UpdateSessionAccessToken(ctx context.Context, sessionID int64, accessToken string) error {
	return nil
}
func (f *fakeUserRepo) UpdateUser(ctx context.Context, id string, isActive bool) error { return nil }
func (f *fakeUserRepo) UpsertProfile(ctx context.Context, profile *models.UserProfile) error {
	return nil
}
func (f *fakeUserRepo) FindPractitionerProfileByUserID(ctx context.Context, userID string) (*models.PractitionerProfile, error) {
	return nil, nil
}
func (f *fakeUserRepo) UpsertPractitionerProfile(ctx context.Context, profile *models.PractitionerProfile) error {
	return nil
}

var _ repository.UserRepository = (*fakeUserRepo)(nil)

func platformTestRepo() *fakeUserRepo {
	return &fakeUserRepo{
		callerIsAdmin: true,
		roles: []*models.AppRole{
			platformRole(1, "super_admin"),
			platformRole(2, "app_admin"),
			platformRole(4, "user"),
		},
		userByEmail: map[string]*models.User{},
		userByID:    map[string]*models.User{},
		userRoles:   map[string][]*models.AppRole{},
	}
}

func TestRequireSuperAdmin(t *testing.T) {
	f := platformTestRepo()
	if err := newPlatformSvc(f, "").RequireSuperAdmin(context.Background()); err == nil {
		t.Fatal("anonymous caller must be rejected")
	}
	f.callerIsAdmin = false
	if err := newPlatformSvc(f, "admin1").RequireSuperAdmin(context.Background()); err == nil {
		t.Fatal("non-admin caller must be rejected")
	}
	f.callerIsAdmin = true
	if err := newPlatformSvc(f, "admin1").RequireSuperAdmin(context.Background()); err != nil {
		t.Fatalf("super_admin must pass: %v", err)
	}
}

func TestCreatePlatformUser(t *testing.T) {
	f := platformTestRepo()
	svc := newPlatformSvc(f, "admin1")

	user, err := svc.CreatePlatformUser(context.Background(), "  Boss@clinic.com ", "secret123", "app_admin")
	if err != nil {
		t.Fatalf("CreatePlatformUser: %v", err)
	}
	if user.Email != "boss@clinic.com" {
		t.Fatalf("email not normalized: %q", user.Email)
	}
	if len(f.assigned) != 1 {
		t.Fatalf("expected one role grant, got %v", f.assigned)
	}
	found := false
	for _, r := range user.AppRoles {
		if r.Name == "app_admin" {
			found = true
		}
	}
	if !found {
		t.Fatalf("new account missing app_admin role: %+v", user.AppRoles)
	}
}

func TestCreatePlatformUserValidation(t *testing.T) {
	f := platformTestRepo()
	svc := newPlatformSvc(f, "admin1")

	if _, err := svc.CreatePlatformUser(context.Background(), "not-an-email", "secret123", "user"); err == nil {
		t.Fatal("expected error for malformed email")
	}
	if _, err := svc.CreatePlatformUser(context.Background(), "a@b.com", "short", "user"); err == nil {
		t.Fatal("expected error for short password")
	}
	if _, err := svc.CreatePlatformUser(context.Background(), "a@b.com", "secret123", "janitor"); err == nil {
		t.Fatal("expected error for unknown app role")
	}
	f.userByEmail["taken@clinic.com"] = platformUser("u9", "taken@clinic.com")
	if _, err := svc.CreatePlatformUser(context.Background(), "taken@clinic.com", "secret123", "user"); err == nil {
		t.Fatal("expected error for duplicate email")
	}
	f.callerIsAdmin = false
	if _, err := svc.CreatePlatformUser(context.Background(), "x@clinic.com", "secret123", "user"); err == nil {
		t.Fatal("non-admin must not create platform users")
	}
}

func TestSetUserAppRole(t *testing.T) {
	f := platformTestRepo()
	f.userByID["u1"] = platformUser("u1", "staff@clinic.com")
	svc := newPlatformSvc(f, "admin1")

	user, err := svc.SetUserAppRole(context.Background(), "u1", "support")
	if err == nil {
		t.Fatal("expected error: support role does not exist in fake")
	}
	_ = user

	user, err = svc.SetUserAppRole(context.Background(), "u1", "app_admin")
	if err != nil {
		t.Fatalf("SetUserAppRole: %v", err)
	}
	// Idempotent second grant.
	if _, err := svc.SetUserAppRole(context.Background(), "u1", "app_admin"); err != nil {
		t.Fatalf("repeat grant must be a no-op: %v", err)
	}
	if len(f.assigned) != 1 {
		t.Fatalf("repeat grant must not write twice: %v", f.assigned)
	}
	_ = user

	if _, err := svc.SetUserAppRole(context.Background(), "ghost", "user"); err == nil {
		t.Fatal("expected error for unknown user")
	}
}

func TestRemoveUserAppRole(t *testing.T) {
	f := platformTestRepo()
	f.userByID["u1"] = platformUser("u1", "staff@clinic.com")
	f.userRoles["u1"] = []*models.AppRole{platformRole(2, "app_admin")}
	f.users = []*models.User{f.userByID["u1"]}
	svc := newPlatformSvc(f, "admin1")

	if _, err := svc.RemoveUserAppRole(context.Background(), "u1", "app_admin"); err != nil {
		t.Fatalf("RemoveUserAppRole: %v", err)
	}
	if len(f.userRoles["u1"]) != 0 {
		t.Fatalf("role not removed: %+v", f.userRoles["u1"])
	}
}

func TestRemoveLastSuperAdminBlocked(t *testing.T) {
	f := platformTestRepo()
	admin := platformUser("admin1", "superadmin@clinic.com")
	f.userByID["admin1"] = admin
	f.userRoles["admin1"] = []*models.AppRole{platformRole(1, "super_admin")}
	f.users = []*models.User{admin}
	svc := newPlatformSvc(f, "admin1")

	if _, err := svc.RemoveUserAppRole(context.Background(), "admin1", "super_admin"); err == nil {
		t.Fatal("demoting the last super_admin must be refused")
	}
	if len(f.removed) != 0 {
		t.Fatal("blocked demotion must not write")
	}

	// With a second super_admin around, demotion is allowed.
	admin2 := platformUser("admin2", "second@clinic.com")
	f.userByID["admin2"] = admin2
	f.userRoles["admin2"] = []*models.AppRole{platformRole(1, "super_admin")}
	f.users = []*models.User{admin, admin2}
	if _, err := svc.RemoveUserAppRole(context.Background(), "admin1", "super_admin"); err != nil {
		t.Fatalf("demotion with backup admin must succeed: %v", err)
	}
}

func TestListAppRolesGated(t *testing.T) {
	f := platformTestRepo()
	svc := newPlatformSvc(f, "admin1")
	roles, err := svc.ListAppRoles(context.Background())
	if err != nil || len(roles) != 3 {
		t.Fatalf("ListAppRoles: %+v %v", roles, err)
	}
	f.callerIsAdmin = false
	if _, err := svc.ListAppRoles(context.Background()); err == nil {
		t.Fatal("non-admin must not list app roles")
	}
}
