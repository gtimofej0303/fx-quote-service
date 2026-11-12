package frankfurter

import (
	"encoding/json"
	"testing"
)

// TestParseJSONDecimalExactAndScientific checks exact decimal values and numbers such as 1e2.
func TestParseJSONDecimalExactAndScientific(t *testing.T) {
	cases := []struct {
		raw, want string
	}{
		{"61.960000", "61.96"},
		{"1.234567890123456789", "1.234567890123456789"},
		{"0.000000000000000001", "0.000000000000000001"},
		{"1e-8", "0.00000001"},
		{"1.23E+3", "1230"},
		{"0.001e4", "10"},
		{"100.0e-2", "1"},
		{"1e+19", "10000000000000000000"},
		{"1e-18", "0.000000000000000001"},
		{"100000000000000000000e-20", "1"},
		{"0.10000000000000000000000", "0.1"},
	}

	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			price, err := parseJSONDecimal(json.RawMessage(tc.raw))

			if err != nil || price.String() != tc.want {
				t.Fatalf("parse(%s) = %s, %v; want %s", tc.raw, price.String(), err, tc.want)
			}
		})
	}
}

// TestParseJSONDecimalInvalid checks that invalid or too large numbers are rejected.
func TestParseJSONDecimalInvalid(t *testing.T) {
	cases := []string{
		"", "null", "true", `"1.2"`, "0", "-1.2", "1e-19",
		"0.1234567890123456789", "1e+20", "1e999999999999999999", "1e-300", "01", "1.",
		"1e-1e1", "1e+999", "2.3.4", "0.00000000000000000000000000000000",
	}

	for _, tc := range cases {
		t.Run(tc, func(t *testing.T) {
			if p, err := parseJSONDecimal(json.RawMessage(tc)); err == nil {
				t.Fatalf("unexpected valid price %s", p.String())
			}
		})
	}
}

// TestParseRateResponseRequiresExactDirectionAndFields checks required rate fields and the currency order.
func TestParseRateResponseRequiresExactDirectionAndFields(t *testing.T) {
	data := []byte(`{"date":"2026-11-06","base":"USD","quote":"EUR","rate":0.9}`)

	if _, _, err := parseRateResponse(data, "EUR", "USD"); err == nil {
		t.Fatal("wrong direction accepted")
	}

	p, d, err := parseRateResponse(data, "USD", "EUR")
	if err != nil || p.String() != "0.9" || d.String() != "2026-11-06" {
		t.Fatalf("wrong rate: %s, %s, %v", p.String(), d.String(), err)
	}
}
