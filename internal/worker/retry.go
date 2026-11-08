package worker

import "time"

// retryDelay returns bounded exponential backoff for a 1-based attempt number.
// With maxAttempts=3, the retry delays are 2s after attempt 1 and 4s after 2.
func retryDelay(attempt int) time.Duration {
	const base = 2 * time.Second
	const cap = 30 * time.Second

	if attempt <= 1 {
		return base
	}

	delay := base

	for i := 1; i < attempt; i++ {
		if delay >= cap/2 {
			return cap
		}

		delay *= 2
	}

	return delay
}
