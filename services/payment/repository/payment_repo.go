package repository

import (
	"context"
	"database/sql"

	"github.com/clinicmanager/services/payment/db"
	shareddb "github.com/clinicmanager/shared/db"
)

type PaymentRepository interface {
	FindWallet(ctx context.Context, tenantID, branchID, patientID string) (*db.BunPaymentWallet, error)
	CreateWallet(ctx context.Context, wallet *db.BunPaymentWallet) error
	WalletBalanceCents(ctx context.Context, walletID string) (int64, error)
	HeldCents(ctx context.Context, walletID string) (int64, error)
	CreateHold(ctx context.Context, hold *db.BunPaymentHold) error
	CaptureHold(ctx context.Context, appointmentID string) (*db.BunPaymentHold, error)
	ReleaseHold(ctx context.Context, appointmentID string) error
	InsertLedgerEntry(ctx context.Context, entry *db.BunPaymentLedger) error
	FindConsumptionByAppointment(ctx context.Context, appointmentID string) (*db.BunPaymentLedger, error)
	CreateTopUp(ctx context.Context, topUp *db.BunPaymentTopUp) error
	SetTopUpSession(ctx context.Context, id, sessionID, checkoutURL string) error
	FindTopUpByProviderSession(ctx context.Context, providerSessionID string) (*db.BunPaymentTopUp, error)
	MarkTopUpPaid(ctx context.Context, id string) (*db.BunPaymentTopUp, error)
	MarkTopUpFailed(ctx context.Context, id string) (*db.BunPaymentTopUp, error)
	ListPendingTopUps(ctx context.Context, walletID string) ([]*db.BunPaymentTopUp, error)
	ListLedger(ctx context.Context, walletID string, limit int) ([]*db.BunPaymentLedger, error)
	UpsertBranchService(ctx context.Context, svc *db.BunBranchService) (*db.BunBranchService, error)
	FindBranchService(ctx context.Context, branchID, appointmentTypeID string) (*db.BunBranchService, error)
	ListBranchServices(ctx context.Context, branchID string) ([]*db.BunBranchService, error)
	GetBranchSettings(ctx context.Context, branchID string) (*db.BunBranchSetting, error)
	UpsertBranchSettings(ctx context.Context, settings *db.BunBranchSetting) (*db.BunBranchSetting, error)
}

type paymentRepository struct {
	db       *shareddb.ScopedDB
	tenantdb *shareddb.ScopedDB
	alldb    *shareddb.ScopedDB
}

func NewPaymentRepository(dbs *shareddb.DBSet) PaymentRepository {
	return &paymentRepository{
		db:       dbs.DB,
		tenantdb: dbs.TenantDB,
		alldb:    dbs.AllDB,
	}
}

func (r *paymentRepository) FindWallet(ctx context.Context, tenantID, branchID, patientID string) (*db.BunPaymentWallet, error) {
	var w db.BunPaymentWallet
	err := r.tenantdb.NewSelect(ctx, &w).
		Where("tenant_id = ? AND branch_id = ? AND patient_id = ?", tenantID, branchID, patientID).
		Scan(ctx)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &w, nil
}

func (r *paymentRepository) CreateWallet(ctx context.Context, wallet *db.BunPaymentWallet) error {
	_, err := r.tenantdb.NewInsert(wallet).On("CONFLICT DO NOTHING").Exec(ctx)
	return err
}

func (r *paymentRepository) WalletBalanceCents(ctx context.Context, walletID string) (int64, error) {
	var entries []*db.BunPaymentLedger
	if err := r.tenantdb.NewSelect(ctx, &entries).
		Where("wallet_id = ?", walletID).
		Scan(ctx); err != nil {
		return 0, err
	}
	var sum int64
	for _, e := range entries {
		sum += int64(e.AmountCents)
	}
	return sum, nil
}

func (r *paymentRepository) HeldCents(ctx context.Context, walletID string) (int64, error) {
	var holds []*db.BunPaymentHold
	if err := r.tenantdb.NewSelect(ctx, &holds).
		Where("wallet_id = ? AND status = 'held'", walletID).
		Scan(ctx); err != nil {
		return 0, err
	}
	var sum int64
	for _, h := range holds {
		sum += int64(h.AmountCents)
	}
	return sum, nil
}

func (r *paymentRepository) CreateHold(ctx context.Context, hold *db.BunPaymentHold) error {
	_, err := r.tenantdb.NewInsert(hold).On("CONFLICT DO NOTHING").Exec(ctx)
	return err
}

func (r *paymentRepository) CaptureHold(ctx context.Context, appointmentID string) (*db.BunPaymentHold, error) {
	var h db.BunPaymentHold
	_, err := r.tenantdb.NewUpdate(ctx, &h).
		Set("status = 'captured'").
		Set("captured_at = NOW()").
		Where("appointment_id = ? AND status = 'held'", appointmentID).
		Returning("*").
		Exec(ctx)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &h, nil
}

func (r *paymentRepository) ReleaseHold(ctx context.Context, appointmentID string) error {
	_, err := r.tenantdb.NewUpdate(ctx, (*db.BunPaymentHold)(nil)).
		Set("status = 'released'").
		Set("released_at = NOW()").
		Where("appointment_id = ? AND status = 'held'", appointmentID).
		Exec(ctx)
	return err
}

