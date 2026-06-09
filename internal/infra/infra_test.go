package infra

import (
	"context"
	"errors"
	"testing"

	"github.com/alicebob/miniredis/v2"
)

func TestProviders_Ping_InMemory(t *testing.T) {
	p, cleanup, err := New("")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer cleanup()
	if err := p.Ping(context.Background()); err != nil {
		t.Errorf("in-memory Ping: expected nil, got %v", err)
	}
}

func TestProviders_Ping_RedisConfigured(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	defer mr.Close()

	p, cleanup, err := New("redis://" + mr.Addr() + "/0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer cleanup()

	err = p.Ping(context.Background())
	if err != nil {
		t.Fatalf("redis-configured Ping: expected nil, got %v", err)
	}
}

func TestProviders_Ping_RedisConfiguredContextCanceled(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	defer mr.Close()

	p, cleanup, err := New("redis://" + mr.Addr() + "/0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = p.Ping(ctx)
	if err == nil {
		t.Fatal("redis-configured Ping with canceled context: expected error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("redis-configured Ping with canceled context: expected context.Canceled, got %v", err)
	}
}

func TestProviders_Ping_RedisUnavailable(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	addr := mr.Addr()
	mr.Close()

	p, cleanup, err := New("redis://" + addr + "/0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer cleanup()

	err = p.Ping(context.Background())
	if err == nil {
		t.Fatal("redis-unavailable Ping: expected error, got nil")
	}
}
