package pubsub_test

import (
	"testing"
	"time"

	"github.com/tabslate/server/internal/pubsub"
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
