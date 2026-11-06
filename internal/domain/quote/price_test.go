package quote

import (
	"errors"
	"testing"
)

func TestParsePriceValidAndCanonical(t *testing.T) {
	tests := []struct{ input, want string }{
		{"61.9600", "61.96"},
		{"1.0000000000000000000", "1"},
		{"0.000000000000000001", "0.000000000000000001"},
		{"99999999999999999999.999999999999999999", "99999999999999999999.999999999999999999"},
		{"1", "1"},
		{"0.500", "0.5"},
	}
	for _, tt := range tests {
		p, err := ParsePrice(tt.input)
		if err != nil || p.String() != tt.want || p.Validate() != nil {
			t.Errorf("ParsePrice(%q) = %q, %v; want %q", tt.input, p, err, tt.want)
		}
	}
}

func TestParsePriceRejectsInvalidOrUnrepresentable(t *testing.T) {
	inputs := []string{
		"", "0", "0.000000000000000000", "-1", "+1", "NaN", "Inf", "1e-3",
		"01.2", ".5", "1.", "1.2.3", " 1.2", "1,2", "123456789012345678901",
		"0.0000000000000000001", "1.1234567890123456789", "1.2\n",
	}

	for _, raw := range inputs {
		if _, err := ParsePrice(raw); !errors.Is(err, ErrInvalidPrice) {
			t.Errorf("ParsePrice(%q) = %v, want ErrInvalidPrice", raw, err)
		}
	}

	if err := (Price{}).Validate(); !errors.Is(err, ErrInvalidPrice) {
		t.Errorf("zero Price should be invalid: %v", err)
	}
}
