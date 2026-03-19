package bus

import (
	"context"
	"sync"
	"testing"
)

// Verify interfaces are satisfied at compile time.
var _ Bus = (*NATSBus)(nil)
var _ Bus = (*StubBus)(nil)

// publishedMsg records a message sent via Publish.
type publishedMsg struct {
	Subject string
	Data    []byte
}

// StubBus implements Bus for testing.
type StubBus struct {
	mu        sync.Mutex
	published []publishedMsg
	handlers  map[string]func(data []byte)
	reqHandlers map[string]Handler
}

// NewStubBus creates a new StubBus.
func NewStubBus() *StubBus {
	return &StubBus{
		handlers:    make(map[string]func(data []byte)),
		reqHandlers: make(map[string]Handler),
	}
}

func (b *StubBus) Publish(subject string, data []byte) error {
	b.mu.Lock()
	b.published = append(b.published, publishedMsg{Subject: subject, Data: data})
	h := b.handlers[subject]
	b.mu.Unlock()
	if h != nil {
		h(data)
	}
	return nil
}

func (b *StubBus) Subscribe(subject string, handler func(data []byte)) (Subscription, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[subject] = handler
	return &stubSubscription{bus: b, subject: subject}, nil
}

func (b *StubBus) HandleRequest(subject string, handler Handler) (Subscription, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reqHandlers[subject] = handler
	return &stubSubscription{bus: b, subject: subject}, nil
}

func (b *StubBus) Request(ctx context.Context, subject string, data []byte) ([]byte, error) {
	b.mu.Lock()
	h := b.reqHandlers[subject]
	b.mu.Unlock()
	if h == nil {
		return nil, context.DeadlineExceeded
	}
	return h(subject, data)
}

func (b *StubBus) Close() {}

// Published returns a copy of published messages.
func (b *StubBus) Published() []publishedMsg {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]publishedMsg, len(b.published))
	copy(out, b.published)
	return out
}

type stubSubscription struct {
	bus     *StubBus
	subject string
}

func (s *stubSubscription) Unsubscribe() error {
	s.bus.mu.Lock()
	defer s.bus.mu.Unlock()
	delete(s.bus.handlers, s.subject)
	delete(s.bus.reqHandlers, s.subject)
	return nil
}

func TestStubBusPublish(t *testing.T) {
	bus := NewStubBus()

	tests := []struct {
		name    string
		subject string
		data    string
	}{
		{"simple message", "test.subject", "hello"},
		{"empty data", "test.empty", ""},
		{"binary-like data", "test.binary", "\x00\x01\x02"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := len(bus.Published())
			if err := bus.Publish(tc.subject, []byte(tc.data)); err != nil {
				t.Fatalf("Publish() error = %v", err)
			}
			msgs := bus.Published()
			if len(msgs) != before+1 {
				t.Fatalf("expected %d published messages, got %d", before+1, len(msgs))
			}
			last := msgs[len(msgs)-1]
			if last.Subject != tc.subject {
				t.Errorf("subject = %q, want %q", last.Subject, tc.subject)
			}
			if string(last.Data) != tc.data {
				t.Errorf("data = %q, want %q", last.Data, tc.data)
			}
		})
	}
}

func TestStubBusSubscribe(t *testing.T) {
	bus := NewStubBus()
	var received []byte

	_, err := bus.Subscribe("events.test", func(data []byte) {
		received = data
	})
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}

	if err := bus.Publish("events.test", []byte("payload")); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	if string(received) != "payload" {
		t.Errorf("received = %q, want %q", received, "payload")
	}
}

func TestStubBusHandleRequest(t *testing.T) {
	bus := NewStubBus()

	_, err := bus.HandleRequest("rpc.echo", func(subject string, data []byte) ([]byte, error) {
		return append([]byte("echo:"), data...), nil
	})
	if err != nil {
		t.Fatalf("HandleRequest() error = %v", err)
	}

	reply, err := bus.Request(context.Background(), "rpc.echo", []byte("ping"))
	if err != nil {
		t.Fatalf("Request() error = %v", err)
	}
	if string(reply) != "echo:ping" {
		t.Errorf("reply = %q, want %q", reply, "echo:ping")
	}
}

func TestStubBusRequestNoHandler(t *testing.T) {
	bus := NewStubBus()

	_, err := bus.Request(context.Background(), "rpc.missing", []byte("data"))
	if err == nil {
		t.Fatal("expected error for missing handler, got nil")
	}
}

func TestStubBusUnsubscribe(t *testing.T) {
	bus := NewStubBus()
	var called bool

	sub, err := bus.Subscribe("events.unsub", func(data []byte) {
		called = true
	})
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}

	if err := sub.Unsubscribe(); err != nil {
		t.Fatalf("Unsubscribe() error = %v", err)
	}

	// Publish after unsubscribe should not trigger handler.
	if err := bus.Publish("events.unsub", []byte("data")); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if called {
		t.Error("handler called after unsubscribe")
	}
}

func TestNATSBusConnectionError(t *testing.T) {
	// Attempting to connect to an invalid URL should return an error.
	_, err := NewNATSBus("nats://127.0.0.1:1")
	if err == nil {
		t.Fatal("expected connection error, got nil")
	}
}
