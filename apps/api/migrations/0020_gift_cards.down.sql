-- Phase 18: Gift Cards (down) — local dev rollback only, never for the live DB.
DROP TABLE gift_cards;
ALTER TABLE carts  DROP COLUMN IF EXISTS gift_card_code;
ALTER TABLE orders DROP COLUMN IF EXISTS gift_card_code,
                   DROP COLUMN IF EXISTS gift_card_cents;