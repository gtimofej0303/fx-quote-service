package frankfurter

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// parseRetryAfter accepts an HTTP date or an unsigned number of seconds.
// Invalid values fall back to the worker's normal backoff. A uint32 seconds
// bound (about 136 years) prevents overflow when converting to time.Duration.
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
