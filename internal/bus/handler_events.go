package bus

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// LifecycleEvent is published when a pod changes state.
type LifecycleEvent struct {
	PodName   string `json:"podName"`
	EventType string `json:"eventType"` // scheduled, started, completed, failed, preempted, restarted, deleted
	Message   string `json:"message"`
	Timestamp string `json:"timestamp"`
}

// EventPublisher publishes pod lifecycle events over NATS.
type EventPublisher struct {
	bus Bus
}

// NewEventPublisher creates a new EventPublisher backed by the given Bus.
func NewEventPublisher(b Bus) *EventPublisher {
	return &EventPublisher{bus: b}
}

// Publish sends a lifecycle event to evt.spark.{eventType}.{podName}.
func (ep *EventPublisher) Publish(podName, eventType, message string) error {
	subject := fmt.Sprintf("evt.spark.%s.%s", eventType, podName)

	evt := LifecycleEvent{
		PodName:   podName,
		EventType: eventType,
		Message:   message,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	data, err := json.Marshal(evt)
	if err != nil {
		slog.Error("failed to marshal lifecycle event", "error", err, "podName", podName, "eventType", eventType)
		return err
	}

	if err := ep.bus.Publish(subject, data); err != nil {
		slog.Error("failed to publish lifecycle event", "error", err, "subject", subject)
		return err
	}

	return nil
}
