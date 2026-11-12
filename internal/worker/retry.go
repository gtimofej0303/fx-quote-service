package worker

import "time"

// retryDelay doubles the wait after each try, up to a limit. Tries start at 1.
// With maxAttempts=3, wait 2 seconds after try 1 and 4 seconds after try 2.
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
