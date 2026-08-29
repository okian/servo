package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/okian/servo/v3/servo"
	"github.com/okian/servo/v3/servotest"

	"example.com/servoscoped/chat"
)

// These tests are the gate on the scopes feature, not cleanup after it. A
// golden file pins what the generator emits; nothing about a golden file
// can catch an instance torn down while a caller still holds it, an
// acquirer that spins forever against a dying entry, or a goroutine per
// room that outlives the room. Every test here runs under -race in CI and
// finishes with a goroutine-leak check.

// newApp builds the app and guarantees the whole scope is torn down before
// the leak check runs, whatever the test does in between.
//
// Both are registered as cleanups rather than deferred in the test body,
// and in this order: t.Cleanup runs LIFO and after the test function
// returns, so the leak check registered first here is the very last thing
// to run. A `defer servotest.NoLeaks(t)` in the test body would instead
// run *before* the shutdown below and report every still-live room as a
// leak.
func newApp(t *testing.T) (*App, context.Context, context.CancelFunc) {
	t.Helper()
	t.Cleanup(func() { servotest.NoLeaks(t) })

	ctx, cancel := context.WithCancel(context.Background())
	app, err := New(ctx)
	if err != nil {
		cancel()
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		if r := app.Shutdown(context.Background()); !r.Clean() {
			t.Errorf("shutdown was not clean: %v", r)
		}
		cancel()
		settle(t, app)
	})
	return app, ctx, cancel
}

// settle waits for the scope to reach a quiet state. Releases can arrive
// from a context.AfterFunc goroutine rather than from the caller's own
// defer, so "the last release has been observed" is a condition to wait
// for, not something the test can assert the instant it cancels.
func settle(t *testing.T, app *App) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s := app.rooms.Stats()
		if s.Live == 0 && s.Refs == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("scope never settled: %+v", app.rooms.Stats())
}

func roomCtx(ctx context.Context, key string) context.Context {
	return chat.WithRoom(ctx, chat.RoomKey(key))
}

// TestConcurrentAcquireOfOneColdKeyConstructsOnce is the central claim:
// everyone presenting the same key shares one instance, however many of
// them arrive at once and however cold the key is.
func TestConcurrentAcquireOfOneColdKeyConstructsOnce(t *testing.T) {
	app, ctx, _ := newApp(t)

	const n = 64
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	rooms := make([]*chat.Room, n)
	releases := make([]func(), n)

	for i := range n {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			room, release, err := app.rooms.Acquire(roomCtx(ctx, "general"))
			if err != nil {
				t.Errorf("Acquire: %v", err)
				return
			}
			rooms[i], releases[i] = room, release
		}()
	}
	start.Done()
	done.Wait()

	for i := 1; i < n; i++ {
		if rooms[i] != rooms[0] {
			t.Fatalf("goroutine %d got a different instance — the key was constructed more than once", i)
		}
	}
	if got := app.rooms.Stats(); got.Live != 1 || got.Refs != n {
		t.Fatalf("stats = %+v, want Live=1 Refs=%d", got, n)
	}

	for _, release := range releases {
		release()
	}
}

// TestReleaseEvictsAfterLinger pins the whole lifetime in one pass: the
// instance survives its last holder for the linger window, then drains,
// flushes and stops, and the map is empty afterwards.
func TestReleaseEvictsAfterLinger(t *testing.T) {
	servotest.Linger(t, 10*time.Millisecond)
	app, ctx, _ := newApp(t)

	room, release, err := app.rooms.Acquire(roomCtx(ctx, "general"))
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	room.Post("hello")
	release()

	// Still there, briefly: this is the difference between a linger window
	// and none at all.
	if got := app.rooms.Stats(); got.Live != 1 {
		t.Fatalf("stats = %+v immediately after release, want Live=1", got)
	}
	settle(t, app)
	if got := app.rooms.Stats(); got.Evictions != 1 {
		t.Fatalf("stats = %+v, want exactly one eviction", got)
	}
}

