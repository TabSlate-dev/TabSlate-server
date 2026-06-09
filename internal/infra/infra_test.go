package infra

import (
	"context"
	"errors"
	"testing"
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

func TestProviders_Ping_RedisConfiguredContextCanceled(t *testing.T) {
	p, cleanup, err := New("redis://127.0.0.1:6379/0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = p.Ping(ctx)
	if err == nil {
		t.Fatal("redis-configured Ping: expected error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("redis-configured Ping: expected context.Canceled, got %v", err)
	}
}
