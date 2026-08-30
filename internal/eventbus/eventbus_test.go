package eventbus

import (
	"testing"
	"time"
)

func TestPublishSubscribe(t *testing.T) {
	bus := New()
	ch, unsub := bus.Subscribe(4)
	defer unsub()

	bus.Publish(Event{Source: "tftp", Kind: KindTransfer, Message: "hello"})

	select {
	case ev := <-ch:
		if ev.Source != "tftp" || ev.Message != "hello" {
			t.Errorf("got %+v", ev)
		}
		if ev.Time.IsZero() {
			t.Error("Publish should stamp a zero-value Time")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for published event")
	}
}

func TestMultipleSubscribersReceiveSameEvent(t *testing.T) {
	bus := New()
	ch1, unsub1 := bus.Subscribe(4)
	defer unsub1()
	ch2, unsub2 := bus.Subscribe(4)
	defer unsub2()

	bus.Publish(Event{Message: "fan-out"})

	for i, ch := range []<-chan Event{ch1, ch2} {
		select {
		case ev := <-ch:
			if ev.Message != "fan-out" {
				t.Errorf("subscriber %d got %+v", i, ev)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d: timed out", i)
		}
	}
}

func TestUnsubscribeClosesChannel(t *testing.T) {
	bus := New()
	ch, unsub := bus.Subscribe(4)
	unsub()

	_, ok := <-ch
	if ok {
		t.Error("channel should be closed after unsubscribe")
	}
}

func TestPublishNeverBlocksOnFullSubscriber(t *testing.T) {
	bus := New()
	ch, unsub := bus.Subscribe(1)
	defer unsub()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			bus.Publish(Event{Message: "spam"})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked despite a full, un-drained subscriber channel")
	}
	<-ch // drain one so the deferred unsub's close doesn't race a pending send
}
