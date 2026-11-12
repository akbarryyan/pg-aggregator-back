-- Migration 022: dashboard test payments marker.
-- The merchant Test Payment page now tags its payments with customer_name
-- 'TEST PAYMENT' (previously 'Dashboard test') and finds its history by that
-- name. Move the old rows to the new marker so they show up again.
-- Data only, idempotent, safe while the api is running.
BEGIN;

UPDATE payments SET customer_name = 'TEST PAYMENT' WHERE customer_name = 'Dashboard test';

COMMIT;
