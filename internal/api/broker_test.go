package api

import (
	"testing"

	"github.com/zichuanxu/nimget/internal/download"
)

func TestBrokerDisconnectsLaggingSubscriber(t *testing.T) {
	broker := NewBroker(1)
	events, unsubscribe := broker.subscribe()
	defer unsubscribe()

	broker.Publish(download.Event{Type: download.EventProgress})
	broker.Publish(download.Event{Type: download.EventProgress}) // buffer full

	if event, open := <-events; !open || event.Sequence != 1 {
		t.Fatalf("first event = %+v, open=%v", event, open)
	}
	if _, open := <-events; open {
		t.Fatal("lagging subscriber was not disconnected")
	}
	broker.Publish(download.Event{Type: download.EventProgress}) // must not panic on the closed channel
	if got := broker.sequence(); got != 3 {
		t.Fatalf("sequence = %d; want 3", got)
	}
}
