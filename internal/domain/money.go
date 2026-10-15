package domain

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Currency is a supported ISO-4217 code.
type Currency string

const (
	ETB Currency = "ETB"
	USD Currency = "USD"
	EUR Currency = "EUR"
)

// minorDigits is the single table of supported currencies and how many minor
// units make one major unit. Adding a currency is a one-line change here.
var minorDigits = map[Currency]int{
	ETB: 2,
	USD: 2,
	EUR: 2,
}

// ErrUnsupportedCurrency is returned for any code missing from the table.
var ErrUnsupportedCurrency = errors.New("is not a supported currency")

// ParseCurrency accepts an exact, upper-case supported code.
func ParseCurrency(code string) (Currency, error) {
	c := Currency(code)
	if _, ok := minorDigits[c]; !ok {
		return "", ErrUnsupportedCurrency
	}
	return c, nil
}

// MinorDigits is how many decimal places an amount in c may carry.
func (c Currency) MinorDigits() int { return minorDigits[c] }

// Money is an amount in a Currency, held as an integer count of minor units.
// Floats never touch money.
type Money struct {
	Minor    int64
	Currency Currency
}

// Errors from ParseMoney. Their text is safe to show to API clients as a
// field-level message.
var (
	ErrMalformedAmount = errors.New("must be a non-negative decimal number such as 100.50")
	ErrAmountTooLarge  = errors.New("is too large")
)

// ParseMoney converts a plain decimal string ("100.50") into minor units of c.
// It accepts only ASCII digits with an optional fractional part of 1 to
// c.MinorDigits() digits. Signs, exponents, whitespace, separators, and excess
// precision are rejected rather than rounded; so is anything beyond int64.
func ParseMoney(s string, c Currency) (Money, error) {
	digits := c.MinorDigits()
	whole, frac, hasPoint := strings.Cut(s, ".")
	if !allDigits(whole) || (hasPoint && !allDigits(frac)) {
		return Money{}, ErrMalformedAmount
	}
	if len(frac) > digits {
		return Money{}, fmt.Errorf("must have at most %d decimal places", digits)
	}

	scale := pow10(digits)
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || w > math.MaxInt64/scale {
		return Money{}, ErrAmountTooLarge
	}
	var f int64
	if frac != "" {
		f, _ = strconv.ParseInt(frac+strings.Repeat("0", digits-len(frac)), 10, 64)
	}
	if w*scale > math.MaxInt64-f {
		return Money{}, ErrAmountTooLarge
	}
	return Money{Minor: w*scale + f, Currency: c}, nil
}

// String formats m as a decimal string with exactly c.MinorDigits() places.
func (m Money) String() string {
	digits := m.Currency.MinorDigits()
	s := strconv.FormatInt(m.Minor, 10)
	if digits == 0 {
		return s
	}
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	if len(s) <= digits {
		s = strings.Repeat("0", digits-len(s)+1) + s
	}
	s = s[:len(s)-digits] + "." + s[len(s)-digits:]
	if neg {
		s = "-" + s
	}
	return s
}

// allDigits reports whether s is non-empty and only ASCII digits.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func pow10(n int) int64 {
	p := int64(1)
	for range n {
		p *= 10
	}
	return p
}
