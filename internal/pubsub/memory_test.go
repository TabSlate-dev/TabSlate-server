package pubsub_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/TabSlate-dev/TabSlate-server/internal/pubsub"
)

func TestInMemoryHub_BroadcastReachesSubscriber(t *testing.T) {
	h := pubsub.NewInMemoryHub()
	defer h.Close()

	connID, ch := h.Subscribe("user1")
	_ = connID

	h.Broadcast("user1", 42)

	select {
	case seq := <-ch:
		if seq != 42 {
			t.Fatalf("got seq %d, want 42", seq)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for broadcast")
	}
}

func TestInMemoryHub_UnsubscribeClosesChannel(t *testing.T) {
	h := pubsub.NewInMemoryHub()
	defer h.Close()

	connID, ch := h.Subscribe("user1")
	h.Unsubscribe("user1", connID)

	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected channel to be closed")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout: channel not closed after Unsubscribe")
	}
}

func TestInMemoryHub_BroadcastToWrongUserIgnored(t *testing.T) {
	h := pubsub.NewInMemoryHub()
	defer h.Close()

	_, ch := h.Subscribe("user1")

	h.Broadcast("user2", 99)

	select {
	case <-ch:
		t.Fatal("expected no broadcast for user1")
	case <-time.After(50 * time.Millisecond):
		// correct: nothing received
	}
}

func TestInMemoryHub_MultipleSubscribersAllReceive(t *testing.T) {
	h := pubsub.NewInMemoryHub()
	defer h.Close()

	_, ch1 := h.Subscribe("user1")
	_, ch2 := h.Subscribe("user1")

	h.Broadcast("user1", 7)

	for _, ch := range []<-chan int64{ch1, ch2} {
		select {
		case seq := <-ch:
			if seq != 7 {
				t.Fatalf("got %d, want 7", seq)
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatal("timeout")
		}
	}
}

func TestInMemoryHub_CloseClosesAllChannels(t *testing.T) {
	h := pubsub.NewInMemoryHub()

	_, ch1 := h.Subscribe("user1")
	_, ch2 := h.Subscribe("user2")

	h.Close()

	for _, ch := range []<-chan int64{ch1, ch2} {
		select {
		case _, ok := <-ch:
			if ok {
				t.Fatal("expected channel to be closed after Close()")
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatal("timeout: channel not closed after Close()")
		}
	}
}

func TestInMemoryHub_ConcurrentAccess(t *testing.T) {
	h := pubsub.NewInMemoryHub()

	const goroutines = 10
	var wg sync.WaitGroup

	// Start subscribers
	for i := 0; i < goroutines; i++ {
		userID := fmt.Sprintf("user%d", i)
		connID, ch := h.Subscribe(userID)
		go func(uid string, cid int64, c <-chan int64) {
			for range c {
			}
			_ = cid
		}(userID, connID, ch)
	}

	// Concurrent broadcasts
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			userID := fmt.Sprintf("user%d", i%goroutines)
			h.Broadcast(userID, int64(i))
		}(i)
	}

	wg.Wait()
	h.Close()
}
