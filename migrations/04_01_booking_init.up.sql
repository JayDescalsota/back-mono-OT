CREATE TABLE booking_branch_hours (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    branch_id UUID NOT NULL,
    recurrence_rule TEXT NOT NULL,
    open_time TIMESTAMPTZ NOT NULL,
    close_time TIMESTAMPTZ NOT NULL,
    effective_from TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    effective_until TIMESTAMPTZ,
    is_active BOOLEAN NOT NULL DEFAULT true,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_by UUID,
    created_action TEXT NOT NULL DEFAULT '',
    updated_by UUID,
    updated_action TEXT NOT NULL DEFAULT ''
);

CREATE TABLE booking_practitioner_branches (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    practitioner_id UUID NOT NULL,
    branch_id UUID NOT NULL,
    role TEXT,
    is_primary BOOLEAN NOT NULL DEFAULT false,
    is_active BOOLEAN NOT NULL DEFAULT true,
    joined_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_by UUID,
    created_action TEXT NOT NULL DEFAULT '',
    updated_by UUID,
    updated_action TEXT NOT NULL DEFAULT ''
);

CREATE TABLE booking_practitioner_availabilities (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    practitioner_id UUID NOT NULL,
    recurrence_rule TEXT,
    available_from TIMESTAMPTZ NOT NULL,
    available_to TIMESTAMPTZ NOT NULL,
    effective_from TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    effective_until TIMESTAMPTZ,
    status TEXT NOT NULL DEFAULT 'ACTIVE',
    is_active BOOLEAN NOT NULL DEFAULT true,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_by UUID,
    created_action TEXT NOT NULL DEFAULT '',
    updated_by UUID,
    updated_action TEXT NOT NULL DEFAULT ''
);

CREATE TABLE booking_schedule_templates (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    branch_id UUID NOT NULL,
    practitioner_id UUID,
    recurrence_rule TEXT NOT NULL,
    start_time TIMESTAMPTZ NOT NULL,
    end_time TIMESTAMPTZ NOT NULL,
    slot_duration INT NOT NULL DEFAULT 30,
    buffer_duration INT NOT NULL DEFAULT 0,
    capacity INT NOT NULL DEFAULT 1,
    booked_count INT NOT NULL DEFAULT 0,
    effective_from TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    effective_until TIMESTAMPTZ,
    status TEXT NOT NULL DEFAULT 'ACTIVE',
    is_active BOOLEAN NOT NULL DEFAULT true,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_by UUID,
    created_action TEXT NOT NULL DEFAULT '',
    updated_by UUID,
    updated_action TEXT NOT NULL DEFAULT ''
);

CREATE TABLE booking_appointment_slots (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    schedule_template_id UUID NOT NULL,
    branch_id UUID NOT NULL,
    practitioner_id UUID,
    start_at TIMESTAMPTZ NOT NULL,
    end_at TIMESTAMPTZ NOT NULL,
    capacity INT NOT NULL DEFAULT 1,
    booked_count INT NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'AVAILABLE',
    generated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    is_active BOOLEAN NOT NULL DEFAULT true,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_by UUID,
    created_action TEXT NOT NULL DEFAULT '',
    updated_by UUID,
    updated_action TEXT NOT NULL DEFAULT ''
);

CREATE TABLE booking_appointments (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    appointment_slot_id UUID NOT NULL,
    branch_id UUID NOT NULL,
    patient_id UUID,
    practitioner_id UUID,
    start_at TIMESTAMPTZ NOT NULL,
    end_at TIMESTAMPTZ NOT NULL,
    status TEXT NOT NULL DEFAULT 'PENDING',
    notes TEXT NOT NULL DEFAULT '',

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_by UUID,
    created_action TEXT NOT NULL DEFAULT '',
    updated_by UUID,
    updated_action TEXT NOT NULL DEFAULT ''
);

CREATE TABLE booking_schedule_exceptions (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    schedule_template_id UUID,
    appointment_slot_id UUID,
    practitioner_id UUID,
    type TEXT NOT NULL DEFAULT 'OVERRIDE',
    reason TEXT NOT NULL DEFAULT '',
    start_at TIMESTAMPTZ NOT NULL,
    end_at TIMESTAMPTZ NOT NULL,
    status TEXT NOT NULL DEFAULT 'ACTIVE',
    is_active BOOLEAN NOT NULL DEFAULT true,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_by UUID,
    created_action TEXT NOT NULL DEFAULT '',
    updated_by UUID,
    updated_action TEXT NOT NULL DEFAULT ''
);