// TestReacquireWithinLingerKeepsState is the reason the window exists: a
// reconnect a few milliseconds after the last person left rejoins the same
// room rather than a fresh, empty one.
func TestReacquireWithinLingerKeepsState(t *testing.T) {
	servotest.Linger(t, time.Second)
	app, ctx, _ := newApp(t)

	room, release, err := app.rooms.Acquire(roomCtx(ctx, "general"))
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	room.Post("hello")
	release()

	again, release2, err := app.rooms.Acquire(roomCtx(ctx, "general"))
	if err != nil {
		t.Fatalf("re-Acquire: %v", err)
	}
	defer release2()

	if again != room {
		t.Fatal("re-acquire within the linger window built a new instance")
	}
	if got := again.Messages(); len(got) != 1 || got[0] != "hello" {
		t.Fatalf("messages = %v, want the state from before the release", got)
	}
}

// TestEvictionRacingAcquire drives the one interleaving a golden file can
// never reach: an acquirer finds an entry in the map at the exact moment
// that entry has decided to die. The join must be abortable and the retry
// must terminate, which it only does because the key is removed from the
// map before dead is closed.
func TestEvictionRacingAcquire(t *testing.T) {
	servotest.Linger(t, 0) // die with the last holder
	app, ctx, _ := newApp(t)

	const rounds = 300
	for range rounds {
		var wg sync.WaitGroup
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				room, release, err := app.rooms.Acquire(roomCtx(ctx, "hot"))
				if err != nil {
					t.Errorf("Acquire: %v", err)
					return
				}
				room.Post("x")
				release()
			}()
		}
		wg.Wait()
	}
	settle(t, app)
}

// TestHammerAcquireRelease is the broad soak: many keys, many goroutines,
// for a fixed wall-clock budget, ending with nothing live and nothing
// leaked.
func TestHammerAcquireRelease(t *testing.T) {
	servotest.Linger(t, time.Millisecond)
	app, ctx, _ := newApp(t)

	deadline := time.Now().Add(500 * time.Millisecond)
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; time.Now().Before(deadline); i++ {
				key := fmt.Sprintf("room-%d", (w+i)%16)
				room, release, err := app.rooms.Acquire(roomCtx(ctx, key))
				if err != nil {
					t.Errorf("Acquire(%s): %v", key, err)
					return
				}
				room.Post("msg")
				release()
			}
		}()
	}
	wg.Wait()
	settle(t, app)
}

// TestCancellationIsTheOnlyRelease covers the caller who forgets the
// closure entirely. The context.AfterFunc backstop has to free the
// instance anyway — later than a defer would, but not never.
func TestCancellationIsTheOnlyRelease(t *testing.T) {
	servotest.Linger(t, 0)
	app, appCtx, _ := newApp(t)

	reqCtx, cancelReq := context.WithCancel(appCtx)
	if _, _, err := app.rooms.Acquire(roomCtx(reqCtx, "forgotten")); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if got := app.rooms.Stats(); got.Refs != 1 {
		t.Fatalf("stats = %+v, want one outstanding reference", got)
	}

	cancelReq()
	settle(t, app)
}

// TestAcquireRejectsContextWithNoLifetime is the other half of that
// backstop: a context that can never be done would disable it silently, so
// it is refused instead.
func TestAcquireRejectsContextWithNoLifetime(t *testing.T) {
	app, _, _ := newApp(t)

	_, _, err := app.rooms.Acquire(roomCtx(context.Background(), "general"))
	if !errors.Is(err, servo.ErrNoLifetime) {
		t.Fatalf("err = %v, want servo.ErrNoLifetime", err)
	}
	if got := app.rooms.Stats(); got.Live != 0 {
		t.Fatalf("stats = %+v, want nothing created", got)
	}
}

// TestAcquireWithoutKeyFails is what stops every keyless caller from
// silently sharing the zero key's instance.
func TestAcquireWithoutKeyFails(t *testing.T) {
	app, ctx, _ := newApp(t)

	_, _, err := app.rooms.Acquire(ctx) // no chat.WithRoom
	if !errors.Is(err, servo.ErrNoScopeKey) {
		t.Fatalf("err = %v, want servo.ErrNoScopeKey", err)
	}
	if got := app.rooms.Stats(); got.Live != 0 {
		t.Fatalf("stats = %+v, want nothing created", got)
	}
}

