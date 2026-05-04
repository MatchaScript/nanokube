package healthcheck

import (
	"context"
	"testing"
	"time"
)

func TestWaitForAPIServer_ReturnsWhenReadyz200s(t *testing.T) {
	f := newFakeAPIServer(t)
	f.ready.Store(true)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := WaitForAPIServer(ctx, f.client); err != nil {
		t.Fatalf("WaitForAPIServer: %v", err)
	}
}

func TestWaitForAPIServer_PollsUntilReady(t *testing.T) {
	f := newFakeAPIServer(t)
	go func() {
		time.Sleep(300 * time.Millisecond)
		f.ready.Store(true)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if err := WaitForAPIServer(ctx, f.client); err != nil {
		t.Fatalf("WaitForAPIServer: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 200*time.Millisecond {
		t.Errorf("WaitForAPIServer returned suspiciously fast (%v); fake was not ready yet", elapsed)
	}
}

func TestWaitForAPIServer_TimeoutSurfacesLastError(t *testing.T) {
	f := newFakeAPIServer(t)
	// ready stays false forever

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	err := WaitForAPIServer(ctx, f.client)
	if err == nil {
		t.Fatal("WaitForAPIServer on never-ready apiserver = nil")
	}
}
