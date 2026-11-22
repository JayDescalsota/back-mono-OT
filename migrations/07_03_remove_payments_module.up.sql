-- Service pricing now lives under the Admin module, so the dedicated
-- "payments" module entry is no longer needed.
DELETE FROM tenant_role_permissions trp
USING tenant_permissions p
WHERE p.id = trp.permission_id AND p.resource = 'payments';

DELETE FROM tenant_permissions WHERE resource = 'payments';
DELETE FROM modules WHERE resource = 'payments';
