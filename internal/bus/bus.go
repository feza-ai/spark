package bus

import "context"

// Handler is a function that handles a request and returns a response.
type Handler func(subject string, data []byte) ([]byte, error)

// Bus defines the messaging interface for spark.
type Bus interface {
	// Publish sends a message to a subject (fire-and-forget).
	Publish(subject string, data []byte) error

	// Subscribe registers a callback for messages on a subject.
	// Returns a subscription that can be unsubscribed.
	Subscribe(subject string, handler func(data []byte)) (Subscription, error)

	// HandleRequest registers a request-reply handler for a subject.
	HandleRequest(subject string, handler Handler) (Subscription, error)

	// Request sends a request and waits for a reply with timeout.
	Request(ctx context.Context, subject string, data []byte) ([]byte, error)

	// Close disconnects from the message bus.
	Close()
}

// Subscription represents an active subscription.
type Subscription interface {
	Unsubscribe() error
}