// TestConstructorErrorLeavesNoEntry hits the rollback path from many
// goroutines at once: a failed build must remove its own key, so the next
// attempt is a fresh construction rather than a wait on an instance that
// will never exist.
func TestConstructorErrorLeavesNoEntry(t *testing.T) {
	app, ctx, _ := newApp(t)

	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := app.rooms.Acquire(roomCtx(ctx, "!reserved"))
			if !errors.Is(err, chat.ErrBadRoomName) {
				t.Errorf("err = %v, want chat.ErrBadRoomName", err)
			}
		}()
	}
	wg.Wait()

	if got := app.rooms.Stats(); got.Live != 0 || got.Refs != 0 {
		t.Fatalf("stats = %+v, want nothing left behind", got)
	}
	// The key is genuinely free afterwards, not poisoned.
	if _, release, err := app.rooms.Acquire(roomCtx(ctx, "fine")); err != nil {
		t.Fatalf("Acquire after failures: %v", err)
	} else {
		release()
	}
}

// TestManyDistinctKeys checks that a thousand rooms cost a thousand
// entries and then nothing — one goroutine per live entry is only
// acceptable if the goroutine actually goes away.
func TestManyDistinctKeys(t *testing.T) {
	servotest.Linger(t, 0)
	app, ctx, _ := newApp(t)

	const n = 1000
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			key := fmt.Sprintf("room-%d", i)
			room, release, err := app.rooms.Acquire(roomCtx(ctx, key))
			if err != nil {
				t.Errorf("Acquire(%s): %v", key, err)
				return
			}
			if room.Key() != chat.RoomKey(key) {
				t.Errorf("got room %q for key %q", room.Key(), key)
			}
			release()
		}()
	}
	wg.Wait()
	settle(t, app)

	if got := app.rooms.Stats(); got.Acquires != n || got.Evictions != n {
		t.Fatalf("stats = %+v, want %d acquires and %d evictions", got, n, n)
	}
}

// TestShutdownRacingAcquires is the case ordering alone does not cover: a
// Shutdown arriving while handlers are still acquiring. Every acquire must
// end in a live instance or ErrScopeClosed — never a hang, never an
// instance handed out after teardown began.
func TestShutdownRacingAcquires(t *testing.T) {
	app, ctx, _ := newApp(t)

	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			key := fmt.Sprintf("room-%d", i%4)
			room, release, err := app.rooms.Acquire(roomCtx(ctx, key))
			switch {
			case errors.Is(err, servo.ErrScopeClosed):
				return // the documented outcome once Shutdown has begun
			case err != nil:
				t.Errorf("Acquire(%s): %v", key, err)
				return
			}
			room.Post("racing")
			release()
		}()
	}

	report := app.Shutdown(context.Background())
	wg.Wait()
	if !report.Clean() {
		t.Fatalf("shutdown was not clean: %v", report)
	}
	settle(t, app)
}

// TestShutdownIsIdempotent covers the second Shutdown call finding a scope
// that has already closed its own quit channel.
func TestShutdownIsIdempotent(t *testing.T) {
	app, ctx, _ := newApp(t)

	_, release, err := app.rooms.Acquire(roomCtx(ctx, "general"))
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	release()

	first := app.Shutdown(context.Background())
	second := app.Shutdown(context.Background())
	if !first.Clean() || !second.Clean() {
		t.Fatalf("shutdown reports: first=%v second=%v", first, second)
	}
	if _, _, err := app.rooms.Acquire(roomCtx(ctx, "general")); !errors.Is(err, servo.ErrScopeClosed) {
		t.Fatalf("post-shutdown Acquire err = %v, want servo.ErrScopeClosed", err)
	}
	settle(t, app)
}

// TestEvictionDrainsFlushesAndStops asserts the teardown sequence itself,
// through the one observable that proves the order: a Room whose Stop runs
// before its Drain returns an error, and the RoomLog's buffer is only
// flushed if Flush ran after the messages were posted.
func TestEvictionDrainsFlushesAndStops(t *testing.T) {
	servotest.Linger(t, 0)
	app, ctx, _ := newApp(t)

	before := app.logger.Lines()
	room, release, err := app.rooms.Acquire(roomCtx(ctx, "general"))
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	room.Post("one")
	room.Post("two")
	release()
	settle(t, app)

	if got := app.logger.Lines() - before; got != 2 {
		t.Fatalf("flushed %d lines at eviction, want 2", got)
	}
}

