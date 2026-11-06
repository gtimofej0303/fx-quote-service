package quote

import (
	"errors"
	"testing"
)

func TestParseCurrencyPairAllDirections(t *testing.T) {
	tests := []struct {
		input string
		want  CurrencyPair
	}{
		{"usd/eur", PairUSDEUR},
		{" USD/EGP ", PairUSDEGP},
		{"eur/usd", PairEURUSD},
		{"EUR/EGP", PairEUREGP},
		{"egp/usd", PairEGPUSD},
		{"\tEgP/Eur\n", PairEGPEUR},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseCurrencyPair(tt.input)
			if err != nil || got != tt.want || got.String() != string(tt.want) {
				t.Fatalf("ParseCurrencyPair(%q) = %q, %v; want %q", tt.input, got, err, tt.want)
			}
		})
	}
}

func TestParseCurrencyPairErrors(t *testing.T) {
	tests := []struct {
		input string
		want  error
	}{
		{"", ErrInvalidPair},
		{"USD-EUR", ErrInvalidPair},
		{"US/EUR", ErrInvalidPair},
		{"USD/ EUR", ErrInvalidPair},
		{"USD/€UR", ErrInvalidPair},
		{"UſD/EUR", ErrInvalidPair},
		{"USD/E2R", ErrInvalidPair},
		{"EUR/MXN", ErrUnsupportedPair},
		{"USD/USD", ErrUnsupportedPair},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseCurrencyPair(tt.input)
			if got != "" || !errors.Is(err, tt.want) {
				t.Fatalf("ParseCurrencyPair(%q) = %q, %v; want %v", tt.input, got, err, tt.want)
			}
		})
	}

	if err := CurrencyPair("usd/eur").Validate(); !errors.Is(err, ErrInvalidPair) {
		t.Fatalf("noncanonical pair validation = %v", err)
	}

	if err := (CurrencyPair("")).Validate(); !errors.Is(err, ErrInvalidPair) {
		t.Fatalf("zero pair validation = %v", err)
	}
}
