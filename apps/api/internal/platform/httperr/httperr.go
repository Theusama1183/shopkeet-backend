package httperr

import (
	"errors"

	"github.com/gofiber/fiber/v2"
)

// E is the typed error the whole API returns. Handlers return *E instead of
// writing `{"error": [...]}` inline; the Fiber error handler (Handler) is the
// single writer of the documented error shape
// (docs/api-reference.md §Error shape):
//
//	{ "error": { "code": "product_not_found", "message": "..." } }
type E struct {
	Status  int
	Code    string
	Message string
}

func (e *E) Error() string { return e.Message }

// New builds an *E from parts.
func New(status int, code, message string) *E {
	return &E{Status: status, Code: code, Message: message}
}

// Common constructors. Codes are tiny stable snake strings (they become the
// machine-readable `code` field clients can branch on); messages are the
// human copy for the storefront/admin UX.
func BadRequest(code, message string) *E { return New(fiber.StatusBadRequest, code, message) }
func Unauthorized(code, message string) *E {
	return New(fiber.StatusUnauthorized, code, message)
}
func Forbidden(code, message string) *E { return New(fiber.StatusForbidden, code, message) }
func NotFound(code, message string) *E  { return New(fiber.StatusNotFound, code, message) }
func Conflict(code, message string) *E  { return New(fiber.StatusConflict, code, message) }

// Internal builds the generic 500. ErrInternalServerError is the zero-effort
// 500 a handler can `return` when it has no better shape for the failure.
func Internal(message string) *E {
	return New(fiber.StatusInternalServerError, "internal_error", message)
}

// C builds an *E whose machine code is derived from the status (used by the
// bulk Phase 7 migration of legacy inline error responses). Prefer the labeled
// constructors when a stable, resource-specific code matters for clients.
func C(status int, message string) *E { return New(status, codeFor(status), message) }

func codeFor(status int) string {
	switch status {
	case fiber.StatusBadRequest:
		return "invalid_request"
	case fiber.StatusUnauthorized:
		return "unauthorized"
	case fiber.StatusForbidden:
		return "forbidden"
	case fiber.StatusNotFound:
		return "not_found"
	case fiber.StatusConflict:
		return "conflict"
	case fiber.StatusBadGateway:
		return "upstream_error"
	default:
		return "internal_error"
	}
}

// ErrInternalServerError mirrors fiber's sentinel but as *E so call sites can
// keep their existing `return ErrInternalServerError` style. It is a package
// var (not a const) because fiber uses an interface for its sentinel.
var ErrInternalServerError = Internal("internal server error")

// Handler is the Fiber error handler. Every error reaching the boundary — *E,
// *fiber.Error, a generic panic-recovered error — is serialized as the standard
// shape, so no raw stack trace or framework default error escapes to a client.
func Handler(c *fiber.Ctx, err error) error {
	var e *E
	if errors.As(err, &e) {
		return c.Status(e.Status).JSON(fiber.Map{
			"error": fiber.Map{"code": e.Code, "message": e.Message},
		})
	}

	var fe *fiber.Error
	if errors.As(err, &fe) {
		return c.Status(fe.Code).JSON(fiber.Map{
			"error": fiber.Map{"code": "request_error", "message": fe.Message},
		})
	}

	c.Set("Content-Type", "application/json")
	return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
		"error": fiber.Map{"code": "internal_error", "message": "internal server error"},
	})
}