// TestSingletonAndScopeShareOneLogger is the membership rule in one
// assertion: the logger is reached through the scope, but does not depend
// on the key, so it stays a single app-level instance rather than being
// rebuilt per room.
func TestSingletonAndScopeShareOneLogger(t *testing.T) {
	servotest.Linger(t, 0)
	app, ctx, _ := newApp(t)

	for _, key := range []string{"a", "b", "c"} {
		room, release, err := app.rooms.Acquire(roomCtx(ctx, key))
		if err != nil {
			t.Fatalf("Acquire(%s): %v", key, err)
		}
		room.Post("hi")
		release()
	}
	settle(t, app)

	// Three rooms, three flushes, one logger collecting all of them.
	if got := app.logger.Lines(); got != 3 {
		t.Fatalf("logger collected %d lines, want 3 — one per room, all through the same instance", got)
	}
}

// TestServerUsesTheAccessor exercises the consumer side end to end: a
// singleton that depends on the accessor interface, acquiring per call.
func TestServerUsesTheAccessor(t *testing.T) {
	servotest.Linger(t, 50*time.Millisecond)
	app, ctx, _ := newApp(t)

	general := roomCtx(ctx, "general")
	if err := app.server.Post(general, "hello"); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if err := app.server.Post(roomCtx(ctx, "random"), "elsewhere"); err != nil {
		t.Fatalf("Post: %v", err)
	}

	got, err := app.server.Messages(general)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(got) != 1 || got[0] != "hello" {
		t.Fatalf("general messages = %v, want just the one posted to general", got)
	}
}

// TestGraphReportsScopeAttribution pins the introspection surface: a
// scoped node says which scope it belongs to, and the scope says what it
// holds and what it merely borrows.
func TestGraphReportsScopeAttribution(t *testing.T) {
	app, _, _ := newApp(t)

	g := app.Graph()
	if len(g.Scopes) != 1 {
		t.Fatalf("Graph().Scopes = %v, want exactly one", g.Scopes)
	}
	s := g.Scopes[0]
	if s.Key != "example.com/servoscoped/chat.RoomKey" || s.Max != 10_000 {
		t.Fatalf("scope = %+v", s)
	}
	if len(s.Members) != 2 || len(s.Borrows) != 1 {
		t.Fatalf("scope members = %v, borrows = %v", s.Members, s.Borrows)
	}

	var scoped, singletons int
	for _, n := range g.Nodes {
		if n.Scope == "" {
			singletons++
			continue
		}
		scoped++
		if n.Scope != s.Key {
			t.Fatalf("node %s attributed to scope %q", n.Type, n.Scope)
		}
	}
	if scoped != 2 || singletons != 2 {
		t.Fatalf("got %d scoped and %d singleton nodes, want 2 and 2", scoped, singletons)
	}
}

// The tests below are regressions for defects found in review. Each one
// failed before the fix and describes the interleaving that produced it.

// TestShutdownWaitsForAnInFlightEviction is the one a golden file and a
// leak check both miss. An entry that has begun evicting has already left
// the scope's map, so a Shutdown that snapshots only the map would return
// while that instance's Drain and Flush were still running — against the
// singletons Shutdown is about to stop next. Every flush would then write
// through an already-stopped logger, or, in a real main(), never happen at
// all.
func TestShutdownWaitsForAnInFlightEviction(t *testing.T) {
	servotest.Linger(t, 0) // evict the instant the last holder releases
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	app, err := New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	const n = 64
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			room, release, err := app.rooms.Acquire(roomCtx(ctx, fmt.Sprintf("room-%d", i)))
			if err != nil {
				t.Errorf("Acquire: %v", err)
				return
			}
			room.Post("pending")
			release() // starts eviction immediately; the flush is still to come
		}()
	}
	wg.Wait()

	if r := app.Shutdown(context.Background()); !r.Clean() {
		t.Fatalf("shutdown was not clean: %v", r)
	}

	// The claim under test: by the time Shutdown returns, every instance
	// has finished tearing down. No settling, no polling.
	if got := app.rooms.Stats(); got.Live != 0 || got.Evictions != n {
		t.Fatalf("stats right after Shutdown = %+v, want Live=0 and %d evictions", got, n)
	}
	if got := app.logger.Lines(); got != n {
		t.Fatalf("logger collected %d lines, want %d — a flush ran after Shutdown returned", got, n)
	}
	servotest.NoLeaks(t)
}

