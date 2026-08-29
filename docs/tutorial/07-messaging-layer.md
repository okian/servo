# 7. Messaging layer

So far, every layer has only ever talked to the layer directly below it. This chapter is different:
we're building two independent components — a publisher and a subscriber — that never call each
other directly, and only agree on a contract. That's what makes it a good place to introduce
messaging as a concept, not just as a library.

## Write the contract first

Before either side exists, decide what they'll agree on: a subject name, and the shape of the
event. Create `broker/broker.go`:

```go
package broker

import (
	"context"

	"example.com/servoorders/domain"
)

const OrderPlacedSubject = "orders.placed"

type OrderPlacedEvent struct {
	OrderID string `json:"order_id"`
	UserID  string `json:"user_id"`
	Item    string `json:"item"`
}

type EventPublisher interface {
	PublishOrderPlaced(ctx context.Context, o *domain.Order) error
}
```

This is a slightly different situation than `repository` or `cache` from the last two chapters.
There, one interface has exactly one real implementation. Here, two components that will never
import each other — a publisher and a subscriber — both need this exact subject string and this
exact JSON shape, so the contract has to live somewhere neither of them owns exclusively:

```mermaid
flowchart LR
    Svc["OrderService"] -->|"PublishOrderPlaced"| Pub["natsbroker.Publisher"]
    Pub -->|"publish orders.placed"| NATS[("NATS")]
    NATS -->|"subscribe orders.placed"| Sub["notifier"]
```

## Build the publisher

Create a directory called `natsbroker/`, not `nats/`. Worth pausing on why: the client library
you're about to import, `github.com/nats-io/nats.go`, declares its own package as `nats`. Name your
own package `nats` too, and every file that needs both it and the client library ends up needing an
import alias on one of them — a small, permanent tax for no benefit. This is a real category of
naming collision, not specific to NATS; watch for it whenever a wrapper's natural name matches the
library it wraps.

`natsbroker/natsbroker.go`:

```go
package natsbroker

import (
	"context"
	"encoding/json"
	"fmt"

	"example.com/servoorders/broker"
	"example.com/servoorders/observability"
	"example.com/servoorders/config"
	"example.com/servoorders/domain"
	"github.com/nats-io/nats.go"
)

type Publisher struct {
	url  string
	conn *nats.Conn
}

var _ broker.EventPublisher = (*Publisher)(nil)

const envPrefix = "NATS_"

type Config struct {
	URL string `env:"URL,required"`
}

func NewConfig(src config.Source) (*Config, error) {
	return config.Parse[Config](src, envPrefix)
}

func New(cfg *Config) *Publisher {
	return &Publisher{url: cfg.URL}
}
```

The capability methods follow the pattern from the last two chapters, with NATS's own vocabulary
for each: `Drain` is NATS's graceful "finish in-flight work, then disconnect," and `IsConnected`
gives `Health` something real to check:

```go
func (p *Publisher) Init(context.Context) error {
	conn, err := nats.Connect(p.url)
	if err != nil {
		return fmt.Errorf("natsbroker: connect: %w", err)
	}
	p.conn = conn
	return nil
}

func (p *Publisher) Stop(context.Context) error {
	p.conn.Drain()
	return nil
}

func (p *Publisher) Health(context.Context) error {
	if !p.conn.IsConnected() {
		return fmt.Errorf("natsbroker: not connected (status: %s)", p.conn.Status())
	}
	return nil
}
```

And the one method the interface actually asked for:

```go
func (p *Publisher) PublishOrderPlaced(ctx context.Context, o *domain.Order) error {
	raw, err := json.Marshal(broker.OrderPlacedEvent{
		OrderID: o.ID.String(),
		UserID:  o.UserID.String(),
		Item:    o.Item,
	})
	if err != nil {
		return fmt.Errorf("natsbroker: marshal: %w", err)
	}
	if err := p.conn.Publish(broker.OrderPlacedSubject, raw); err != nil {
		return fmt.Errorf("natsbroker: publish: %w", err)
	}
	return nil
}
```

## Build the consuming side

A real second service would subscribe to `orders.placed` independently, in its own process. We're
building `notifier` in this same binary instead, purely so the event-driven half of this
architecture is visible and testable without standing up an actual second service. Create
`notifier/notifier.go`:

```go
package notifier

import (
	"context"
	"encoding/json"
	"fmt"

	"example.com/servoorders/broker"
	"example.com/servoorders/config"
	"example.com/servoorders/observability"
	"github.com/nats-io/nats.go"
)

// This package declares its own Config even though it wants the same
// NATS_URL natsbroker does. Both ends of the messaging layer connect to
// the same server, and each says so for itself rather than sharing a
// struct to agree on it — see chapter 3.
const envPrefix = "NATS_"

type Config struct {
	URL string `env:"URL,required"`
}

func NewConfig(src config.Source) (*Config, error) {
	return config.Parse[Config](src, envPrefix)
}

type Notifier struct {
	url string
	log *observability.Logger
}

func New(cfg *Config, log *observability.Logger) *Notifier {
	return &Notifier{url: cfg.URL, log: log}
}
```

Now `Run` — and this is the first component in the tutorial with no `Init` at all, which is worth
noticing as a deliberate choice, not a gap. There's nothing useful to do with a subscription except
hold it open until shutdown, so connecting happens inside `Run` itself:

