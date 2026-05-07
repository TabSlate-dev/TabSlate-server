package pubsub

// Hub is a pub/sub broadcaster for SSE connections.
// Subscribe returns a receive-only channel that receives new seq values.
// Implementations must be safe for concurrent use.
type Hub interface {
	Subscribe(userID string) (connID int64, ch <-chan int64)
	Broadcast(userID string, seq int64)
	Unsubscribe(userID string, connID int64)
}