// TestPanicInAConstructorBecomesAnError: net/http recovers a per-request
// panic and keeps serving, so a panicking constructor is reachable in an
// ordinary service. Two things must hold. The entry must not be orphaned —
// without the guard it stayed in the map with no loop goroutine, so every
// later acquire of that key blocked until its own context died and
// Shutdown waited forever. And the panic must not escape: on a
// multi-initializer level it would come from an errgroup goroutine no
// caller can recover, taking the process down. So it becomes this
// acquire's error instead.
func TestPanicInAConstructorBecomesAnError(t *testing.T) {
	servotest.Linger(t, 0)
	app, ctx, _ := newApp(t)

	_, _, err := app.rooms.Acquire(roomCtx(ctx, string(chat.PanicKey)))
	if err == nil || !strings.Contains(err.Error(), "panic constructing") {
		t.Fatalf("Acquire = %v, want an error naming the panic", err)
	}

	if got := app.rooms.Stats(); got.Live != 0 {
		t.Fatalf("stats = %+v, want the panicking key to have left nothing behind", got)
	}

	// The key is usable again, and — the part that hung before — this
	// returns rather than blocking on an entry nobody will ever tear down.
	reqCtx, cancelReq := context.WithTimeout(ctx, 2*time.Second)
	defer cancelReq()
	if _, _, err := app.rooms.Acquire(roomCtx(reqCtx, string(chat.PanicKey))); err == nil {
		t.Fatal("expected the second acquire to fail the same way")
	}
	if reqCtx.Err() != nil {
		t.Fatal("the second acquire blocked until its context expired — the entry was orphaned")
	}
}

// TestPanicRollsBackWhatWasBuilt: the panicking Room is constructed after
// its RoomLog, so the RoomLog exists and has to be flushed and stopped —
// the error path unrolls a known prefix, and the panic path has to reach
// the same place without knowing how far it got.
func TestPanicRollsBackWhatWasBuilt(t *testing.T) {
	servotest.Linger(t, 0)
	app, ctx, _ := newApp(t)

	before := app.logger.Lines()
	if _, _, err := app.rooms.Acquire(roomCtx(ctx, string(chat.PanicKey))); err == nil {
		t.Fatal("expected an error")
	}
	settle(t, app)

	// RoomLog.Flush logs nothing for an empty buffer, so the observable is
	// that the scope is clean and nothing leaked — checked by newApp's
	// cleanup. The line count must not have moved.
	if got := app.logger.Lines(); got != before {
		t.Fatalf("logger gained %d lines from a failed build", got-before)
	}
}

// TestMaxCountsInstancesNotMapEntries: an instance that has left the map
// but is still draining still holds its memory. Counting only the map
// makes Max a bound on map size, which is not the thing it exists to
// bound.
func TestMaxCountsInstancesNotMapEntries(t *testing.T) {
	servotest.Linger(t, 0)
	app, ctx, _ := newApp(t)

	// chat.SlowDrainKey drains slowly, so each of these is still tearing
	// down while the next is admitted.
	var held []func()
	for i := range 4 {
		_, release, err := app.rooms.Acquire(roomCtx(ctx, fmt.Sprintf("%s-%d", chat.SlowDrainKey, i)))
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		held = append(held, release)
	}
	for _, release := range held {
		release()
	}

	// Peak live count must never exceed the declared Max, whatever the
	// mix of mapped and tearing entries.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := app.rooms.Stats(); got.Live > 10_000 {
			t.Fatalf("live instances = %d, over the declared Max", got.Live)
		}
		if app.rooms.Stats().Live == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	settle(t, app)
}

// TestAcquireAfterShutdownNeverReturnsATornInstance: the creator used to
// return its freshly built instance without re-checking, so an Acquire
// that raced Shutdown could hand back — with a nil error — an instance
// already Drained and Stopped.
func TestAcquireAfterShutdownNeverReturnsATornInstance(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	app, err := New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var wg sync.WaitGroup
	for i := range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			room, release, err := app.rooms.Acquire(roomCtx(ctx, fmt.Sprintf("racing-%d", i)))
			switch {
			case errors.Is(err, servo.ErrScopeClosed):
				return
			case err != nil:
				t.Errorf("Acquire: %v", err)
				return
			}
			defer release()
			// A successful acquire must hand back a usable instance, not
			// one whose teardown already ran.
			if room.Stopped() {
				t.Error("Acquire returned a nil error for an instance that was already stopped")
			}
		}()
	}

	report := app.Shutdown(context.Background())
	wg.Wait()
	if !report.Clean() {
		t.Fatalf("shutdown was not clean: %v", report)
	}
	settle(t, app)
	servotest.NoLeaks(t)
}