```go
func (n *Notifier) Run(ctx context.Context) error {
	conn, err := nats.Connect(n.url)
	if err != nil {
		return fmt.Errorf("notifier: connect: %w", err)
	}
	defer conn.Drain()

	sub, err := conn.Subscribe(broker.OrderPlacedSubject, func(msg *nats.Msg) {
		var event broker.OrderPlacedEvent
		if err := json.Unmarshal(msg.Data, &event); err != nil {
			n.log.ErrorContext(ctx, "notifier: malformed event", "error", err)
			return
		}
		n.log.InfoContext(ctx, "order placed",
			"order_id", event.OrderID, "user_id", event.UserID, "item", event.Item)
	})
	if err != nil {
		return fmt.Errorf("notifier: subscribe: %w", err)
	}
	defer sub.Unsubscribe()

	<-ctx.Done()
	return nil
}
```

`Run` blocking on `<-ctx.Done()` is the standard shape for anything servo will detect as a
`Runner` — it returns when, and only when, the context it was given is cancelled, which is exactly
what lets servo's generated shutdown code wait for it to actually stop.

## The trade-off we're accepting here

Before moving on, it's worth being honest about a gap in what we just built. The service layer
(next chapter) will write an order to Postgres, then call `PublishOrderPlaced`, and won't fail the
whole request if that publish fails — it'll only log. That's a real, known limitation: if the
process crashes between the database commit and the publish, or NATS is briefly unreachable, that
event is simply lost, even though the order itself was correctly saved. This is the classic
**dual-write problem** — two systems can't be updated atomically without a distributed transaction,
and Postgres and NATS don't share one.

The real fix is the **transactional outbox pattern**: write the event to an `outbox` table inside
the *same* Postgres transaction as the order, then have a separate process read unpublished rows
and publish them, marking each sent only after a confirmed publish. That gets you at-least-once
delivery — at the cost of a background poller, and the possibility that a consumer sees the same
event twice, which any real at-least-once consumer has to be built to tolerate (idempotent
processing, keyed by `OrderID`). We're not building an outbox in this tutorial — see
[chapter 19](19-alternatives-and-further-reading.md#the-outbox-pattern) for what it would take —
because the failure mode being visible and understood is more valuable here than the machinery to
eliminate it.

## Try it against real NATS

```
$ make up
$ TEST_NATS_URL=nats://localhost:4222 go test ./natsbroker/... ./notifier/... -v
=== RUN   TestPublishOrderPlacedIsReceivedBySubscribers
--- PASS: TestPublishOrderPlacedIsReceivedBySubscribers (0.01s)
=== RUN   TestHealthReflectsConnectionState
--- PASS: TestHealthReflectsConnectionState (0.00s)
PASS
ok  	example.com/servoorders/natsbroker	0.160s
=== RUN   TestRunLogsReceivedEvents
--- PASS: TestRunLogsReceivedEvents (0.05s)
PASS
ok  	example.com/servoorders/notifier	0.201s
```

The notifier test is worth a look before you write it, because it has to solve a real timing
problem: `Run`'s subscription registers asynchronously, after it connects, so publishing
immediately after starting `Run` in a goroutine can race ahead of the subscription actually being
ready. Rather than guess at a fixed delay (flaky if too short, slow if too long), it swaps `slog`'s
default handler for one writing into a buffer, then retries the publish in a short loop until the
buffer shows the event arrived.

## Diagnostics

- **`nats: no responders`** — NATS core telling you nothing is subscribed to the subject at all
  (not a delivery failure to an existing subscriber). Check that `notifier` — or your test's own
  subscription — actually started before the publish happened; the retry loop above exists
  specifically because this is a real race, not a hypothetical one.
- **`notifier` never logs anything even though publishing succeeds** — check the subject strings
  match *exactly*. Using `broker.OrderPlacedSubject` on both sides, instead of a hardcoded string on
  one, is what turns a typo here into a single-source-of-truth problem the compiler helps with,
  rather than a silent runtime mismatch.
- **An event arrives twice for one order** — shouldn't happen under what we built here (one
  `Publish` call per `CreateOrder`), but it's the first thing to check if an outbox poller is ever
  added: at-least-once delivery means *some* duplication is expected.

## Do's and don'ts

- **Do** keep the event payload minimal — an ID and just enough to route or filter on — rather than
  the full `domain.Order`. A consumer that needs the full order can fetch it; a wide payload becomes
  a compatibility burden the moment a second consumer depends on fields the first one didn't need.
- **Do** version your subjects or event schemas once more than one consumer exists in production.
  You can't coordinate a breaking payload change across every consumer's deploy the way you can
  within one service's own release.
- **Don't** assume a publish inside a database transaction is covered by that transaction's
  rollback — a message broker doesn't participate in a SQL transaction. If the publish genuinely
  needs to be transactionally consistent with the write, the outbox pattern above is the real
  answer, not call ordering.
- **Don't** let a subscriber's handler block indefinitely or panic — `notifier`'s handler
  deliberately does neither. A slow or panicking handler stalls or crashes the whole subscription,
  not just the one message.

## Next

[Chapter 8: Service layer](08-service-layer.md) — where the repository, the cache, and the broker
all come together into one place that has to reason about all three at once.