ALTER TABLE booking_appointments ALTER COLUMN appointment_slot_id DROP NOT NULL;

ALTER TABLE booking_appointments ADD COLUMN cancellation_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE booking_appointments ADD COLUMN cancelled_at TIMESTAMPTZ;
ALTER TABLE booking_appointments ADD COLUMN rescheduled_from_id UUID REFERENCES booking_appointments(id);
ALTER TABLE booking_appointments ADD COLUMN rescheduled_to_id UUID REFERENCES booking_appointments(id);
ALTER TABLE booking_appointments ADD COLUMN reschedule_reason TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS booking_appointment_goals (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID,
    appointment_id UUID NOT NULL REFERENCES booking_appointments(id) ON DELETE CASCADE,
    goal_id UUID NOT NULL,
    progress INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'Not Started',
    notes TEXT,
    is_active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by UUID,
    created_action TEXT,
    updated_by UUID,
    updated_action TEXT
);

CREATE INDEX idx_booking_appointment_goals_appointment ON booking_appointment_goals (appointment_id);
CREATE INDEX idx_booking_appointment_goals_goal ON booking_appointment_goals (goal_id);
CREATE INDEX idx_booking_appointment_goals_active ON booking_appointment_goals (is_active);

CREATE TABLE IF NOT EXISTS booking_soap_notes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID,
    appointment_id UUID NOT NULL UNIQUE REFERENCES booking_appointments(id) ON DELETE CASCADE,
    subjective TEXT,
    objective TEXT,
    assessment TEXT,
    plan TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by UUID,
    created_action TEXT,
    updated_by UUID,
    updated_action TEXT
);

CREATE INDEX idx_booking_soap_notes_appointment ON booking_soap_notes (appointment_id);

-- Date-specific branch hours (holidays, special openings). One row per
-- branch/date; overrides the weekly branch hours for that day.
CREATE TABLE IF NOT EXISTS booking_branch_special_hours (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    branch_id UUID NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    is_yearly BOOLEAN NOT NULL DEFAULT false,
    special_date DATE NOT NULL,
    is_closed BOOLEAN NOT NULL DEFAULT false,
    open_time TIMESTAMPTZ,
    close_time TIMESTAMPTZ,
    is_active BOOLEAN NOT NULL DEFAULT true,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_by UUID,
    created_action TEXT NOT NULL DEFAULT '',
    updated_by UUID,
    updated_action TEXT NOT NULL DEFAULT '',

    UNIQUE (branch_id, special_date)
);

CREATE INDEX idx_booking_branch_special_hours_branch ON booking_branch_special_hours (branch_id, special_date);

-- Appointment type lookup for the New Appointment modal.
CREATE TABLE booking_appointment_types (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    attendees TEXT NOT NULL DEFAULT '',
    typical_duration TEXT NOT NULL DEFAULT '',
    scheduling_notes TEXT NOT NULL DEFAULT '',
    discipline_mode TEXT NOT NULL DEFAULT 'hidden',
    sort_order INTEGER NOT NULL DEFAULT 0,
    is_active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ
);

-- Therapy discipline lookup shared by all appointment types.
CREATE TABLE booking_disciplines (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    sort_order INTEGER NOT NULL DEFAULT 0,
    is_active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ
);

-- Which disciplines each appointment type offers. Disciplines keep their
-- single stable IDs; this table only links sets to types.
CREATE TABLE booking_appointment_type_disciplines (
    appointment_type_id UUID NOT NULL REFERENCES booking_appointment_types(id) ON DELETE CASCADE,
    discipline_id UUID NOT NULL REFERENCES booking_disciplines(id) ON DELETE CASCADE,
    PRIMARY KEY (appointment_type_id, discipline_id)
);

ALTER TABLE booking_appointments
    ADD COLUMN appointment_type_id UUID REFERENCES booking_appointment_types(id) ON DELETE SET NULL;
ALTER TABLE booking_appointments
    ADD COLUMN discipline_id UUID REFERENCES booking_disciplines(id) ON DELETE SET NULL;