func (r *paymentRepository) InsertLedgerEntry(ctx context.Context, entry *db.BunPaymentLedger) error {
	_, err := r.tenantdb.NewInsert(entry).On("CONFLICT DO NOTHING").Exec(ctx)
	return err
}

func (r *paymentRepository) FindConsumptionByAppointment(ctx context.Context, appointmentID string) (*db.BunPaymentLedger, error) {
	var e db.BunPaymentLedger
	err := r.tenantdb.NewSelect(ctx, &e).
		Where("appointment_id = ? AND type = 'consumption'", appointmentID).
		Scan(ctx)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &e, nil
}

func (r *paymentRepository) CreateTopUp(ctx context.Context, topUp *db.BunPaymentTopUp) error {
	_, err := r.tenantdb.NewInsert(topUp).Exec(ctx)
	return err
}

func (r *paymentRepository) SetTopUpSession(ctx context.Context, id, sessionID, checkoutURL string) error {
	_, err := r.tenantdb.NewUpdate(ctx, (*db.BunPaymentTopUp)(nil)).
		Set("provider_session_id = ?", sessionID).
		Set("checkout_url = ?", checkoutURL).
		Where("id = ?", id).
		Exec(ctx)
	return err
}

func (r *paymentRepository) FindTopUpByProviderSession(ctx context.Context, providerSessionID string) (*db.BunPaymentTopUp, error) {
	var t db.BunPaymentTopUp
	err := r.tenantdb.NewSelect(ctx, &t).
		Where("provider_session_id = ?", providerSessionID).
		Scan(ctx)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &t, nil
}

func (r *paymentRepository) MarkTopUpPaid(ctx context.Context, id string) (*db.BunPaymentTopUp, error) {
	var t db.BunPaymentTopUp
	_, err := r.tenantdb.NewUpdate(ctx, &t).
		Set("status = 'paid'").
		Set("paid_at = NOW()").
		Where("id = ? AND status = 'pending'", id).
		Returning("*").
		Exec(ctx)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &t, nil
}

func (r *paymentRepository) MarkTopUpFailed(ctx context.Context, id string) (*db.BunPaymentTopUp, error) {
	var t db.BunPaymentTopUp
	_, err := r.tenantdb.NewUpdate(ctx, &t).
		Set("status = 'failed'").
		Where("id = ? AND status = 'pending'", id).
		Returning("*").
		Exec(ctx)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &t, nil
}

func (r *paymentRepository) ListPendingTopUps(ctx context.Context, walletID string) ([]*db.BunPaymentTopUp, error) {
	var out []*db.BunPaymentTopUp
	err := r.tenantdb.NewSelect(ctx, &out).
		Where("wallet_id = ? AND status = 'pending'", walletID).
		Order("created_at DESC").
		Scan(ctx)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (r *paymentRepository) ListLedger(ctx context.Context, walletID string, limit int) ([]*db.BunPaymentLedger, error) {
	var out []*db.BunPaymentLedger
	q := r.tenantdb.NewSelect(ctx, &out).
		Where("wallet_id = ?", walletID).
		Order("created_at DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Scan(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *paymentRepository) UpsertBranchService(ctx context.Context, svc *db.BunBranchService) (*db.BunBranchService, error) {
	_, err := r.tenantdb.NewInsert(svc).
		On("CONFLICT (branch_id, appointment_type_id) DO UPDATE").
		Set("price_centavos = EXCLUDED.price_centavos").
		Set("is_offered = EXCLUDED.is_offered").
		Set("updated_at = NOW()").
		Returning("*").
		Exec(ctx)
	if err != nil {
		return nil, err
	}
	return svc, nil
}

func (r *paymentRepository) FindBranchService(ctx context.Context, branchID, appointmentTypeID string) (*db.BunBranchService, error) {
	var s db.BunBranchService
	err := r.tenantdb.NewSelect(ctx, &s).
		Where("branch_id = ? AND appointment_type_id = ?", branchID, appointmentTypeID).
		Scan(ctx)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &s, nil
}

func (r *paymentRepository) ListBranchServices(ctx context.Context, branchID string) ([]*db.BunBranchService, error) {
	var out []*db.BunBranchService
	err := r.tenantdb.NewSelect(ctx, &out).
		Where("branch_id = ?", branchID).
		Scan(ctx)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (r *paymentRepository) GetBranchSettings(ctx context.Context, branchID string) (*db.BunBranchSetting, error) {
	var s db.BunBranchSetting
	err := r.tenantdb.NewSelect(ctx, &s).
		Where("branch_id = ?", branchID).
		Scan(ctx)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &s, nil
}

func (r *paymentRepository) UpsertBranchSettings(ctx context.Context, settings *db.BunBranchSetting) (*db.BunBranchSetting, error) {
	_, err := r.tenantdb.NewInsert(settings).
		On("CONFLICT (branch_id) DO UPDATE").
		Set("payment_mode = EXCLUDED.payment_mode").
		Set("tenant_id = EXCLUDED.tenant_id").
		Set("updated_at = NOW()").
		Returning("*").
		Exec(ctx)
	if err != nil {
		return nil, err
	}
	return settings, nil
}
