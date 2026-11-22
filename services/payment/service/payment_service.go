package service

import (
	"context"
	"fmt"
	"time"

	"github.com/clinicmanager/services/payment/db"
	"github.com/clinicmanager/services/payment/repository"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// CheckoutSessionInput is the provider-agnostic request to start a hosted
// checkout. Amounts are integer minor units (centavos for PHP).
type CheckoutSessionInput struct {
	AmountCents        int
	Currency           string
	Description        string
	Reference          string
	SuccessURL         string
	CancelURL          string
	PaymentMethodTypes []string
	Metadata           map[string]string
}

// CheckoutSession is a provider-agnostic created checkout.
type CheckoutSession struct {
	ID          string
	CheckoutURL string
}

// WebhookEvent is a normalized payment event from any provider webhook.
type WebhookEvent struct {
	ID          string
	Type        string
	SessionID   string
	AmountCents int
	Livemode    bool
	Reference   string
}

// Gateway abstracts the payment provider (PayMongo today, someone else
// tomorrow). The billing logic below only ever talks to this interface,
// mirroring how userservice talks to its Mailer interface.
type Gateway interface {
	Name() string
	CreateCheckoutSession(ctx context.Context, in CheckoutSessionInput) (*CheckoutSession, error)
	ParseWebhook(rawBody []byte, signatureHeader string) (*WebhookEvent, error)
}

// PaymentService owns wallets, holds, the append-only ledger, top-ups and
// the branch service catalog. Balances are always derived (SUM of ledger
// entries minus active holds) — never stored — so they cannot drift.
type PaymentService struct {
	Repo        repository.PaymentRepository
	Cache       *redis.Client
	gateway     Gateway
	frontendURL string
	// SuccessURL / CancelURL are where the hosted checkout returns the
	// customer. Default to the frontend billing pages; override for tunnels.
	SuccessURL string
	CancelURL  string
}

func NewPaymentService(repo repository.PaymentRepository, cache *redis.Client, gateway Gateway, frontendURL string) *PaymentService {
	return &PaymentService{Repo: repo, Cache: cache, gateway: gateway, frontendURL: frontendURL}
}

// minTopUpCents is the smallest top-up we accept (PHP 1.00).
const minTopUpCents = 100

// EnsureWallet returns the patient's wallet, creating an empty one on first use.
func (s *PaymentService) EnsureWallet(ctx context.Context, tenantID, branchID, patientID string) (*db.BunPaymentWallet, error) {
	w, err := s.Repo.FindWallet(ctx, tenantID, branchID, patientID)
	if err != nil {
		return nil, err
	}
	if w != nil {
		return w, nil
	}
	w = &db.BunPaymentWallet{
		ID:        uuid.NewString(),
		TenantID:  tenantID,
		BranchID:  branchID,
		PatientID: patientID,
		CreatedAt: time.Now(),
	}
	if err := s.Repo.CreateWallet(ctx, w); err != nil {
		return nil, err
	}
	return w, nil
}

// WalletBalance returns balance, held and spendable amounts (all centavos).
func (s *PaymentService) WalletBalance(ctx context.Context, tenantID, branchID, patientID string) (balance, held, spendable int64, err error) {
	w, err := s.EnsureWallet(ctx, tenantID, branchID, patientID)
	if err != nil {
		return 0, 0, 0, err
	}
	balance, err = s.Repo.WalletBalanceCents(ctx, w.ID)
	if err != nil {
		return 0, 0, 0, err
	}
	held, err = s.Repo.HeldCents(ctx, w.ID)
	if err != nil {
		return 0, 0, 0, err
	}
	spendable = balance - held
	if spendable < 0 {
		spendable = 0
	}
	return balance, held, spendable, nil
}

// TopUpInput starts a hosted-checkout top-up for a wallet.
type TopUpInput struct {
	TenantID      string
	BranchID      string
	PatientID     string
	AmountCents   int
	AppointmentID *string
}

// CreateTopUp validates the request, asks the gateway for a checkout
// session, and records the pending top-up. Money moves only when the
// webhook confirms payment.
func (s *PaymentService) CreateTopUp(ctx context.Context, in TopUpInput) (*db.BunPaymentTopUp, string, error) {
	if in.AmountCents < minTopUpCents {
		return nil, "", fmt.Errorf("top-up amount must be at least PHP 1.00")
	}
	wallet, err := s.EnsureWallet(ctx, in.TenantID, in.BranchID, in.PatientID)
	if err != nil {
		return nil, "", err
	}
	topUp := &db.BunPaymentTopUp{
		ID:            uuid.NewString(),
		TenantID:      in.TenantID,
		BranchID:      in.BranchID,
		WalletID:      wallet.ID,
		AppointmentID: in.AppointmentID,
		AmountCents:   in.AmountCents,
		Provider:      s.gateway.Name(),
		Status:        "pending",
		CreatedAt:     time.Now(),
	}
	if err := s.Repo.CreateTopUp(ctx, topUp); err != nil {
		return nil, "", err
	}
	session, err := s.gateway.CreateCheckoutSession(ctx, CheckoutSessionInput{
		AmountCents: in.AmountCents,
		Currency:    "PHP",
		Description: "Clinic credits top-up",
		Reference:   topUp.ID,
		SuccessURL:  s.successURL(),
		CancelURL:   s.cancelURL(),
		Metadata: map[string]string{
			"topup_id":       topUp.ID,
			"wallet_id":      wallet.ID,
			"appointment_id": derefString(in.AppointmentID),
		},
	})
	if err != nil {
		// Don't leave an unusable pending row in the UI.
		_, _ = s.Repo.MarkTopUpFailed(ctx, topUp.ID)
		return nil, "", err
	}
	if err := s.Repo.SetTopUpSession(ctx, topUp.ID, session.ID, session.CheckoutURL); err != nil {
		_, _ = s.Repo.MarkTopUpFailed(ctx, topUp.ID)
		return nil, "", err
	}
	topUp.ProviderSessionID = &session.ID
	topUp.CheckoutURL = session.CheckoutURL
	return topUp, session.CheckoutURL, nil
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (s *PaymentService) successURL() string {
	if s.SuccessURL != "" {
		return s.SuccessURL
	}
	return s.frontendURL + "/billing/success"
}

func (s *PaymentService) cancelURL() string {
	if s.CancelURL != "" {
		return s.CancelURL
	}
	return s.frontendURL + "/billing/cancel"
}

// HandleWebhook verifies the delivery and applies it. Unknown events are
// acknowledged and ignored; already-processed ones are idempotent no-ops.
func (s *PaymentService) HandleWebhook(ctx context.Context, rawBody []byte, signatureHeader string) error {
	event, err := s.gateway.ParseWebhook(rawBody, signatureHeader)
	if err != nil {
		return err
	}
	if event == nil {
		return nil
	}
	switch event.Type {
	case "checkout_session.payment.paid", "payment.paid":
		return s.applyTopUpPaid(ctx, event)
	case "checkout_session.payment.failed", "payment.failed":
		return s.markTopUpFailed(ctx, event)
	default:
		return nil
	}
}

func (s *PaymentService) applyTopUpPaid(ctx context.Context, event *WebhookEvent) error {
	topUp, err := s.Repo.FindTopUpByProviderSession(ctx, event.SessionID)
	if err != nil {
		return err
	}
	if topUp == nil {
		return fmt.Errorf("payment: no top-up found for session %s", event.SessionID)
	}
	if topUp.Status == "paid" {
		return nil
	}
	if event.AmountCents != 0 && event.AmountCents != topUp.AmountCents {
		return fmt.Errorf("payment: amount mismatch for top-up %s (expected %d, got %d)", topUp.ID, topUp.AmountCents, event.AmountCents)
	}
	marked, err := s.Repo.MarkTopUpPaid(ctx, topUp.ID)
	if err != nil {
		return err
	}
	if marked == nil {
		return nil
	}
	balance, err := s.Repo.WalletBalanceCents(ctx, topUp.WalletID)
	if err != nil {
		return err
	}
	entry := &db.BunPaymentLedger{
		ID:            uuid.NewString(),
		TenantID:      marked.TenantID,
		BranchID:      marked.BranchID,
		WalletID:      marked.WalletID,
		Type:          "top_up",
		AmountCents:   marked.AmountCents,
		BalanceAfter:  int(balance) + marked.AmountCents,
		AppointmentID: marked.AppointmentID,
		ProviderRef:   derefString(marked.ProviderSessionID),
		Note:          "PayMongo top-up",
	}
	return s.Repo.InsertLedgerEntry(ctx, entry)
}

func (s *PaymentService) markTopUpFailed(ctx context.Context, event *WebhookEvent) error {
	topUp, err := s.Repo.FindTopUpByProviderSession(ctx, event.SessionID)
	if err != nil {
		return err
	}
	if topUp == nil || topUp.Status != "pending" {
		return nil
	}
	_, err = s.Repo.MarkTopUpFailed(ctx, topUp.ID)
	return err
}

// HoldFunds reserves amount for an appointment when the branch runs the
// upfront model. Idempotent: an existing active hold succeeds silently.
func (s *PaymentService) HoldFunds(ctx context.Context, tenantID, branchID, patientID, appointmentID string, amountCents int) error {
	if amountCents <= 0 {
		return nil
	}
	settings, err := s.Repo.GetBranchSettings(ctx, branchID)
	if err != nil {
		return err
	}
	if settings != nil && settings.PaymentMode != "upfront" {
		return nil
	}
	wallet, err := s.EnsureWallet(ctx, tenantID, branchID, patientID)
	if err != nil {
		return err
	}
	balance, err := s.Repo.WalletBalanceCents(ctx, wallet.ID)
	if err != nil {
		return err
	}
	held, err := s.Repo.HeldCents(ctx, wallet.ID)
	if err != nil {
		return err
	}
	if balance-held < int64(amountCents) {
		return fmt.Errorf("insufficient credits: need %d, available %d", amountCents, balance-held)
	}
	return s.Repo.CreateHold(ctx, &db.BunPaymentHold{
		ID:            uuid.NewString(),
		TenantID:      tenantID,
		BranchID:      branchID,
		WalletID:      wallet.ID,
		AppointmentID: appointmentID,
		AmountCents:   amountCents,
		Status:        "held",
		CreatedAt:     time.Now(),
	})
}

// CaptureHold settles an upfront hold into a consumption entry. If no hold
// exists (e.g. a session started before holds were enforced), it falls back
// to a balance check + direct consumption so completion never dead-ends.
func (s *PaymentService) CaptureHold(ctx context.Context, tenantID, branchID, patientID, appointmentID string, amountCents int) error {
	held, err := s.Repo.CaptureHold(ctx, appointmentID)
	if err != nil {
		return err
	}
	if held == nil {
		if existing, err := s.Repo.FindConsumptionByAppointment(ctx, appointmentID); err != nil {
			return err
		} else if existing != nil {
			return nil
		}
		return s.consume(ctx, tenantID, branchID, patientID, appointmentID, amountCents)
	}
	err = s.recordConsumption(ctx, tenantID, branchID, held.WalletID, appointmentID, held.AmountCents, "Session completed")
	return err
}

// ConsumeForAppointment implements the postpaid path: check spendable,
// then record consumption. Idempotent via the appointment unique guard.
func (s *PaymentService) ConsumeForAppointment(ctx context.Context, tenantID, branchID, patientID, appointmentID string, amountCents int) error {
	existing, err := s.Repo.FindConsumptionByAppointment(ctx, appointmentID)
	if err != nil {
		return err
	}
	if existing != nil {
		return nil
	}
	return s.consume(ctx, tenantID, branchID, patientID, appointmentID, amountCents)
}

func (s *PaymentService) consume(ctx context.Context, tenantID, branchID, patientID, appointmentID string, amountCents int) error {
	if amountCents <= 0 {
		return nil
	}
	wallet, err := s.EnsureWallet(ctx, tenantID, branchID, patientID)
	if err != nil {
		return err
	}
	balance, err := s.Repo.WalletBalanceCents(ctx, wallet.ID)
	if err != nil {
		return err
	}
	held, err := s.Repo.HeldCents(ctx, wallet.ID)
	if err != nil {
		return err
	}
	if balance-held < int64(amountCents) {
		return fmt.Errorf("insufficient credits: need %d, available %d", amountCents, balance-held)
	}
	return s.recordConsumption(ctx, tenantID, branchID, wallet.ID, appointmentID, amountCents, "Session completed")
}

func (s *PaymentService) recordConsumption(ctx context.Context, tenantID, branchID, walletID, appointmentID string, amountCents int, note string) error {
	balance, err := s.Repo.WalletBalanceCents(ctx, walletID)
	if err != nil {
		return err
	}
	return s.Repo.InsertLedgerEntry(ctx, &db.BunPaymentLedger{
		ID:            uuid.NewString(),
		TenantID:      tenantID,
		BranchID:      branchID,
		WalletID:      walletID,
		Type:          "consumption",
		AmountCents:   -amountCents,
		BalanceAfter:  int(balance) - amountCents,
		AppointmentID: &appointmentID,
		Note:          note,
		CreatedAt:     time.Now(),
	})
}

// ReleaseHold frees a reservation, e.g. when an appointment is cancelled.
func (s *PaymentService) ReleaseHold(ctx context.Context, appointmentID string) error {
	return s.Repo.ReleaseHold(ctx, appointmentID)
}

// ListLedger returns recent ledger entries, newest first.
func (s *PaymentService) ListLedger(ctx context.Context, tenantID, branchID, patientID string, limit int) ([]*db.BunPaymentLedger, error) {
	wallet, err := s.EnsureWallet(ctx, tenantID, branchID, patientID)
	if err != nil {
		return nil, err
	}
	return s.Repo.ListLedger(ctx, wallet.ID, limit)
}

// ListPendingTopUps returns awaiting-payment checkouts for a wallet.
func (s *PaymentService) ListPendingTopUps(ctx context.Context, tenantID, branchID, patientID string) ([]*db.BunPaymentTopUp, error) {
	wallet, err := s.EnsureWallet(ctx, tenantID, branchID, patientID)
	if err != nil {
		return nil, err
	}
	return s.Repo.ListPendingTopUps(ctx, wallet.ID)
}

// Branch catalog management (admin): offered services + prices.
func (s *PaymentService) ListBranchServices(ctx context.Context, branchID string) ([]*db.BunBranchService, error) {
	return s.Repo.ListBranchServices(ctx, branchID)
}

func (s *PaymentService) SetBranchService(ctx context.Context, tenantID, branchID, appointmentTypeID string, priceCents int, isOffered bool) (*db.BunBranchService, error) {
	if priceCents < 0 {
		return nil, fmt.Errorf("price cannot be negative")
	}
	return s.Repo.UpsertBranchService(ctx, &db.BunBranchService{
		ID:                uuid.NewString(),
		TenantID:          tenantID,
		BranchID:          branchID,
		AppointmentTypeID: appointmentTypeID,
		PriceCents:        priceCents,
		IsOffered:         isOffered,
	})
}

func (s *PaymentService) GetBranchPrice(ctx context.Context, branchID, appointmentTypeID string) (int, bool, error) {
	svc, err := s.Repo.FindBranchService(ctx, branchID, appointmentTypeID)
	if err != nil || svc == nil {
		return 0, false, err
	}
	if !svc.IsOffered {
		return 0, false, nil
	}
	return svc.PriceCents, true, nil
}

// Branch payment policy (upfront vs postpaid). Defaults to upfront.
func (s *PaymentService) GetBranchPaymentMode(ctx context.Context, branchID string) (string, error) {
	settings, err := s.Repo.GetBranchSettings(ctx, branchID)
	if err != nil || settings == nil {
		return "upfront", err
	}
	if settings.PaymentMode != "upfront" && settings.PaymentMode != "postpaid" {
		return "upfront", nil
	}
	return settings.PaymentMode, nil
}

func (s *PaymentService) SetBranchPaymentMode(ctx context.Context, tenantID, branchID, mode string) (*db.BunBranchSetting, error) {
	if mode != "upfront" && mode != "postpaid" {
		return nil, fmt.Errorf("payment mode must be upfront or postpaid")
	}
	return s.Repo.UpsertBranchSettings(ctx, &db.BunBranchSetting{
		BranchID:    branchID,
		TenantID:    tenantID,
		PaymentMode: mode,
	})
}
