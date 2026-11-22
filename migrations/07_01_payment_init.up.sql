-- Payments domain: prepaid credit wallets, appointment holds, append-only
-- ledger, checkout sessions (top-ups), branch service catalog with prices,
-- and per-branch payment policy (upfront vs postpaid).
CREATE TABLE IF NOT EXISTS payment_wallets (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    branch_id UUID NOT NULL,
    patient_id UUID NOT NULL REFERENCES patients(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ,
    UNIQUE (tenant_id, branch_id, patient_id)
);

CREATE TABLE IF NOT EXISTS payment_holds (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    branch_id UUID NOT NULL,
    wallet_id UUID NOT NULL REFERENCES payment_wallets(id) ON DELETE CASCADE,
    appointment_id UUID NOT NULL REFERENCES booking_appointments(id) ON DELETE CASCADE,
    amount_centavos INTEGER NOT NULL,
    status TEXT NOT NULL DEFAULT 'held',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    captured_at TIMESTAMPTZ,
    released_at TIMESTAMPTZ
);

-- One active hold per appointment at most; completed/cancelled sessions
-- release or capture their hold first, so retries stay idempotent.
CREATE UNIQUE INDEX IF NOT EXISTS payment_holds_one_active_per_appointment
    ON payment_holds (appointment_id) WHERE status = 'held';

CREATE TABLE IF NOT EXISTS payment_ledger (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    branch_id UUID NOT NULL,
    wallet_id UUID NOT NULL REFERENCES payment_wallets(id) ON DELETE CASCADE,
    type TEXT NOT NULL,
    amount_centavos INTEGER NOT NULL,
    balance_after_centavos INTEGER NOT NULL,
    appointment_id UUID REFERENCES booking_appointments(id) ON DELETE SET NULL,
    provider_ref TEXT NOT NULL DEFAULT '',
    note TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- A consumption entry is written once per appointment; the unique index
-- makes double-charging impossible even if complete is retried.
CREATE UNIQUE INDEX IF NOT EXISTS payment_ledger_one_consumption_per_appointment
    ON payment_ledger (appointment_id) WHERE type = 'consumption';

-- Ledger inserts are idempotent per provider reference (webhook retries).
CREATE UNIQUE INDEX IF NOT EXISTS payment_ledger_one_entry_per_provider_ref
    ON payment_ledger (wallet_id, provider_ref) WHERE provider_ref <> '';

CREATE TABLE IF NOT EXISTS payment_topups (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    branch_id UUID NOT NULL,
    wallet_id UUID NOT NULL REFERENCES payment_wallets(id) ON DELETE CASCADE,
    appointment_id UUID REFERENCES booking_appointments(id) ON DELETE SET NULL,
    amount_centavos INTEGER NOT NULL,
    provider TEXT NOT NULL DEFAULT 'paymongo',
    provider_session_id TEXT UNIQUE,
    checkout_url TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    paid_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS payment_branch_services (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL,
    branch_id UUID NOT NULL REFERENCES tenant_branches(id) ON DELETE CASCADE,
    appointment_type_id UUID NOT NULL REFERENCES booking_appointment_types(id) ON DELETE RESTRICT,
    price_centavos INTEGER NOT NULL DEFAULT 0,
    is_offered BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ,
    UNIQUE (branch_id, appointment_type_id)
);

CREATE TABLE IF NOT EXISTS payment_branch_settings (
    branch_id UUID PRIMARY KEY REFERENCES tenant_branches(id) ON DELETE CASCADE,
    tenant_id UUID NOT NULL,
    payment_mode TEXT NOT NULL DEFAULT 'upfront',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ
);

-- Snapshot the agreed price onto each appointment at booking time.
ALTER TABLE booking_appointments
    ADD COLUMN IF NOT EXISTS price_centavos INTEGER NOT NULL DEFAULT 0;
