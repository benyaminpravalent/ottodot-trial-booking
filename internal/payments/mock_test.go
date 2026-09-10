package payments

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Pure unit tests: the provider is deterministic and has no I/O.

func TestMockChargeOutcomes(t *testing.T) {
	m := NewMock()
	cases := []struct {
		number      string
		wantOK      bool
		wantFailure string
	}{
		{CardSuccess, true, ""},
		{CardDeclined, false, "card_declined"},
		{CardInsufficientFunds, false, "insufficient_funds"},
		{"4111111111111111", false, "invalid_card"},
	}
	for _, tc := range cases {
		res, err := m.Charge(context.Background(), ChargeRequest{AmountCents: 2000, Currency: "SGD", Card: Card{Number: tc.number}})
		if err != nil {
			t.Fatalf("%s: %v", tc.number, err)
		}
		if res.Succeeded != tc.wantOK || res.FailureCode != tc.wantFailure {
			t.Errorf("%s: got ok=%v code=%q, want ok=%v code=%q", tc.number, res.Succeeded, res.FailureCode, tc.wantOK, tc.wantFailure)
		}
		if tc.wantOK && !strings.HasPrefix(res.ProviderRef, "mock_pay_") {
			t.Errorf("%s: provider ref = %q, want mock_pay_ prefix", tc.number, res.ProviderRef)
		}
	}
}

func TestMockDelayHonoursContext(t *testing.T) {
	m := NewMock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := m.Charge(ctx, ChargeRequest{Card: Card{Number: CardSuccess}, Delay: 5 * time.Second})
	if err == nil {
		t.Fatal("expected context deadline error when Delay exceeds the context")
	}
}

func TestMockRefund(t *testing.T) {
	res, err := NewMock().Refund(context.Background(), "mock_pay_abc")
	if err != nil || !strings.HasPrefix(res.RefundRef, "mock_refund_") {
		t.Fatalf("refund = %+v, %v", res, err)
	}
}

func TestValidate(t *testing.T) {
	now := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	good := Normalize(Card{Number: "4242 4242 4242 4242", ExpMonth: 12, ExpYear: 2030, CVC: "123"})
	if problems := Validate(good, now); len(problems) != 0 {
		t.Errorf("valid card rejected: %v", problems)
	}
	// Valid through the end of the expiry month.
	if problems := Validate(Card{Number: CardSuccess, ExpMonth: 9, ExpYear: 2026, CVC: "123"}, now); len(problems) != 0 {
		t.Errorf("card expiring this month rejected: %v", problems)
	}
	bad := Card{Number: "42", ExpMonth: 13, ExpYear: 19, CVC: "1"}
	if problems := Validate(bad, now); len(problems) != 4 {
		t.Errorf("want 4 problems, got %d: %v", len(problems), problems)
	}
	expired := Card{Number: CardSuccess, ExpMonth: 8, ExpYear: 2026, CVC: "123"}
	if problems := Validate(expired, now); len(problems) != 1 || !strings.Contains(problems[0], "expired") {
		t.Errorf("expired card: %v", problems)
	}
}
