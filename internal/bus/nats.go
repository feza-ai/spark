package bus

import (
	"context"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
)

// NATSBus implements Bus using NATS.
type NATSBus struct {
	conn *nats.Conn
}

// NewNATSBus connects to a NATS server and returns a Bus.
func NewNATSBus(url string) (*NATSBus, error) {
	conn, err := nats.Connect(url,
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			slog.Warn("nats disconnected", "error", err)
		}),
		nats.ReconnectHandler(func(_ *nats.Conn) {
			slog.Info("nats reconnected")
		}),
	)
	if err != nil {
		return nil, err
	}
	return &NATSBus{conn: conn}, nil
}

func (b *NATSBus) Publish(subject string, data []byte) error {
	return b.conn.Publish(subject, data)
}

func (b *NATSBus) Subscribe(subject string, handler func(data []byte)) (Subscription, error) {
	sub, err := b.conn.Subscribe(subject, func(msg *nats.Msg) {
		handler(msg.Data)
	})
	if err != nil {
		return nil, err
	}
	return &natsSubscription{sub: sub}, nil
}

func (b *NATSBus) HandleRequest(subject string, handler Handler) (Subscription, error) {
	sub, err := b.conn.Subscribe(subject, func(msg *nats.Msg) {
		reply, err := handler(msg.Subject, msg.Data)
		if err != nil {
			slog.Error("request handler failed", "subject", subject, "error", err)
			return
		}
		if msg.Reply != "" {
			if pubErr := b.conn.Publish(msg.Reply, reply); pubErr != nil {
				slog.Error("failed to publish reply", "subject", subject, "error", pubErr)
			}
		}
	})
	if err != nil {
		return nil, err
	}
	return &natsSubscription{sub: sub}, nil
}

func (b *NATSBus) Request(ctx context.Context, subject string, data []byte) ([]byte, error) {
	msg, err := b.conn.RequestWithContext(ctx, subject, data)
	if err != nil {
		return nil, err
	}
	return msg.Data, nil
}

func (b *NATSBus) Close() {
	b.conn.Close()
}

// natsSubscription wraps nats.Subscription to implement the Subscription interface.
type natsSubscription struct {
	sub *nats.Subscription
}

func (s *natsSubscription) Unsubscribe() error {
	return s.sub.Unsubscribe()
}
