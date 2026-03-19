package bus

import (
	"encoding/json"
	"testing"
)

func TestEventPublisher_Publish_Scheduled(t *testing.T) {
	stub := NewStubBus()
	ep := NewEventPublisher(stub)

	if err := ep.Publish("my-pod", "scheduled", "pod scheduled on node-1"); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	msgs := stub.Published()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 published message, got %d", len(msgs))
	}

	msg := msgs[0]
	wantSubject := "evt.spark.scheduled.my-pod"
	if msg.Subject != wantSubject {
		t.Errorf("subject = %q, want %q", msg.Subject, wantSubject)
	}

	var evt LifecycleEvent
	if err := json.Unmarshal(msg.Data, &evt); err != nil {
		t.Fatalf("failed to unmarshal event: %v", err)
	}
	if evt.PodName != "my-pod" {
		t.Errorf("PodName = %q, want %q", evt.PodName, "my-pod")
	}
	if evt.EventType != "scheduled" {
		t.Errorf("EventType = %q, want %q", evt.EventType, "scheduled")
	}
	if evt.Message != "pod scheduled on node-1" {
		t.Errorf("Message = %q, want %q", evt.Message, "pod scheduled on node-1")
	}
	if evt.Timestamp == "" {
		t.Error("Timestamp is empty")
	}
}

func TestEventPublisher_Publish_MultipleEventTypes(t *testing.T) {
	stub := NewStubBus()
	ep := NewEventPublisher(stub)

	types := []struct {
		eventType   string
		podName     string
		wantSubject string
	}{
		{"started", "pod-a", "evt.spark.started.pod-a"},
		{"completed", "pod-b", "evt.spark.completed.pod-b"},
		{"failed", "pod-c", "evt.spark.failed.pod-c"},
		{"preempted", "pod-d", "evt.spark.preempted.pod-d"},
		{"restarted", "pod-e", "evt.spark.restarted.pod-e"},
		{"deleted", "pod-f", "evt.spark.deleted.pod-f"},
	}

	for _, tc := range types {
		t.Run(tc.eventType, func(t *testing.T) {
			before := len(stub.Published())
			if err := ep.Publish(tc.podName, tc.eventType, "test message"); err != nil {
				t.Fatalf("Publish() error = %v", err)
			}

			msgs := stub.Published()
			if len(msgs) != before+1 {
				t.Fatalf("expected %d published messages, got %d", before+1, len(msgs))
			}

			msg := msgs[len(msgs)-1]
			if msg.Subject != tc.wantSubject {
				t.Errorf("subject = %q, want %q", msg.Subject, tc.wantSubject)
			}

			var evt LifecycleEvent
			if err := json.Unmarshal(msg.Data, &evt); err != nil {
				t.Fatalf("failed to unmarshal event: %v", err)
			}
			if evt.EventType != tc.eventType {
				t.Errorf("EventType = %q, want %q", evt.EventType, tc.eventType)
			}
			if evt.PodName != tc.podName {
				t.Errorf("PodName = %q, want %q", evt.PodName, tc.podName)
			}
		})
	}
}
