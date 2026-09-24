// Package payments abstracts how an order is charged. v1 ships exactly one
// provider — Cash on Delivery (no external gateway calls, no keys). The
// interface is the seam a real gateway slots into without touching the orders
// module (docs/04-agent-build-spec.md "Forward compatibility").
package payments

import (
	"context"
)

// ProcessRequest describes the payment being taken at checkout.
type ProcessRequest struct {
	Method      string
	AmountCents int
	Currency    string
}

// ProcessResult tells checkout what payment_status the order should carry.
type ProcessResult struct {
	// PaymentStatus is the order's initial payment_status ("pending" for COD;
	// a card gateway would return "paid").
	PaymentStatus string
}

// Provider charges an order. Provider implementations must not be the source
// of truth for money: order rows in Postgres are; a provider only sets the
// initial payment_status.
type Provider interface {
	// Name is the payment_method value stored on the order.
	Name() string
	Process(ctx context.Context, req ProcessRequest) (ProcessResult, error)
}

// CODProvider accepts the order with payment_status "pending"; payment happens
// on delivery (PATCH /orders/:id/status → delivered sets it to "paid").
type CODProvider struct{}

func (CODProvider) Name() string { return "cod" }

func (p CODProvider) Process(_ context.Context, _ ProcessRequest) (ProcessResult, error) {
	return ProcessResult{PaymentStatus: "pending"}, nil
}

// Registry maps payment_method names to providers. v1 ships `cod` only.
type Registry struct {
	providers map[string]Provider
}

// NewRegistry builds a registry seeded with the COD provider.
func NewRegistry() *Registry {
	providers := map[string]Provider{}
	for _, p := range []Provider{CODProvider{}} {
		providers[p.Name()] = p
	}
	return &Registry{providers: providers}
}

// Get returns the provider registered under name.
func (r *Registry) Get(name string) (Provider, bool) {
	p, ok := r.providers[name]
	return p, ok
}

// Methods lists the registered payment method names.
func (r *Registry) Methods() []string {
	names := make([]string, 0, len(r.providers))
	for n := range r.providers {
		names = append(names, n)
	}
	return names
}
