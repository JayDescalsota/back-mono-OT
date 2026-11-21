-- Page module catalog for the access-management UI (replaces the former
-- hardcoded frontend list). Modules are global, not per-branch.
CREATE TABLE IF NOT EXISTS modules (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    resource TEXT NOT NULL UNIQUE,
    label TEXT NOT NULL,
    parent_resource TEXT NULL REFERENCES modules(resource) ON DELETE CASCADE,
    sort_order INTEGER NOT NULL DEFAULT 0,
    max_level TEXT NULL,
    is_active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ
);

-- Top-level modules first (children reference them)
INSERT INTO modules (resource, label, parent_resource, sort_order, max_level) VALUES
    ('dashboard', 'Dashboard', NULL, 10, 'read'),
    ('patients', 'Patients', NULL, 20, NULL),
    ('patient-detail', 'Patient Detail', NULL, 30, NULL),
    ('schedule', 'Schedule', NULL, 80, NULL),
    ('assessments', 'Assessments', NULL, 85, NULL),
    ('messaging', 'Messaging', NULL, 90, NULL),
    ('billing', 'Billing', NULL, 100, NULL),
    ('referrals', 'Referrals', NULL, 110, NULL),
    ('staff', 'Staff', NULL, 120, NULL),
    ('admin', 'Admin', NULL, 130, NULL),
    ('settings', 'Settings', NULL, 190, NULL)
ON CONFLICT (resource) DO NOTHING;

-- Child modules
INSERT INTO modules (resource, label, parent_resource, sort_order, max_level) VALUES
    ('patient-detail.soap', 'SOAP', 'patient-detail', 40, NULL),
    ('patient-detail.goals', 'Goals', 'patient-detail', 50, NULL),
    ('patient-detail.treatment_plan', 'Treatment Plan', 'patient-detail', 60, NULL),
    ('patient-detail.assessments', 'Assessments', 'patient-detail', 70, NULL),
    ('admin.branch-profile', 'Branch Profile', 'admin', 140, NULL),
    ('admin.branch-address', 'Branch Address', 'admin', 150, NULL),
    ('admin.access-management', 'Access Management', 'admin', 160, NULL),
    ('admin.user-management', 'User Management', 'admin', 170, NULL),
    ('admin.appearance', 'Appearance', 'admin', 180, NULL)
ON CONFLICT (resource) DO NOTHING;
