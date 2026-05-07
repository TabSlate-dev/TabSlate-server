package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/tabslate/server/internal/store"
)

func TestInMemoryCache_SetAndGet(t *testing.T) {
	c := store.NewInMemoryCache()
	defer c.Close()

	ctx := context.Background()
	if err := c.Set(ctx, "k", []byte("v"), time.Minute); err != nil {
		t.Fatal(err)
	}

	val, found, err := c.Get(ctx, "k")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("expected key to be found")
	}
	if string(val) != "v" {
		t.Fatalf("got %q, want %q", val, "v")
	}
}

func TestInMemoryCache_GetMissing(t *testing.T) {
	c := store.NewInMemoryCache()
	defer c.Close()

	_, found, err := c.Get(context.Background(), "no-such-key")
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("expected key to not be found")
	}
}

func TestInMemoryCache_Del(t *testing.T) {
	c := store.NewInMemoryCache()
	defer c.Close()

	ctx := context.Background()
	c.Set(ctx, "k", []byte("v"), time.Minute)
	c.Del(ctx, "k")

	_, found, _ := c.Get(ctx, "k")
	if found {
		t.Fatal("expected key to be gone after Del")
	}
}

func TestInMemoryCache_TTLExpiry(t *testing.T) {
	c := store.NewInMemoryCache()
	defer c.Close()

	ctx := context.Background()
	c.Set(ctx, "k", []byte("v"), 50*time.Millisecond)

	time.Sleep(100 * time.Millisecond)

	_, found, _ := c.Get(ctx, "k")
	if found {
		t.Fatal("expected key to be expired")
	}
}
