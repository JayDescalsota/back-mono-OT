package db

import (
	"time"

	"github.com/uptrace/bun"
)

// BunPaymentWallet is one patient's prepaid credit wallet per branch.
// Balances are always derived from the ledger, never stored.
type BunPaymentWallet struct {
	bun.BaseModel `bun:"table:payment_wallets"`

	ID        string `bun:"id,pk"              json:"id"`
	TenantID  string `bun:"tenant_id,notnull" json:"tenantId"`
	BranchID  string `bun:"branch_id,notnull" json:"branchId"`
	PatientID string `bun:"patient_id,notnull" json:"patientId"`

	CreatedAt time.Time  `bun:"created_at" json:"-"`
	UpdatedAt *time.Time `bun:"updated_at" json:"-"`
}

func (BunPaymentWallet) IsEntity() {}

// BunPaymentHold reserves credits when an upfront session starts.
// Released on cancel, captured (as a consumption ledger entry) on complete.
type BunPaymentHold struct {
	bun.BaseModel `bun:"table:payment_holds"`

	ID            string `bun:"id,pk"              json:"id"`
	TenantID      string `bun:"tenant_id,notnull" json:"tenantId"`
	BranchID      string `bun:"branch_id,notnull" json:"branchId"`
	WalletID      string `bun:"wallet_id,notnull" json:"walletId"`
	AppointmentID string `bun:"appointment_id,notnull" json:"appointmentId"`
	AmountCents   int    `bun:"amount_centavos,notnull" json:"amountCents"`
	Status        string `bun:"status,notnull,default:'held'" json:"status"`

	CreatedAt  time.Time  `bun:"created_at" json:"-"`
	CapturedAt *time.Time `bun:"captured_at" json:"-"`
	ReleasedAt *time.Time `bun:"released_at" json:"-"`
}

func (BunPaymentHold) IsEntity() {}

// BunPaymentLedger is the append-only money trail. type is one of:
// top_up | consumption | refund | adjustment. Amounts are signed centavos
// and balance_after_centavos snapshots the wallet balance after posting.
type BunPaymentLedger struct {
	bun.BaseModel `bun:"table:payment_ledger"`

	ID            string  `bun:"id,pk"              json:"id"`
	TenantID      string  `bun:"tenant_id,notnull" json:"tenantId"`
	BranchID      string  `bun:"branch_id,notnull" json:"branchId"`
	WalletID      string  `bun:"wallet_id,notnull" json:"walletId"`
	Type          string  `bun:"type,notnull"       json:"type"`
	AmountCents   int     `bun:"amount_centavos,notnull" json:"amountCents"`
	BalanceAfter  int     `bun:"balance_after_centavos,notnull" json:"balanceAfterCents"`
	AppointmentID *string `bun:"appointment_id"    json:"appointmentId,omitempty"`
	ProviderRef   string  `bun:"provider_ref,notnull,default:''" json:"providerRef"`
	Note          string  `bun:"note,notnull,default:''" json:"note"`

	CreatedAt time.Time `bun:"created_at" json:"-"`
}

func (BunPaymentLedger) IsEntity() {}

// BunPaymentTopUp tracks one hosted-checkout top-up from creation until the
// webhook confirms (or fails) it. Only confirmed top-ups touch the ledger.
type BunPaymentTopUp struct {
	bun.BaseModel `bun:"table:payment_topups"`

	ID                string  `bun:"id,pk"              json:"id"`
	TenantID          string  `bun:"tenant_id,notnull" json:"tenantId"`
	BranchID          string  `bun:"branch_id,notnull" json:"branchId"`
	WalletID          string  `bun:"wallet_id,notnull" json:"walletId"`
	AppointmentID     *string `bun:"appointment_id"    json:"appointmentId,omitempty"`
	AmountCents       int     `bun:"amount_centavos,notnull" json:"amountCents"`
	Provider          string  `bun:"provider,notnull,default:'paymongo'" json:"provider"`
	ProviderSessionID *string `bun:"provider_session_id,unique" json:"providerSessionId,omitempty"`
	CheckoutURL       string  `bun:"checkout_url,notnull,default:''" json:"checkoutUrl"`
	Status            string  `bun:"status,notnull,default:'pending'" json:"status"`

	CreatedAt time.Time  `bun:"created_at" json:"-"`
	PaidAt    *time.Time `bun:"paid_at" json:"-"`
}

func (BunPaymentTopUp) IsEntity() {}

// BunBranchService is the per-branch service catalog: which appointment
// types the branch offers, and at what price (centavos). Prices are
// snapshotted onto appointments at booking time.
type BunBranchService struct {
	bun.BaseModel `bun:"table:payment_branch_services"`

	ID                string `bun:"id,pk"              json:"id"`
	TenantID          string `bun:"tenant_id,notnull" json:"tenantId"`
	BranchID          string `bun:"branch_id,notnull" json:"branchId"`
	AppointmentTypeID string `bun:"appointment_type_id,notnull" json:"appointmentTypeId"`
	PriceCents        int    `bun:"price_centavos,notnull,default:0" json:"priceCents"`
	IsOffered         bool   `bun:"is_offered,notnull,default:true" json:"isOffered"`

	CreatedAt time.Time  `bun:"created_at" json:"-"`
	UpdatedAt *time.Time `bun:"updated_at" json:"-"`
}

func (BunBranchService) IsEntity() {}

// BunBranchSetting holds per-branch payment policy. payment_mode is either
// "upfront" (cannot start a session without cover) or "postpaid"
// (cannot complete a session without cover).
type BunBranchSetting struct {
	bun.BaseModel `bun:"table:payment_branch_settings"`

	BranchID    string `bun:"branch_id,pk"       json:"branchId"`
	TenantID    string `bun:"tenant_id,notnull"  json:"tenantId"`
	PaymentMode string `bun:"payment_mode,notnull,default:'upfront'" json:"paymentMode"`

	CreatedAt time.Time  `bun:"created_at" json:"-"`
	UpdatedAt *time.Time `bun:"updated_at" json:"-"`
}

func (BunBranchSetting) IsEntity() {}
