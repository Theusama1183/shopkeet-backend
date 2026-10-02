-- Phase 27 down: affiliate program tables + the order link column.

ALTER TABLE orders DROP COLUMN IF EXISTS affiliate_code;

DROP TABLE IF EXISTS affiliate_payouts;
DROP TABLE IF EXISTS affiliate_commissions;
DROP TABLE IF EXISTS affiliates;