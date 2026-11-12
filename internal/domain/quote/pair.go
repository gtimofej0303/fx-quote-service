package quote

import (
	"fmt"
	"strings"
)

// CurrencyPair uses A/B: the amount of B for one unit of A.
// Only uppercase values from ParseCurrencyPair are valid.
type CurrencyPair string

const (
	PairUSDEUR CurrencyPair = "USD/EUR"
	PairUSDEGP CurrencyPair = "USD/EGP"
	PairEURUSD CurrencyPair = "EUR/USD"
	PairEUREGP CurrencyPair = "EUR/EGP"
	PairEGPUSD CurrencyPair = "EGP/USD"
	PairEGPEUR CurrencyPair = "EGP/EUR"
)

// Parse and format the pair
func ParseCurrencyPair(raw string) (CurrencyPair, error) {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) != 7 || trimmed[3] != '/' {
		return "", fmt.Errorf("%w: expected AAA/BBB", ErrInvalidPair)
	}

	for i := 0; i < len(trimmed); i++ {
		if i == 3 {
			continue
		}
		c := trimmed[i]
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')) {
			return "", fmt.Errorf("%w: expected ASCII currency codes", ErrInvalidPair)
		}
	}

	canonical := strings.ToUpper(trimmed)
	pair := CurrencyPair(canonical)
	if err := pair.Validate(); err != nil {
		return "", err
	}

	return pair, nil
}

func (p CurrencyPair) String() string { return string(p) }

func (p CurrencyPair) Validate() error {
	value := string(p)

	if len(value) != 7 || value[3] != '/' {
		return fmt.Errorf("%w: expected AAA/BBB", ErrInvalidPair)
	}

	for i := 0; i < len(value); i++ {
		if i == 3 {
			continue
		}
		if value[i] < 'A' || value[i] > 'Z' {
			return fmt.Errorf("%w: expected ASCII uppercase currency codes", ErrInvalidPair)
		}
	}

	switch p {
	case PairUSDEUR, PairUSDEGP, PairEURUSD, PairEUREGP, PairEGPUSD, PairEGPEUR:
		return nil
	default:
		return fmt.Errorf("%w: %s", ErrUnsupportedPair, value)
	}
}
