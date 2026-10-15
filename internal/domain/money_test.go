package domain

import (
	"testing"
)

func TestParseCurrency(t *testing.T) {
	for _, code := range []string{"ETB", "USD", "EUR"} {
		c, err := ParseCurrency(code)
		if err != nil || string(c) != code {
			t.Errorf("ParseCurrency(%q) = %q, %v; want supported", code, c, err)
		}
	}
	for _, code := range []string{"", "etb", "GBP", "US", "USDD", " USD"} {
		if _, err := ParseCurrency(code); err == nil {
			t.Errorf("ParseCurrency(%q) succeeded, want error", code)
		}
	}
}

func TestParseMoney(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"0", 0, false},
		{"0.00", 0, false},
		{"0.0", 0, false},
		{"1", 100, false},
		{"100.50", 10050, false},
		{"100.5", 10050, false},
		{"0.01", 1, false},
		{"007.10", 710, false},
		{"92233720368547758.07", 9223372036854775807, false},

		{"92233720368547758.08", 0, true}, // one minor unit past int64
		{"99999999999999999999", 0, true},
		{"1.001", 0, true}, // excess precision is rejected, never rounded
		{"0.000", 0, true},
		{"-1.00", 0, true},
		{"+1.00", 0, true},
		{"1e2", 0, true},
		{"1E2", 0, true},
		{" 1.00", 0, true},
		{"1.00 ", 0, true},
		{"1 000", 0, true},
		{"", 0, true},
		{".", 0, true},
		{".50", 0, true},
		{"1.", 0, true},
		{"1.2.3", 0, true},
		{"1,00", 0, true},
		{"abc", 0, true},
		{"١٢", 0, true}, // non-ASCII digits
		{"0x10", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			m, err := ParseMoney(tc.in, ETB)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseMoney(%q) = %d, want error", tc.in, m.Minor)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseMoney(%q): %v", tc.in, err)
			}
			if m.Minor != tc.want || m.Currency != ETB {
				t.Errorf("ParseMoney(%q) = %+v, want %d ETB", tc.in, m, tc.want)
			}
		})
	}
}

func TestMoneyString(t *testing.T) {
	cases := []struct {
		minor int64
		want  string
	}{
		{0, "0.00"},
		{1, "0.01"},
		{10, "0.10"},
		{100, "1.00"},
		{10050, "100.50"},
		{9223372036854775807, "92233720368547758.07"},
	}
	for _, tc := range cases {
		if got := (Money{Minor: tc.minor, Currency: USD}).String(); got != tc.want {
			t.Errorf("Money{%d}.String() = %q, want %q", tc.minor, got, tc.want)
		}
	}
}

func TestMoneyRoundTrips(t *testing.T) {
	for _, s := range []string{"0.00", "0.01", "12.34", "92233720368547758.07"} {
		m, err := ParseMoney(s, EUR)
		if err != nil {
			t.Fatalf("ParseMoney(%q): %v", s, err)
		}
		if got := m.String(); got != s {
			t.Errorf("round trip %q -> %q", s, got)
		}
	}
}
