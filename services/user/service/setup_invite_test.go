package service_test

// Tests for the platform account-setup invite: an admin emails an
// unvalidated account a one-time link; submitting a new password through it
// both validates the account and sets the password (see ResetPassword).

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/clinicmanager/services/user/models"
	"github.com/clinicmanager/services/user/service"
)

type recordingMailer struct {
	to      []string
	subject []string
	body    []string
}

func (m *recordingMailer) Send(to, subject, body string) error {
	m.to = append(m.to, to)
	m.subject = append(m.subject, subject)
	m.body = append(m.body, body)
	return nil
}

func setupSvc(repo *mockRepo, mailer *recordingMailer) *service.AuthService {
	svc := service.NewAuthService(repo, "test-secret", mailer, "http://api", 15*time.Minute, 7*24*time.Hour)
	svc.FrontendURL = "http://front:4200"
	return svc
}

func unvalidatedUserStub(id, email string) *models.User {
	return &models.User{ID: id, Email: email, IsActive: true, IsValidated: false}
}

func TestSendSetupInvite(t *testing.T) {
	repo := defaultMock()
	repo.findByIDFn = func(_ context.Context, _ string) (*models.User, error) {
		return unvalidatedUserStub("u-new", "new@clinic.com"), nil
	}
	mailer := &recordingMailer{}
	svc := setupSvc(repo, mailer)

	if err := svc.SendSetupInvite(context.Background(), "u-new"); err != nil {
		t.Fatalf("SendSetupInvite: %v", err)
	}
	if len(repo.setResetTokens) != 1 || repo.setResetTokens[0][0] != "u-new" || len(repo.setResetTokens[0][1]) < 32 {
		t.Fatalf("one-time token not stored: %v", repo.setResetTokens)
	}
	if len(mailer.to) != 1 || mailer.to[0] != "new@clinic.com" {
		t.Fatalf("invite not emailed to the account: %v", mailer.to)
	}
	if !strings.Contains(mailer.body[0], "http://front:4200/auth/reset-password?token="+repo.setResetTokens[0][1]) {
		t.Fatalf("email body missing frontend setup link: %q", mailer.body[0])
	}
}

func TestSendSetupInviteGuards(t *testing.T) {
	repo := defaultMock()
	repo.findByIDFn = func(_ context.Context, _ string) (*models.User, error) {
		u := unvalidatedUserStub("u-new", "new@clinic.com")
		u.IsValidated = true
		return u, nil
	}
	svc := setupSvc(repo, &recordingMailer{})

	if err := svc.SendSetupInvite(context.Background(), "u-new"); err == nil {
		t.Fatal("validated accounts must use password reset, not setup invite")
	}

	repo.findByIDFn = func(_ context.Context, _ string) (*models.User, error) { return nil, nil }
	if err := svc.SendSetupInvite(context.Background(), "ghost"); err == nil {
		t.Fatal("expected error for unknown user")
	}
}

func TestResetPasswordValidatesAccount(t *testing.T) {
	repo := defaultMock()
	repo.findByResetTokenFn = func(_ context.Context, _ string) (*models.User, error) {
		return unvalidatedUserStub("u-new", "new@clinic.com"), nil
	}
	svc := setupSvc(repo, &recordingMailer{})

	if err := svc.ResetPassword(context.Background(), "tok-abc", "newsecret1"); err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}
	if len(repo.validatedIDs) != 1 || repo.validatedIDs[0] != "u-new" {
		t.Fatalf("reset must validate the account: %v", repo.validatedIDs)
	}
	if len(repo.resetPasswords) != 1 || repo.resetPasswords[0] != "u-new" {
		t.Fatalf("password not reset: %v", repo.resetPasswords)
	}
}

func TestResetPasswordKeepsValidatedAccount(t *testing.T) {
	repo := defaultMock()
	repo.findByResetTokenFn = func(_ context.Context, _ string) (*models.User, error) {
		u := unvalidatedUserStub("u-old", "old@clinic.com")
		u.IsValidated = true
		return u, nil
	}
	svc := setupSvc(repo, &recordingMailer{})

	if err := svc.ResetPassword(context.Background(), "tok-abc", "newsecret1"); err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}
	if len(repo.validatedIDs) != 0 {
		t.Fatal("already-validated accounts must not be re-validated")
	}
	if len(repo.resetPasswords) != 1 {
		t.Fatal("password reset must still happen")
	}
}
