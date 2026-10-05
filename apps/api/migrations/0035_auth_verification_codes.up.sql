-- Account verification codes: the email OTP that gates signup/login, and the
-- single-use token behind password reset.
--
-- Account-scoped, deliberately RLS-free — same posture and same reasoning as
-- `merchant_accounts` (migration 0034): at OTP/reset time no tenant scope exists
-- in the request, and the row carries no merchant business data, only a hash.
-- It is reachable solely through the auth endpoints themselves, which require
-- either a just-verified password (login/signup issue the code) or the emailed
-- secret (verify/reset consume it).
--
-- Only the SHA-256 of the code/token is stored: a database read never exposes a
-- live credential. One live row per (email, purpose) is maintained by the API —
-- issuing a new code consumes the predecessor — and `attempts` gives each code
-- a bounded number of guesses independent of any per-IP rate limit.
CREATE TABLE auth_verification_codes (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  account_id  UUID NOT NULL REFERENCES merchant_accounts(id) ON DELETE CASCADE,
  email       TEXT NOT NULL,              -- lower(email); the lookup identity
  purpose     TEXT NOT NULL,              -- 'otp' | 'password_reset'
  code_hash   TEXT NOT NULL,              -- sha256 hex of the code/token
  expires_at  TIMESTAMPTZ NOT NULL,
  attempts    INT NOT NULL DEFAULT 0,     -- failed guesses against THIS code
  consumed_at TIMESTAMPTZ,                -- set on success (or on too many tries)
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (purpose IN ('otp', 'password_reset'))
);

-- Serves every auth lookup: live-code fetch, resend guard, per-hour attempt
-- sum, and the per-email cleanup delete. One account has a handful of rows.
CREATE INDEX auth_verification_codes_lookup_idx
  ON auth_verification_codes (email, purpose, created_at DESC);
