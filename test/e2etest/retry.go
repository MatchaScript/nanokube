package e2etest

import "time"

// Retry calls fn up to attempts times, sleeping delay between failed
// calls. Returns nil on first success, otherwise the last error.
// Mirrors test/e2e/lib.sh:retry.
func Retry(attempts int, delay time.Duration, fn func() error) error {
	var err error
	for i := 0; i < attempts; i++ {
		if err = fn(); err == nil {
			return nil
		}
		if i+1 < attempts {
			time.Sleep(delay)
		}
	}
	return err
}
