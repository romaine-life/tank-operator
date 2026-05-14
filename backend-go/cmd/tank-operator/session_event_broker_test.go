package main

import (
	"testing"
	"time"
)

func TestSessionEventBrokerNotifiesSessionSubscribers(t *testing.T) {
	broker := newSessionEventBroker()
	ch, unsubscribe := broker.Subscribe("63")
	defer unsubscribe()

	broker.Notify("other")
	select {
	case <-ch:
		t.Fatal("subscriber received notification for another session")
	default:
	}

	broker.Notify("63")
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for session notification")
	}
}

func TestSessionEventBrokerUnsubscribeRemovesSubscriber(t *testing.T) {
	broker := newSessionEventBroker()
	_, unsubscribe := broker.Subscribe("63")
	if got := broker.SubscriberCount("63"); got != 1 {
		t.Fatalf("SubscriberCount = %d, want 1", got)
	}
	unsubscribe()
	if got := broker.SubscriberCount("63"); got != 0 {
		t.Fatalf("SubscriberCount after unsubscribe = %d, want 0", got)
	}
}
