package frankfurter

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// parseRetryAfter reads an HTTP date or a number of seconds with no sign.
// If the value is invalid, use the worker's normal retry delay.
// Limit seconds to uint32 (about 136 years) so time.Duration fits.
func parseRetryAfter(raw string, receivedAt time.Time) time.Time {
	raw = strings.TrimSpace(raw)

	receivedAt = receivedAt.UTC()

	if raw == "" {
		return time.Time{}
	}

	if at, err := http.ParseTime(raw); err == nil {
		if at.After(receivedAt) {
			return at.UTC()
		}

		return time.Time{}
	}

	for i := 0; i < len(raw); i++ {
		if raw[i] < '0' || raw[i] > '9' {
			return time.Time{}
		}
	}

	seconds, err := strconv.ParseUint(raw, 10, 32)
	if err != nil {
		return time.Time{}
	}

	return receivedAt.Add(time.Duration(seconds) * time.Second)
}
