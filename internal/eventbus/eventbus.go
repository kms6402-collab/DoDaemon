// Package eventbus is an in-process publish/subscribe hub used by the
// FTP/TFTP/Syslog servers to broadcast connection, transfer and error
// events to the logging, audit and web UI (SSE) subscribers without those
// subscribers being coupled to the servers directly.
package eventbus

import (
	"context"
	"sync"
	"time"
)

// Kind categorizes an Event so subscribers can filter cheaply.
type Kind string

const (
	KindConnect    Kind = "connect"
	KindDisconnect Kind = "disconnect"
	KindAuth       Kind = "auth"
	KindTransfer   Kind = "transfer"
	KindError      Kind = "error"
	KindConfig     Kind = "config"
)

// Event is a single structured occurrence emitted by any server.
type Event struct {
	Time       time.Time      `json:"time"`
	Source     string         `json:"source"` // "ftp" | "tftp" | "syslog" | "webui" | "config"
	Kind       Kind           `json:"kind"`
	RemoteAddr string         `json:"remote_addr,omitempty"`
	Message    string         `json:"message"`
	Fields     map[string]any `json:"fields,omitempty"`
}

// Bus is a fan-out, non-blocking publish/subscribe hub.
type Bus struct {
	mu   sync.RWMutex
	subs map[int]chan Event
	next int
}

// New creates an empty Bus.
func New() *Bus {
	return &Bus{subs: make(map[int]chan Event)}
}

// Subscribe returns a channel that receives every future published Event
// and an unsubscribe function that must be called when the caller is done
// (typically via defer). The channel is buffered; if a slow subscriber
// falls behind, the oldest-pending event is dropped rather than blocking
// publishers.
func (b *Bus) Subscribe(bufSize int) (<-chan Event, func()) {
	if bufSize <= 0 {
		bufSize = 64
	}
	ch := make(chan Event, bufSize)

	b.mu.Lock()
	id := b.next
	b.next++
	b.subs[id] = ch
	b.mu.Unlock()

	unsubscribe := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if c, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(c)
		}
	}
	return ch, unsubscribe
}

// Publish broadcasts ev to every current subscriber. It never blocks:
// a full subscriber channel has its oldest event dropped to make room.
func (b *Bus) Publish(ev Event) {
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}

	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs {
		select {
		case ch <- ev:
		default:
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- ev:
			default:
			}
		}
	}
}

// PublishCtx is a convenience wrapper that respects cancellation while
// still following Publish's non-blocking semantics.
func (b *Bus) PublishCtx(ctx context.Context, ev Event) {
	if ctx.Err() != nil {
		return
	}
	b.Publish(ev)
}
