ALTER TABLE booking_appointments DROP COLUMN IF EXISTS price_centavos;
DROP TABLE IF EXISTS payment_branch_settings;
DROP TABLE IF EXISTS payment_branch_services;
DROP TABLE IF EXISTS payment_topups;
DROP TABLE IF EXISTS payment_ledger;
DROP TABLE IF EXISTS payment_holds;
DROP TABLE IF EXISTS payment_wallets;
