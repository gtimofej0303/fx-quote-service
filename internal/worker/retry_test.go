package worker

import (
	"testing"
	"time"
)

func TestRetryDelay(t *testing.T) {
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 2 * time.Second},
		{1, 2 * time.Second},
		{2, 4 * time.Second},
		{3, 8 * time.Second},
		{4, 16 * time.Second},
		{5, 30 * time.Second},
		{1000, 30 * time.Second},
	}
	for _, tc := range cases {
		if got := retryDelay(tc.attempt); got != tc.want {
			t.Errorf("attempt=%d: delay=%s, want=%s", tc.attempt, got, tc.want)
		}
	}
}
