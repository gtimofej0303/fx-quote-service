package quote

import (
	"fmt"
	"strings"
)

// Price is an exact, canonical positive decimal string. It deliberately does not use float64.
type Price struct {
	value string
}

// ParsePrice never rounds: unrepresentable scale or magnitude is an error.
// It also normalizes trailing fractional zeros, including values from SQL.
func ParsePrice(raw string) (Price, error) {
	parts := strings.Split(raw, ".")

	if len(parts) > 2 || len(parts[0]) == 0 || len(parts[0]) > 20 {
		return Price{}, fmt.Errorf("%w: invalid decimal representation", ErrInvalidPrice)
	}

	integer := parts[0]
	if len(integer) > 1 && integer[0] == '0' {
		return Price{}, fmt.Errorf("%w: leading zeros", ErrInvalidPrice)
	}

	for i := 0; i < len(integer); i++ {
		if integer[i] < '0' || integer[i] > '9' {
			return Price{}, fmt.Errorf("%w: invalid integer digit", ErrInvalidPrice)
		}
	}

	fraction := ""

	if len(parts) == 2 {
		if len(parts[1]) == 0 {
			return Price{}, fmt.Errorf("%w: missing fractional digits", ErrInvalidPrice)
		}

		for i := 0; i < len(parts[1]); i++ {
			if parts[1][i] < '0' || parts[1][i] > '9' {
				return Price{}, fmt.Errorf("%w: invalid fractional digit", ErrInvalidPrice)
			}
		}

		fraction = strings.TrimRight(parts[1], "0")

		if len(fraction) > 18 {
			return Price{}, fmt.Errorf("%w: exceeds 18 fractional places", ErrInvalidPrice)
		}
	}

	if integer == "0" && fraction == "" {
		return Price{}, fmt.Errorf("%w: price must be positive", ErrInvalidPrice)
	}

	if fraction == "" {
		return Price{value: integer}, nil
	}

	return Price{value: integer + "." + fraction}, nil
}

func (p Price) String() string { return p.value }

// Validate catches the zero value, which is never a valid exchange rate.
func (p Price) Validate() error {
	parsed, err := ParsePrice(p.value)
	if err != nil {
		return err
	}

	if parsed.value != p.value {
		return fmt.Errorf("%w: price is not canonical", ErrInvalidPrice)
	}
	
	return nil
}
