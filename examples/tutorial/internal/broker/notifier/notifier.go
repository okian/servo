// Package notifier is the consuming side of the messaging layer: it
// subscribes to broker.OrderPlacedSubject and logs each event. A real
// second service would do this in its own process; this exists in the same
// binary purely to make the event-driven half of the architecture visible
// and testable without standing up an actual second service.
package notifier

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"example.com/servoorders/internal/broker"
	"example.com/servoorders/internal/broker/natsbroker"
	"example.com/servoorders/internal/observability"
	"github.com/nats-io/nats.go"
)

type Notifier struct {
	url string
	log *observability.Logger

	// mu guards conn and sub, which Run assigns and Drain/Stop read from
	// the shutdown goroutine.
	mu   sync.Mutex
	conn *nats.Conn
	sub  *nats.Subscription

	// inFlight counts handlers that have started and not yet finished.
	// Drain waits on it; without it, "stopped consuming" would mean
	// "stopped reading", not "finished what was already read".
	inFlight sync.WaitGroup
}

// New takes natsbroker.Config rather than declaring a config of its own:
// both ends of the messaging layer connect to the same server, and under
// //servo:config a setting has exactly one owner — a second struct tagged
// to read NATS_URL would be a collision `servo generate` refuses. The
// publisher's package owns the setting; this package borrows the value.
func New(cfg natsbroker.Config, log *observability.Logger) *Notifier {
	return &Notifier{url: cfg.URL, log: log}
}

// Run connects and subscribes, then holds until its context ends.
//
// It deliberately tears nothing down on the way out. servo runs every
// Runner to completion before it calls Shutdown, so a Run that closed its
// own subscription would leave Drain with nothing to drain and Stop with
// nothing to stop — the two phases below would be decoration. Releasing in
// Drain and Stop is what makes the distinction between them real.
func (n *Notifier) Run(ctx context.Context) error {
	conn, err := nats.Connect(n.url)
	if err != nil {
		return fmt.Errorf("notifier: connect: %w", err)
	}

	sub, err := conn.Subscribe(broker.OrderPlacedSubject, func(msg *nats.Msg) {
		n.inFlight.Add(1)
		defer n.inFlight.Done()
		n.handle(ctx, msg)
	})
	if err != nil {
		conn.Close()
		return fmt.Errorf("notifier: subscribe: %w", err)
	}

	n.mu.Lock()
	n.conn, n.sub = conn, sub
	n.mu.Unlock()

	<-ctx.Done()
	return nil
}

// handle processes one message. Core NATS is at-most-once and has no ack:
// a message is delivered, and whether this function succeeds or panics,
// the server will not send it again. That is what makes Drain matter here
// — the only chance to finish this work is before the process exits.
//
// If you need redelivery, that is JetStream, where this function would end
// in msg.Ack() on success and msg.Nak() on a failure worth retrying. The
// shape below does not change: you still want the handler to finish before
// shutdown, and Drain is still what waits for it.
func (n *Notifier) handle(ctx context.Context, msg *nats.Msg) {
	var event broker.OrderPlacedEvent
	if err := json.Unmarshal(msg.Data, &event); err != nil {
		// Nothing to retry: a message that will not parse now will not
		// parse later. Log it and drop it, rather than blocking the
		// subscription behind a poison message.
		n.log.ErrorContext(ctx, "notifier: malformed event", "error", err)
		return
	}
	n.log.InfoContext(ctx, "order placed",
		"order_id", event.OrderID, "user_id", event.UserID, "item", event.Item)
}

// Drain stops consuming and waits for what is already being consumed.
//
// Unsubscribe removes interest, so the server sends nothing further. The
// wait is the half people forget: at the moment shutdown begins there may
// be handlers midway through, and returning before they finish loses their
// work with no error anywhere — precisely the failure Drain exists to
// prevent.
//
// The wait is bounded by the context servo passes, which carries the stop
// budget. Overrunning it is reported rather than waited out forever.
func (n *Notifier) Drain(ctx context.Context) error {
	n.mu.Lock()
	sub := n.sub
	n.mu.Unlock()
	if sub == nil {
		return nil // Run never got as far as subscribing
	}

	if err := sub.Unsubscribe(); err != nil && !errors.Is(err, nats.ErrConnectionClosed) {
		return fmt.Errorf("notifier: unsubscribe: %w", err)
	}

	done := make(chan struct{})
	go func() {
		n.inFlight.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("notifier: handlers still running at drain deadline: %w", ctx.Err())
	}
}

// Stop releases the connection, after Drain has finished with it. Split
// from Drain because they answer different questions: Drain is "is the
// work done", Stop is "is the socket closed".
func (n *Notifier) Stop(context.Context) error {
	n.mu.Lock()
	conn := n.conn
	n.mu.Unlock()
	if conn == nil {
		return nil
	}
	conn.Close()
	return nil
}
