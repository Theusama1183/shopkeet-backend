-- Phase 10: Discounts (rollback).
ALTER TABLE orders DROP COLUMN discount_cents;
ALTER TABLE orders DROP COLUMN discount_code;
ALTER TABLE carts DROP COLUMN discount_code;

DROP TABLE discounts;