package infra

import (
	"context"
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
