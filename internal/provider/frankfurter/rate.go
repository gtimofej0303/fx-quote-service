package frankfurter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	domain "github.com/gtimofej0303/fx-quote-service/internal/domain/quote"
)

// parseRateResponse checks the required v2 fields and the currency order.
// Read the rate as decimal text to keep its exact value.
func parseRateResponse(data []byte, expectedBase, expectedQuote string) (domain.Price, domain.SourceDate, error) {
	dec := json.NewDecoder(bytes.NewReader(data))

	start, err := dec.Token()
	if err != nil || start != json.Delim('{') {
		return domain.Price{}, "", errors.New("response root must be an object")
	}

	fields := make(map[string]json.RawMessage, 4)
	seen := make(map[string]struct{}, 4)

	for dec.More() {
		nameToken, err := dec.Token()
		if err != nil {
			return domain.Price{}, "", fmt.Errorf("object key: %w", err)
		}

		name, ok := nameToken.(string)
		if !ok {
			return domain.Price{}, "", errors.New("invalid object field name")
		}

		if _, exists := seen[name]; exists {
			return domain.Price{}, "", errors.New("duplicate response field")
		}
		seen[name] = struct{}{}

		var raw json.RawMessage

		if err := dec.Decode(&raw); err != nil {
			return domain.Price{}, "", fmt.Errorf("decode field: %w", err)
		}

		switch name {
		case "date", "base", "quote", "rate":
			fields[name] = raw
		default:
			// Allow extra fields from the provider.
			// Their JSON must be valid and fit within the size limit.
		}
	}

	end, err := dec.Token()
	if err != nil || end != json.Delim('}') {
		return domain.Price{}, "", errors.New("unterminated response object")
	}

	if _, err := dec.Token(); err != io.EOF {
		return domain.Price{}, "", errors.New("trailing JSON data")
	}

	for _, name := range []string{"date", "base", "quote", "rate"} {
		if _, exists := fields[name]; !exists {
			return domain.Price{}, "", fmt.Errorf("required response field %q is missing", name)
		}
	}

	var base, quote, rawDate string
	if err := json.Unmarshal(fields["base"], &base); err != nil || base != expectedBase {
		return domain.Price{}, "", errors.New("provider base differs from requested base")
	}

	if err := json.Unmarshal(fields["quote"], &quote); err != nil || quote != expectedQuote {
		return domain.Price{}, "", errors.New("provider quote differs from requested quote")
	}

	if err := json.Unmarshal(fields["date"], &rawDate); err != nil {
		return domain.Price{}, "", errors.New("provider date is not a string")
	}

	sourceDate, err := domain.ParseSourceDate(rawDate)
	if err != nil {
		return domain.Price{}, "", fmt.Errorf("invalid provider date: %w", err)
	}

	price, err := parseJSONDecimal(fields["rate"])
	if err != nil {
		return domain.Price{}, "", fmt.Errorf("invalid provider rate: %w", err)
	}

	return price, sourceDate, nil
}

// parseJSONDecimal turns a JSON number, such as 1e2, into decimal text.
// domain.ParsePrice checks the text. No rounding is allowed.
func parseJSONDecimal(raw json.RawMessage) (domain.Price, error) {
	if len(raw) == 0 || raw[0] < '0' || raw[0] > '9' {
		return domain.Price{}, errors.New("rate must be a positive JSON number")
	}

	// Limit the size, even if the provider sends a huge number.
	if len(raw) > 256 {
		return domain.Price{}, errors.New("rate number is too long")
	}

	var value json.Number

	if err := json.Unmarshal(raw, &value); err != nil {
		return domain.Price{}, err
	}
	text := value.String()

	mantissa, exponent := text, 0
	if i := strings.IndexAny(text, "eE"); i >= 0 {
		mantissa = text[:i]

		parsed, err := strconv.Atoi(text[i+1:])

		if err != nil || parsed < -256 || parsed > 256 {
			return domain.Price{}, errors.New("decimal exponent is out of range")
		}

		exponent = parsed
	}

	// Count digits before the decimal point.
	intDigits := len(mantissa)
	if dot := strings.IndexByte(mantissa, '.'); dot >= 0 {
		intDigits = dot

		mantissa = strings.Replace(mantissa, ".", "", 1)
	}

	// Find where the decimal point should go after applying the exponent.
	point := intDigits + exponent

	// Raw result.
	var plain string

	switch {
	case point <= 0:
		plain = "0." + strings.Repeat("0", -point) + mantissa
	case point >= len(mantissa):
		plain = mantissa + strings.Repeat("0", point-len(mantissa))
	default:
		plain = mantissa[:point] + "." + mantissa[point:]
	}

	// JSON does not allow extra zeros before a whole number.
	// Moving the decimal point can add them (0.001e4 -> 0010). Remove them.
	if dot := strings.IndexByte(plain, '.'); dot >= 0 {
		// Get the whole-number part before the decimal point.
		whole := strings.TrimLeft(plain[:dot], "0")

		if whole == "" {
			whole = "0"
		}

		plain = whole + plain[dot:]
	} else {
		plain = strings.TrimLeft(plain, "0")

		if plain == "" {
			plain = "0"
		}
	}

	return domain.ParsePrice(plain)
}
