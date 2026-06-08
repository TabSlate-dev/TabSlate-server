package ratelimit_test

import (
	"context"
	"testing"
	"time"

	"github.com/TabSlate-dev/TabSlate-server/internal/ratelimit"
)

func TestInMemoryLimiter_Allow_UnderLimit(t *testing.T) {
	l := ratelimit.NewInMemoryLimiter()
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if !l.Allow(ctx, "ip:1.2.3.4", 5, time.Minute) {
			t.Fatalf("expected Allow to return true on request %d", i+1)
		}
	}
}

func TestInMemoryLimiter_Allow_AtLimit(t *testing.T) {
	l := ratelimit.NewInMemoryLimiter()
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		l.Allow(ctx, "ip:1.2.3.4", 5, time.Minute)
	}
	if l.Allow(ctx, "ip:1.2.3.4", 5, time.Minute) {
		t.Fatal("expected Allow to return false when at limit")
	}
}

func TestInMemoryLimiter_Allow_WindowExpiry(t *testing.T) {
	l := ratelimit.NewInMemoryLimiter()
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		l.Allow(ctx, "ip:1.2.3.4", 5, 50*time.Millisecond)
	}
	// After window expires, limit resets
	time.Sleep(60 * time.Millisecond)
	if !l.Allow(ctx, "ip:1.2.3.4", 5, 50*time.Millisecond) {
		t.Fatal("expected Allow to return true after window expired")
	}
}

func TestInMemoryLimiter_IncrCounter_ReturnsMonotonicCount(t *testing.T) {
	l := ratelimit.NewInMemoryLimiter()
	ctx := context.Background()

	for i := int64(1); i <= 3; i++ {
		count, err := l.IncrCounter(ctx, "email:foo@bar.com", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if count != i {
			t.Fatalf("got count %d, want %d", count, i)
		}
	}
}

func TestInMemoryLimiter_ResetCounter(t *testing.T) {
	l := ratelimit.NewInMemoryLimiter()
	ctx := context.Background()

	l.IncrCounter(ctx, "email:foo@bar.com", time.Minute)
	l.IncrCounter(ctx, "email:foo@bar.com", time.Minute)
	l.ResetCounter(ctx, "email:foo@bar.com")

	count, _ := l.GetCounter(ctx, "email:foo@bar.com")
	if count != 0 {
		t.Fatalf("got count %d after reset, want 0", count)
	}
}

func TestInMemoryLimiter_IncrCounter_WindowExpiry(t *testing.T) {
	l := ratelimit.NewInMemoryLimiter()
	ctx := context.Background()

	l.IncrCounter(ctx, "ip:1.2.3.4", 50*time.Millisecond)
	l.IncrCounter(ctx, "ip:1.2.3.4", 50*time.Millisecond)

	time.Sleep(60 * time.Millisecond)

	count, _ := l.IncrCounter(ctx, "ip:1.2.3.4", 50*time.Millisecond)
	if count != 1 {
		t.Fatalf("got count %d after window expiry, want 1", count)
	}
}
