package idempotency

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/platform/httperr"
)

// Header is the request header a client sets to make an operation retry-safe.
// A retry with the same (tenant, endpoint, key) returns the stored response
// instead of running the handler a second time — e.g. a double-tapped
// "Place Order" produces exactly one order.
const Header = "Idempotency-Key"

// Retention is how long a completed idempotency response stays replayable. The
// Asynq purge task (Phase 14) sweeps rows older than this hourly.
const Retention = 24 * time.Hour

// Middleware guards a POST endpoint against duplicate execution keyed by the
// Idempotency-Key header. Call it AFTER the tenant-scoping middleware
// (TenantMW / PublicTenantMW / CustomerMW / CustomerOrGuestMW) because it
// runs on the request transaction it opens (c.Locals("tx")) — which is already
// RLS-scoped to app.current_tenant — and needs c.Locals("tenant_id"). Behavior:
//
//   - no Idempotency-Key header → pass through untouched;
//   - cached row for (tenant, endpoint, key) → replay stored response, skip
//     the handler;
//   - row exists but response not stored yet (still in flight) → 409;
//   - otherwise claim the key (INSERT ... ON CONFLICT DO NOTHING), run the
//     handler, and on a 2xx cache status+body in the same transaction; on any
//     other outcome the whole tx rolls back so a retry re-runs the handler.
//
// Because the claim and the handler are one transaction, a double-tapped
// "Place Order" can never create two orders: the second insertion conflict is
// seen inside the loser's transaction, which either 409s or replays.
func Middleware(endpoint string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		key := c.Get(Header)
		if key == "" {
			return c.Next()
		}
		tenantID, _ := c.Locals("tenant_id").(string)
		if tenantID == "" {
			return httperr.BadRequest("missing_tenant", "tenant not resolved before idempotency check")
		}
		tx, ok := c.Locals("tx").(pgx.Tx)
		if !ok {
			return httperr.Internal("idempotency_no_transaction")
		}

		ctx := c.Context()

		// 1. Claim the key inside the request tx. INSERT ... ON CONFLICT DO
		// NOTHING RETURNING id: a fresh claim returns the row; a conflicting
		// claim (already present, committed or in flight) returns no row.
		var claimedID string
		err := tx.QueryRow(ctx, `
			INSERT INTO idempotency_keys (tenant_id, endpoint, key)
			VALUES ($1, $2, $3)
			ON CONFLICT (tenant_id, endpoint, key) DO NOTHING
			RETURNING id`,
			tenantID, endpoint, key).Scan(&claimedID)
		if errors.Is(err, pgx.ErrNoRows) {
			// Conflict — some request already claimed this key. Replay it if a
			// response was cached, otherwise tell the caller it's still going.
			var status int
			var body []byte
			err := tx.QueryRow(ctx, `
				SELECT response_status, response_body
				FROM idempotency_keys
				WHERE tenant_id = $1 AND endpoint = $2 AND key = $3`,
				tenantID, endpoint, key).Scan(&status, &body)
			if err == nil {
				if status > 0 {
					return replay(c, status, body)
				}
				return httperr.Conflict("request_in_progress",
					"a request with this idempotency key is already being processed")
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return httperr.ErrInternalServerError
			}
			// Row vanished between claim and lookup (rolling back tx) — retry
			// the claim once.
			return retryClaim(ctx, tx, tenantID, endpoint, key)
		}
		if err != nil {
			return httperr.ErrInternalServerError
		}

		// 3. Run the handler. On 2xx store the response in the same tx; any
		// error unwinds it (rollback removes the claim), so a retry re-runs.
		if err := c.Next(); err != nil {
			return err
		}
		if s := c.Response().StatusCode(); s >= 200 && s < 300 {
			rb := c.Response().Body()
			if len(rb) == 0 {
				rb = []byte("{}")
			}
			if !json.Valid(rb) {
				rb = []byte("{}")
			}
			if _, err := tx.Exec(ctx, `
				UPDATE idempotency_keys
				SET response_status = $4, response_body = $5
				WHERE tenant_id = $1 AND endpoint = $2 AND key = $3`,
				tenantID, endpoint, key, s, rb); err != nil {
				return httperr.ErrInternalServerError
			}
		}
		return nil
	}
}

// replay serves a previously cached successful response verbatim.
func replay(c *fiber.Ctx, status int, body []byte) error {
	if status == 0 || len(body) == 0 {
		return c.Status(fiber.StatusOK).JSON(fiber.Map{})
	}
	c.Set(fiber.HeaderContentType, "application/json")
	return c.Status(status).Send(body)
}

// retryClaim re-inserts the key once after it vanished mid-flight (the
// competing transaction rolled back between our conflict detection and here).
func retryClaim(ctx context.Context, tx pgx.Tx, tenantID, endpoint, key string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO idempotency_keys (tenant_id, endpoint, key)
		VALUES ($1, $2, $3)
		ON CONFLICT (tenant_id, endpoint, key) DO NOTHING`,
		tenantID, endpoint, key)
	return err
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// PurgeExpired deletes idempotency rows older than Retention. The DELETE runs
// through the superuser-owned SECURITY DEFINER function purge_idempotency_keys
// (migration 0016) — FORCE RLS means no app-role query can sweep every tenant,
// so the sweep is the one contained, pinned-search-path exception. Returns the
// number of rows removed. On any error the change is rolled back.
func PurgeExpired(ctx context.Context, pool *pgxpool.Pool, before time.Time) (int64, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	var n int64
	if err := tx.QueryRow(ctx,
		`SELECT purge_idempotency_keys($1)`, before).Scan(&n); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return n, nil
}