// TestSharedKeyAcquireAfterShutdown is the same race as the test above on
// the other path into an instance. Every goroutine presents one key, so
// exactly one builds the entry and the other sixty-three join an existing
// one — and a join is a reference the entry's loop has already counted.
//
// The distinction matters because the two paths failed for different
// reasons. The creator returns an instance it built itself; a joiner
// returns one the loop handed it. Both used to be checked against the
// entry's death with a sample taken before the return, which cannot hold:
// the loop evicted on Shutdown whatever the reference count was, so the
// window between the check and the return was always open. Draining the
// outstanding references before teardown is what closes it, and it closes
// both paths at once.
func TestSharedKeyAcquireAfterShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	app, err := New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var wg sync.WaitGroup
	for range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			room, release, err := app.rooms.Acquire(roomCtx(ctx, "shared"))
			switch {
			case errors.Is(err, servo.ErrScopeClosed):
				return
			case err != nil:
				t.Errorf("Acquire: %v", err)
				return
			}
			defer release()
			if room.Stopped() {
				t.Error("a joiner's Acquire returned a nil error for an instance that was already stopped")
			}
		}()
	}

	report := app.Shutdown(context.Background())
	wg.Wait()
	if !report.Clean() {
		t.Fatalf("shutdown was not clean: %v", report)
	}
	settle(t, app)
	servotest.NoLeaks(t)
}

// TestDrainOverrunIsReportedAbandoned: a scope waits for outstanding
// holders before tearing an instance down, but only for one stop budget —
// a caller who never releases must not hold Shutdown open. When that bound
// is reached the instance is stopped anyway, and the only place that can
// show is the report, so it has to show there. A silently clean Shutdown
// would mean a broken promise left no trace.
func TestDrainOverrunIsReportedAbandoned(t *testing.T) {
	servotest.Timeout(t, 300*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	app, err := New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// The release closure is deliberately dropped, and ctx is not
	// cancelled, so the backstop cannot fire either: this reference is
	// never coming back.
	room, _, err := app.rooms.Acquire(roomCtx(ctx, "held"))
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	start := time.Now()
	report := app.Shutdown(context.Background())
	elapsed := time.Since(start)

	if report.Clean() {
		t.Error("shutdown reported clean after stopping an instance under a live holder")
	}
	var found bool
	for _, n := range report.Nodes {
		if n.Status == servo.StatusAbandoned && strings.Contains(n.Name, "chat.RoomKey") {
			found = true
		}
	}
	if !found {
		t.Errorf("no abandoned result for the scope: %v", report.Nodes)
	}
	if !room.Stopped() {
		t.Error("the instance was not torn down, so Shutdown did not terminate the drain")
	}
	// Bounded: the whole point of the budget is that one stuck holder
	// cannot hold shutdown open indefinitely.
	if elapsed > 3*time.Second {
		t.Errorf("shutdown took %v, far past the drain budget", elapsed)
	}
	servotest.NoLeaks(t)
}

// TestFailuresCountsAnUncleanEviction: an instance evicted mid-life has no
// Report to appear in, so without a counter a Stop that fails would leave
// no trace anywhere.
func TestFailuresCountsAnUncleanEviction(t *testing.T) {
	servotest.Linger(t, 0)
	app, ctx, _ := newApp(t)

	_, release, err := app.rooms.Acquire(roomCtx(ctx, string(chat.FailStopKey)))
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	release()
	settle(t, app)

	if got := app.rooms.Stats(); got.Failures != 1 {
		t.Fatalf("stats = %+v, want one failed eviction recorded", got)
	}
}

// TestConstructionFailureDoesNotDirtyShutdown: a bad key at the wrong
// instant used to fold a *construction* error into the shutdown report.
func TestConstructionFailureDoesNotDirtyShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	app, err := New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, _, err := app.rooms.Acquire(roomCtx(ctx, "!reserved")); !errors.Is(err, chat.ErrBadRoomName) {
		t.Fatalf("Acquire: %v, want chat.ErrBadRoomName", err)
	}

	if r := app.Shutdown(context.Background()); !r.Clean() {
		t.Fatalf("shutdown reported %v — a failed construction is not a failed teardown", r)
	}
	settle(t, app)
	servotest.NoLeaks(t)
}
