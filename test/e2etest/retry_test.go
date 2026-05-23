package e2etest

import (
	"errors"
	"testing"
	"time"
)

func TestRetry_FirstAttemptSucceeds(t *testing.T) {
	calls := 0
	err := Retry(3, time.Millisecond, func() error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("got %v, want nil", err)
	}
	if calls != 1 {
		t.Errorf("calls=%d, want 1", calls)
	}
}

func TestRetry_EventualSuccess(t *testing.T) {
	calls := 0
	err := Retry(3, time.Millisecond, func() error {
		calls++
		if calls < 3 {
			return errors.New("nope")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("got %v, want nil", err)
	}
	if calls != 3 {
		t.Errorf("calls=%d, want 3", calls)
	}
}

func TestRetry_ExhaustsAttempts(t *testing.T) {
	calls := 0
	target := errors.New("last")
	err := Retry(3, time.Millisecond, func() error {
		calls++
		return target
	})
	if !errors.Is(err, target) {
		t.Errorf("got %v, want %v", err, target)
	}
	if calls != 3 {
		t.Errorf("calls=%d, want 3", calls)
	}
}

func TestRetry_ZeroAttempts(t *testing.T) {
	calls := 0
	err := Retry(0, time.Millisecond, func() error {
		calls++
		return errors.New("should not be called")
	})
	if err != nil {
		t.Errorf("got %v, want nil (no attempts means no error)", err)
	}
	if calls != 0 {
		t.Errorf("calls=%d, want 0", calls)
	}
}
