INSERT INTO modules (resource, label, parent_resource, sort_order, max_level) VALUES
    ('payments', 'Payments', NULL, 95, NULL)
ON CONFLICT (resource) DO NOTHING;
