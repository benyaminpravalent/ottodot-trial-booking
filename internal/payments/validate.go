package payments

import (
	"regexp"
	"strings"
	"time"
)

var digitsOnly = regexp.MustCompile(`^[0-9]+$`)

// Normalize strips spaces from the card number.
func Normalize(c Card) Card {
	c.Number = strings.ReplaceAll(c.Number, " ", "")
	return c
}

// Validate checks the *shape* of a card (not whether it will be accepted) and returns
// one human-readable problem per bad field. Used by both the JSON API and the HTML form,
// so the boundary rules live in exactly one place.
func Validate(c Card, now time.Time) []string {
	var problems []string
	if len(c.Number) < 12 || len(c.Number) > 19 || !digitsOnly.MatchString(c.Number) {
		problems = append(problems, "card.number: must be 12-19 digits")
	}
	if c.ExpMonth < 1 || c.ExpMonth > 12 {
		problems = append(problems, "card.exp_month: must be 1-12")
	}
	if c.ExpYear < 2000 || c.ExpYear > 2100 {
		problems = append(problems, "card.exp_year: must be a four-digit year")
	} else if c.ExpMonth >= 1 && c.ExpMonth <= 12 {
		// A card is valid through the end of its expiry month.
		expiry := time.Date(c.ExpYear, time.Month(c.ExpMonth)+1, 1, 0, 0, 0, 0, time.UTC)
		if !now.Before(expiry) {
			problems = append(problems, "card.exp_year: card has expired")
		}
	}
	if len(c.CVC) < 3 || len(c.CVC) > 4 || !digitsOnly.MatchString(c.CVC) {
		problems = append(problems, "card.cvc: must be 3-4 digits")
	}
	return problems
}
