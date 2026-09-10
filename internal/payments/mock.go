// Package payments defines the payment provider port and an in-process mock implementation.
package payments

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"
)

// Card is the minimal card shape the mock needs. Validation of the shape happens
// at the API boundary; the provider only decides the outcome.
type Card struct {
	Number   string
	ExpMonth int
	ExpYear  int
	CVC      string
}

// ChargeRequest is what the booking service asks the provider to do.
type ChargeRequest struct {
	AmountCents int
	Currency    string
	Card        Card
	// Delay lets tests and the race demo force a specific interleaving
	// ("User A's card network is slower than User B's"). Zero in production paths.
	Delay time.Duration
}

// ChargeResult is the provider's answer. Succeeded=false is a business outcome
// (declined card), not an error; errors are reserved for transport-level failures.
type ChargeResult struct {
	Succeeded   bool
	ProviderRef string // set when Succeeded
	FailureCode string // set when !Succeeded: card_declined | insufficient_funds | invalid_card
}

// RefundResult is returned by Refund.
type RefundResult struct {
	RefundRef string
}

// Provider is the port the booking service depends on.
type Provider interface {
	Charge(ctx context.Context, req ChargeRequest) (ChargeResult, error)
	Refund(ctx context.Context, providerRef string) (RefundResult, error)
}

// Test card numbers. Deterministic so the flow is demo-able and testable.
const (
	CardSuccess           = "4242424242424242"
	CardDeclined          = "4000000000000002"
	CardInsufficientFunds = "4000000000009995"
)

// Mock is a synchronous, deterministic, network-free provider.
type Mock struct{}

// NewMock returns the mock provider.
func NewMock() *Mock { return &Mock{} }

// Charge picks the outcome from the card number, optionally after req.Delay.
func (m *Mock) Charge(ctx context.Context, req ChargeRequest) (ChargeResult, error) {
	if req.Delay > 0 {
		select {
		case <-time.After(req.Delay):
		case <-ctx.Done():
			return ChargeResult{}, ctx.Err()
		}
	}
	switch req.Card.Number {
	case CardSuccess:
		return ChargeResult{Succeeded: true, ProviderRef: "mock_pay_" + randomID()}, nil
	case CardDeclined:
		return ChargeResult{FailureCode: "card_declined"}, nil
	case CardInsufficientFunds:
		return ChargeResult{FailureCode: "insufficient_funds"}, nil
	default:
		return ChargeResult{FailureCode: "invalid_card"}, nil
	}
}

// Refund always succeeds immediately.
func (m *Mock) Refund(_ context.Context, _ string) (RefundResult, error) {
	return RefundResult{RefundRef: "mock_refund_" + randomID()}, nil
}

func randomID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}
