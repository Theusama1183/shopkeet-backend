-- Phase 21 rollback: drop the automatic-discount surface. NULL codes (only auto
-- discounts) are given a synthetic value so the NOT NULL constraint re-applies.

UPDATE discounts
SET code = 'AUTO-' || left(replace(id::text, '-', ''), 8)
WHERE code IS NULL;

ALTER TABLE discounts
  DROP CONSTRAINT discounts_applies_to_check,
  ALTER COLUMN code SET NOT NULL,
  DROP COLUMN applies_to,
  DROP COLUMN buy_quantity,
  DROP COLUMN get_quantity,
  DROP COLUMN requires_code;