package emit

import (
	"fmt"
	"go/types"
	"sort"
	"strings"
	"time"

	"github.com/okian/servo/v3/internal/graph"
	"github.com/okian/servo/v3/internal/resolve"
)

// scopeEmit is one resolved scope plus every identifier the generated file
// needs for it. Names are allocated once, up front, so a module with two
// scopes whose key types share a short name gets readable suffixes rather
// than a compile error.
type scopeEmit struct {
	S *resolve.Scope

	TypeName   string // roomKeyScope
	EntryName  string // roomKeyEntry
	NewFunc    string // newRoomKeyScope
	Field      string // App field holding the scope
	StopMethod string // stopRoomKeyScope
	Roots      []*rootEmit
	Members    []*memberEmit
	// Borrowed is every singleton the scope's members and extractors
	// depend on. The scope must outlive them during shutdown, so they are
	// what its position in the teardown sequence is computed from.
	Borrowed []*resolve.Node
}

type rootEmit struct {
	R             *resolve.ScopeRoot
	AccessorType  string // roomsAccessor
	AccessorField string // App field holding the accessor
	AcquireMethod string // acquireRoom, on the scope
}

type memberEmit struct {
	N     *resolve.Node
	Field string // field on the entry
}

// planScopes allocates every scope-related identifier. It runs before any
// emission so that App field names, package-level type names, and entry
// field names are all settled and mutually non-colliding by the time
// anything references them.
func (e *emitter) planScopes() {
	if len(e.resolved.Scopes) == 0 {
		return
	}
	e.imports.Add("sync", "sync")
	e.imports.Add("sync/atomic", "atomic")
	e.imports.Add("time", "time")
	e.imports.Add("errors", "errors")
	e.imports.Add("fmt", "fmt")
	// Identifiers the emitted scope code hard-codes as parameters and
	// locals, claimed so a user package cannot take one and shadow it.
	e.imports.Reserve(scopeReservedIdents...)

	for _, s := range e.resolved.Scopes {
		base := scopeBaseName(s)
		se := &scopeEmit{S: s}
		se.TypeName = e.types.AllocateName(e.testPrefixed(base + "Scope"))
		se.EntryName = e.types.AllocateName(e.testPrefixed(base + "Entry"))
		se.NewFunc = e.types.AllocateName("new" + capitalize(e.testPrefixed(base+"Scope")))
		se.Field = e.names.AllocateName(base + "Scope")
		se.StopMethod = "stop" + capitalize(se.Field)

		// Seeded with every field writeEntryType hard-codes. A per-tenant
		// *tenant.Key in a tenant-keyed scope is an entirely ordinary type
		// to have, and would otherwise allocate the field name `key`,
		// which the entry already uses for the scope key itself.
		entryNames := NewNameAllocator()
		for _, reserved := range entryReservedFields {
			entryNames.AllocateName(reserved)
		}
		memberBase := baseNamesFor(s.Order)
		for _, n := range s.Order {
			se.Members = append(se.Members, &memberEmit{N: n, Field: allocateEntryField(entryNames, memberBase[n])})
		}
		for _, root := range s.Roots {
			ib := lowerFirst(shortTypeName(root.IfaceType))
			re := &rootEmit{R: root}
			re.AccessorType = e.types.AllocateName(e.testPrefixed(ib + "Accessor"))
			re.AccessorField = e.names.AllocateName(ib)
			re.AcquireMethod = e.acquireNames.AllocateName("acquire" + capitalize(lowerFirst(shortTypeName(root.Node.Provider.ResultType))))
			se.Roots = append(se.Roots, re)
		}
		se.Borrowed = borrowedSingletons(s)

		e.scopes = append(e.scopes, se)
		e.scopeByScope[s] = se
		for i, root := range s.Roots {
			e.rootByAccessor[root] = se.Roots[i]
		}
	}
	for _, se := range e.scopes {
		for _, m := range se.Members {
			e.memberField[m.N] = m.Field
		}
	}
}

// testPrefixed keeps the override variant's package-level declarations
// from colliding with the production file's. Both land in the same package
// — servo_gen.go and servo_gen_test.go — so App/TestApp is not enough on
// its own once a scope also contributes types and a constructor.
func (e *emitter) testPrefixed(name string) string {
	if !e.testMode {
		return name
	}
	return "test" + capitalize(name)
}

// borrowedSingletons is every non-scoped node a scope's members or
// extractors depend on, deduplicated, in the order the resolver
// constructed them.
func borrowedSingletons(s *resolve.Scope) []*resolve.Node {
	seen := map[*resolve.Node]bool{}
	var out []*resolve.Node
	add := func(n *resolve.Node) {
		if n == nil || n.Kind != resolve.NodeProvider || n.Scope != nil || seen[n] {
			return
		}
		seen[n] = true
		out = append(out, n)
	}
	for _, n := range s.Order {
		for _, d := range n.Deps {
			add(d)
		}
	}
	for _, root := range s.Roots {
		for _, d := range root.ExtractorDeps {
			add(d)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Level < out[j].Level })
	return out
}

// entryReservedFields is every field name writeEntryType emits itself. A
// member field colliding with one of them is a redeclaration the generator
// would happily write and the compiler would reject, so they are claimed
// before any member gets a name.
var entryReservedFields = []string{
	// Fields.
	"scope", "key", "ctx", "cancel", "built",
	"joins", "leaves", "ready", "dead", "torn",
	"buildErr", "stopResult", "drainResult", "runWG", "runMu", "runErrs",
	// Methods. In Go a field and a method share one namespace, so a
	// member type named Name would collide with the entry's own name().
	"name", "loop", "evict", "build", "rollback", "teardown",
	"waitRun", "waitTorn", "releaser", "drainRefs",
	// The entry receiver, and the locals build() declares. A member field
	// is referenced as `e.<field>`, but the local it is assigned through
	// is the bare name — so a member type E, A or Err would redeclare the
	// receiver or one of those locals.
	"e", "a", "err",
}

// scopeReservedIdents are the parameters and locals the emitted scope code
// names outright. A user package allowed to claim one of them would shadow
// it at exactly the point the generated code qualifies a type with it.
var scopeReservedIdents = []string{
	"app", "entry", "results", "entries", "live", "res",
	"refs", "timer", "linger", "stopTimer", "zero", "once",
	"release", "created", "started", "key", "err", "budget",
}

// baseNamesFor picks each node's field name before any is allocated, so a
// name two nodes would both want can be qualified by package rather than
// separated by a numeric suffix. Deciding this up front is what keeps the
// two consistent: whoever came first would otherwise keep the bare name
// and only the loser would say which package it came from.
func baseNamesFor(nodes []*resolve.Node) map[*resolve.Node]string {
	count := map[string]int{}
	for _, n := range nodes {
		count[baseName(n.Provider.ResultType)]++
	}
	out := make(map[*resolve.Node]string, len(nodes))
	for _, n := range nodes {
		base := baseName(n.Provider.ResultType)
		if count[base] > 1 {
			base = qualifiedBaseName(n.Provider.ResultType)
		}
		out[n] = base
	}
	return out
}

// allocateEntryField names one scope member's field on the entry, and
// claims the two method names writeEntryTeardown derives from that field
// as well.
//
// In Go a field and a method share one namespace, so reserving the field
// names alone is not enough: a member type named Result takes the field
// `result`, from which the teardown emitter derives `stopResult` — beside
// the entry's own hard-coded stopResult field. `servo generate` wrote that
// happily and the compiler rejected it, which is the worst place for the
// failure to land, since the file it lands in is one users are told not to
// read. Two members named Foo and StopFoo collide the same way.
func allocateEntryField(a *NameAllocator, base string) string {
	for {
		f := a.AllocateName(base)
		drain, stop := "drain"+capitalize(f), "stop"+capitalize(f)
		if a.Free(drain) && a.Free(stop) {
			a.AllocateName(drain)
			a.AllocateName(stop)
			return f
		}
	}
}

// allocateAppField is allocateEntryField's App-level twin. An App node's
// variable name decides a method (stop<F>) and two fields (<F>StopOnce,
// <F>StopResult), so components named Foo and StopFoo used to give App a
// stopFoo method beside a stopFoo field — the same field/method namespace
// collision, emitted just as happily and rejected just as firmly.
func allocateAppField(a *NameAllocator, base string) string {
	for {
		f := a.AllocateName(base)
		derived := []string{"stop" + capitalize(f), f + "StopOnce", f + "StopResult"}
		free := true
		for _, d := range derived {
			if !a.Free(d) {
				free = false
				break
			}
		}
		if free {
			for _, d := range derived {
				a.AllocateName(d)
			}
			return f
		}
	}
}

func scopeBaseName(s *resolve.Scope) string {
	return lowerFirst(shortTypeName(s.KeyType))
}

// scopeDecls renders every scope's registry, entry state machine and
// accessors. It is the only part of a generated file that is concurrent,
// stateful and timer-driven, so it carries more explanatory comment than
// the rest of the output: the reader who ends up here is debugging a race,
// not skimming construction order.
func (e *emitter) scopeDecls() string {
	var b strings.Builder
	for _, se := range e.scopes {
		e.writeScopeType(&b, se)
		e.writeScopeHelpers(&b, se)
		e.writeEntryType(&b, se)
		e.writeEntryLoop(&b, se)
		e.writeEntryBuild(&b, se)
		e.writeEntryTeardown(&b, se)
		for _, re := range se.Roots {
			e.writeAcquire(&b, se, re)
			e.writeAccessorType(&b, se, re)
		}
	}
	return b.String()
}

func (e *emitter) writeScopeType(b *strings.Builder, se *scopeEmit) {
	key := e.qualifiedTypeString(se.S.KeyType)
	fmt.Fprintf(b, "// %s is the registry for %s. One entry per live key, each\n", se.TypeName, se.S.Name)
	fmt.Fprintf(b, "// owning its own reference count and linger timer in its own goroutine.\n")
	fmt.Fprintf(b, "type %s struct {\n", se.TypeName)
	fmt.Fprintf(b, "\tapp *%s\n", e.appType())
	b.WriteString("\tmu     sync.RWMutex\n")
	fmt.Fprintf(b, "\titems  map[%s]*%s\n", key, se.EntryName)
	b.WriteString("\tclosed bool\n")
	b.WriteString("\t// quit is closed once, by Shutdown, and read by every entry loop.\n")
	b.WriteString("\tquit chan struct{}\n\n")
	b.WriteString("\t// base is New's context with cancellation stripped. An instance\n")
	b.WriteString("\t// outlives the request that created it, so hanging its Run loop off\n")
	b.WriteString("\t// an acquirer's context would kill it the moment that one caller\n")
	b.WriteString("\t// disconnects, while the instance is still live and referenced.\n")
	b.WriteString("\t// Values propagate; cancellation does not.\n")
	b.WriteString("\tbase   context.Context\n")
	b.WriteString("\tlinger time.Duration\n")
	b.WriteString("\tmax    int\n\n")
	b.WriteString("\trefs      atomic.Int64\n")
	b.WriteString("\tacquires  atomic.Uint64\n")
	b.WriteString("\tevictions atomic.Uint64\n")
	b.WriteString("\t// failures counts evictions whose teardown did not come out clean.\n")
	b.WriteString("\t// An instance evicted mid-life has no Report to appear in — Shutdown\n")
	b.WriteString("\t// is not running — so without this a Drain that failed or a Stop that\n")
	b.WriteString("\t// overran its budget would leave no trace anywhere.\n")
	b.WriteString("\tfailures atomic.Uint64\n")
	fmt.Fprintf(b, "\t// tearing holds entries that have left items but are still draining\n")
	b.WriteString("\t// and stopping. Removal has to happen first, so a racing acquirer's\n")
	b.WriteString("\t// retry misses — but an entry in this window is still very much an\n")
	b.WriteString("\t// instance: Shutdown has to wait for it, Max has to count it, and\n")
	b.WriteString("\t// Stats must not report the scope quiet while it is running.\n")
	fmt.Fprintf(b, "\ttearing map[*%s]struct{}\n", se.EntryName)
	b.WriteString("}\n\n")

	fmt.Fprintf(b, "func %s(ctx context.Context, app *%s) *%s {\n", se.NewFunc, e.appType(), se.TypeName)
	fmt.Fprintf(b, "\treturn &%s{\n", se.TypeName)
	b.WriteString("\t\tapp:   app,\n")
	fmt.Fprintf(b, "\t\titems:   map[%s]*%s{},\n", key, se.EntryName)
	fmt.Fprintf(b, "\t\ttearing: map[*%s]struct{}{},\n", se.EntryName)
	b.WriteString("\t\tquit:  make(chan struct{}),\n")
	b.WriteString("\t\tbase:  context.WithoutCancel(ctx),\n")
	fmt.Fprintf(b, "\t\tlinger: %s.LingerWindow(%s),\n", e.servoAlias, durationLiteral(se.S.Linger))
	fmt.Fprintf(b, "\t\tmax:    %d,\n", se.S.Max)
	b.WriteString("\t}\n}\n\n")
}

func (e *emitter) writeScopeHelpers(b *strings.Builder, se *scopeEmit) {
	key := e.qualifiedTypeString(se.S.KeyType)

	fmt.Fprintf(b, "func (s *%s) Stats() %s.ScopeStats {\n", se.TypeName, e.servoAlias)
	b.WriteString("\ts.mu.RLock()\n\tlive := s.liveLocked()\n\ts.mu.RUnlock()\n")
	fmt.Fprintf(b, "\treturn %s.ScopeStats{Live: live, Refs: int(s.refs.Load()), Acquires: s.acquires.Load(), Evictions: s.evictions.Load(), Failures: s.failures.Load()}\n", e.servoAlias)
	b.WriteString("}\n\n")

	b.WriteString("// liveLocked is how many instances exist: mapped, plus those that have\n")
	b.WriteString("// left the map but are still tearing down. Callers hold s.mu.\n")
	fmt.Fprintf(b, "func (s *%s) liveLocked() int { return len(s.items) + len(s.tearing) }\n\n", se.TypeName)

	b.WriteString("// lookupOrCreate takes the read lock on the hit path and the write lock,\n")
	b.WriteString("// with a second look, on the miss path: sync.RWMutex has no atomic\n")
	b.WriteString("// upgrade, so two cold acquires of the same key would otherwise both\n")
	b.WriteString("// create, orphaning one entry and the goroutine it was about to start.\n")
	fmt.Fprintf(b, "func (s *%s) lookupOrCreate(key %s) (*%s, bool, error) {\n", se.TypeName, key, se.EntryName)
	b.WriteString("\ts.mu.RLock()\n\te, ok := s.items[key]\n\tclosed := s.closed\n\ts.mu.RUnlock()\n")
	fmt.Fprintf(b, "\tif closed {\n\t\treturn nil, false, %s.ErrScopeClosed\n\t}\n", e.servoAlias)
	b.WriteString("\tif ok {\n\t\treturn e, false, nil\n\t}\n\n")
	b.WriteString("\ts.mu.Lock()\n\tdefer s.mu.Unlock()\n")
	b.WriteString("\tif e, ok := s.items[key]; ok {\n\t\treturn e, false, nil\n\t}\n")
	fmt.Fprintf(b, "\tif s.closed {\n\t\treturn nil, false, %s.ErrScopeClosed\n\t}\n", e.servoAlias)
	b.WriteString("\t// Counts tearing entries too: they still hold their instance and its\n\t// memory, so admitting past them would make Max a bound on map size\n\t// rather than on live instances.\n")
	fmt.Fprintf(b, "\tif s.liveLocked() >= s.max {\n\t\treturn nil, false, %s.ErrScopeFull\n\t}\n", e.servoAlias)
	b.WriteString("\tctx, cancel := context.WithCancel(s.base)\n")
	fmt.Fprintf(b, "\te = &%s{\n", se.EntryName)
	b.WriteString("\t\tscope: s, key: key, ctx: ctx, cancel: cancel,\n")
	b.WriteString("\t\tjoins: make(chan struct{}), leaves: make(chan struct{}),\n")
	b.WriteString("\t\tready: make(chan struct{}), dead: make(chan struct{}), torn: make(chan struct{}),\n")
	b.WriteString("\t}\n\ts.items[key] = e\n\treturn e, true, nil\n}\n\n")

	b.WriteString("// remove deletes key only while it still maps to this exact entry. A\n")
	b.WriteString("// dying entry is never revived — a new incarnation is a new entry with a\n")
	b.WriteString("// new goroutine — and that replacement may already have claimed the key.\n")
	fmt.Fprintf(b, "func (s *%s) remove(key %s, e *%s) {\n", se.TypeName, key, se.EntryName)
	b.WriteString("\ts.mu.Lock()\n\tif s.items[key] == e {\n\t\tdelete(s.items, key)\n\t}\n\ts.mu.Unlock()\n}\n\n")

	b.WriteString("// beginTeardown moves an entry out of the map and into tearing in one\n")
	b.WriteString("// step. Both have to happen under the same lock: an entry visible in\n")
	b.WriteString("// neither set is one Shutdown would not wait for and Max would not count.\n")
	fmt.Fprintf(b, "func (s *%s) beginTeardown(e *%s) {\n", se.TypeName, se.EntryName)
	b.WriteString("\ts.mu.Lock()\n")
	b.WriteString("\ts.tearing[e] = struct{}{}\n")
	b.WriteString("\tif s.items[e.key] == e {\n\t\tdelete(s.items, e.key)\n\t}\n")
	b.WriteString("\ts.mu.Unlock()\n}\n\n")

	b.WriteString("// endTeardown drops the entry and counts the eviction in one critical\n")
	b.WriteString("// section. Counting after the unlock would let Live reach zero before\n")
	b.WriteString("// Evictions was bumped — and since Live reaching zero is the\n")
	b.WriteString("// documented way to wait for a scope to go quiet, a test that waited\n")
	b.WriteString("// that way and then read Evictions could observe the instance gone\n")
	b.WriteString("// and its eviction uncounted.\n")
	fmt.Fprintf(b, "func (s *%s) endTeardown(e *%s, result %s.NodeResult) {\n", se.TypeName, se.EntryName, e.servoAlias)
	b.WriteString("\ts.mu.Lock()\n\tdelete(s.tearing, e)\n")
	b.WriteString("\ts.evictions.Add(1)\n")
	fmt.Fprintf(b, "\tif result.Status != %s.StatusOK {\n\t\ts.failures.Add(1)\n\t}\n", e.servoAlias)
	b.WriteString("\ts.mu.Unlock()\n}\n\n")

	b.WriteString("// abandon unwinds an entry whose construction failed, so it leaves\n")
	b.WriteString("// nothing behind in the map and any waiter gets the error rather than\n")
	b.WriteString("// blocking on an instance that will never exist.\n")
	fmt.Fprintf(b, "func (s *%s) abandon(e *%s, err error) error {\n", se.TypeName, se.EntryName)
	b.WriteString("\ts.remove(e.key, e)\n\te.cancel()\n\te.buildErr = err\n")
	b.WriteString("\t// stopResult stays OK: nothing was constructed, so nothing was torn\n")
	b.WriteString("\t// down. The error goes to the acquirer that caused it — folding it\n")
	b.WriteString("\t// into Shutdown's report as well would make one bad key at the wrong\n")
	b.WriteString("\t// instant render the whole shutdown dirty.\n")
	fmt.Fprintf(b, "\te.stopResult = %s.NodeResult{Name: e.name(), Status: %s.StatusOK}\n", e.servoAlias, e.servoAlias)
	b.WriteString("\tclose(e.dead)\n\tclose(e.torn)\n\treturn err\n}\n\n")
}

func (e *emitter) writeEntryType(b *strings.Builder, se *scopeEmit) {
	fmt.Fprintf(b, "// %s is one live key's instances plus the channels its loop\n", se.EntryName)
	b.WriteString("// selects on. Everything mutable about the reference count lives inside\n")
	b.WriteString("// that loop as a local variable, so there is nothing here to lock.\n")
	fmt.Fprintf(b, "type %s struct {\n", se.EntryName)
	fmt.Fprintf(b, "\tscope  *%s\n", se.TypeName)
	fmt.Fprintf(b, "\tkey    %s\n", e.qualifiedTypeString(se.S.KeyType))
	b.WriteString("\tctx    context.Context\n\tcancel context.CancelFunc\n\n")
	b.WriteString("\tjoins  chan struct{}\n")
	b.WriteString("\tleaves chan struct{}\n")
	b.WriteString("\t// ready closes when construction succeeded; dead closes when the key\n")
	b.WriteString("\t// has been removed from the map, which happens strictly first so a\n")
	b.WriteString("\t// racing acquirer's retry misses and creates fresh instead of finding\n")
	b.WriteString("\t// the same corpse forever. torn closes when teardown has finished.\n")
	b.WriteString("\tready chan struct{}\n\tdead chan struct{}\n\ttorn chan struct{}\n\n")
	b.WriteString("\t// built is how many members build() has finished assigning. The\n")
	b.WriteString("\t// error path unrolls a known prefix; the panic path cannot know\n")
	b.WriteString("\t// how far it got, so it reads this.\n")
	b.WriteString("\tbuilt      int\n")
	b.WriteString("\tbuildErr   error\n")
	fmt.Fprintf(b, "\tstopResult %s.NodeResult\n", e.servoAlias)
	b.WriteString("\t// drainResult is set only when the reference drain overran its\n")
	b.WriteString("\t// budget, so this instance was stopped while a caller still held\n")
	b.WriteString("\t// it. Written by the entry's own loop and read by teardown on the\n")
	b.WriteString("\t// same goroutine, so it needs no lock.\n")
	fmt.Fprintf(b, "\tdrainResult %s.NodeResult\n", e.servoAlias)
	b.WriteString("\trunWG   sync.WaitGroup\n\trunMu   sync.Mutex\n\trunErrs []error\n")
	for _, m := range se.Members {
		fmt.Fprintf(b, "\t%s %s\n", m.Field, e.qualifiedTypeString(m.N.Provider.ResultType))
		if m.N.Provider.HasCleanup {
			fmt.Fprintf(b, "\t%sCleanup func()\n", m.Field)
		}
	}
	b.WriteString("}\n\n")

	fmt.Fprintf(b, "func (e *%s) name() string { return fmt.Sprintf(\"%%s[%%v]\", %q, e.key) }\n\n", se.EntryName, se.S.Name)
}

func (e *emitter) writeEntryLoop(b *strings.Builder, se *scopeEmit) {
	b.WriteString("// loop owns this entry's reference count. A join is acknowledged by the\n")
	b.WriteString("// receive itself, so an acquirer knows its reference is counted before it\n")
	b.WriteString("// returns, and the loop never blocks on a caller that has walked away.\n")
	b.WriteString("//\n")
	b.WriteString("// It starts at one, not zero: whoever built this entry holds the first\n")
	b.WriteString("// reference by construction and never sends a join for it. Starting at\n")
	b.WriteString("// zero would leave an entry whose creator gave up before joining alive\n")
	b.WriteString("// with no reference and no timer armed — live forever, held by nobody.\n")
	fmt.Fprintf(b, "func (e *%s) loop() {\n", se.EntryName)
	b.WriteString("\trefs := 1\n\tvar timer *time.Timer\n\tvar linger <-chan time.Time\n")
	b.WriteString("\tstopTimer := func() {\n\t\tif timer != nil {\n\t\t\ttimer.Stop()\n\t\t\ttimer = nil\n\t\t}\n\t\tlinger = nil\n\t}\n")
	b.WriteString("\tfor {\n")
	b.WriteString("\t\t// Checked before the blocking select, so that once Shutdown has\n")
	b.WriteString("\t\t// closed quit this loop stops accepting joins instead of racing\n")
	b.WriteString("\t\t// them. A join and a quit both ready in one select is a coin\n")
	b.WriteString("\t\t// flip, and losing it hands an acquirer an instance this\n")
	b.WriteString("\t\t// iteration is about to tear down.\n")
	b.WriteString("\t\tselect {\n\t\tcase <-e.scope.quit:\n\t\t\tstopTimer()\n\t\t\te.drainRefs(refs)\n\t\t\te.evict()\n\t\t\treturn\n\t\tdefault:\n\t\t}\n\n")
	b.WriteString("\t\tselect {\n")
	b.WriteString("\t\tcase <-e.joins:\n\t\t\trefs++\n\t\t\tstopTimer()\n")
	b.WriteString("\t\tcase <-e.leaves:\n\t\t\trefs--\n\t\t\tif refs == 0 {\n")
	b.WriteString("\t\t\t\t// The window before eviction. Without it a short handler\n")
	b.WriteString("\t\t\t\t// takes the count 0->1->0 per request and the instance is\n")
	b.WriteString("\t\t\t\t// rebuilt every time, losing whatever in-memory state made\n")
	b.WriteString("\t\t\t\t// it worth sharing.\n")
	b.WriteString("\t\t\t\tstopTimer()\n\t\t\t\ttimer = time.NewTimer(e.scope.linger)\n\t\t\t\tlinger = timer.C\n\t\t\t}\n")
	b.WriteString("\t\tcase <-linger:\n\t\t\ttimer, linger = nil, nil\n\t\t\te.evict()\n\t\t\treturn\n")
	b.WriteString("\t\tcase <-e.scope.quit:\n\t\t\tstopTimer()\n\t\t\te.drainRefs(refs)\n\t\t\te.evict()\n\t\t\treturn\n")
	b.WriteString("\t\t}\n\t}\n}\n\n")

	b.WriteString("// drainRefs waits for the holders outstanding when Shutdown arrived to\n")
	b.WriteString("// give their references back before teardown starts. A counted\n")
	b.WriteString("// reference is a promise that the instance stays usable until it is\n")
	b.WriteString("// released, and evicting under a live holder would break that promise\n")
	b.WriteString("// for the one caller who did everything right: acquired successfully,\n")
	b.WriteString("// a moment before someone else called Shutdown.\n")
	b.WriteString("//\n")
	b.WriteString("// In the ordinary case there is nothing to wait for. Shutdown stops\n")
	b.WriteString("// nodes in reverse level order, so whatever depends on this scope has\n")
	b.WriteString("// already been drained and stopped, its handlers have returned and\n")
	b.WriteString("// their releases have already landed.\n")
	b.WriteString("//\n")
	b.WriteString("// Bounded by one stop budget, because a caller who never releases must\n")
	b.WriteString("// not hold shutdown open. Overrunning that bound is recorded in\n")
	b.WriteString("// drainResult, which teardown merges, so the entry comes back\n")
	b.WriteString("// abandoned: the report is the only place a promise broken this way\n")
	b.WriteString("// could show, and a clean one would hide it entirely.\n")
	fmt.Fprintf(b, "func (e *%s) drainRefs(refs int) {\n", se.EntryName)
	b.WriteString("\tif refs <= 0 {\n\t\treturn\n\t}\n")
	fmt.Fprintf(b, "\tbudget := time.NewTimer(%s.DefaultStopBudget)\n", e.servoAlias)
	b.WriteString("\tdefer budget.Stop()\n")
	b.WriteString("\tfor refs > 0 {\n")
	b.WriteString("\t\tselect {\n")
	b.WriteString("\t\tcase <-e.leaves:\n\t\t\trefs--\n")
	b.WriteString("\t\tcase <-budget.C:\n")
	fmt.Fprintf(b, "\t\t\terr := fmt.Errorf(\"servo: torn down with %%d reference(s) still held after %%s\", refs, %s.DefaultStopBudget)\n", e.servoAlias)
	fmt.Fprintf(b, "\t\t\te.drainResult = %s.NodeResult{Name: e.name(), Status: %s.StatusAbandoned, Err: err}\n", e.servoAlias, e.servoAlias)
	b.WriteString("\t\t\treturn\n")
	b.WriteString("\t\t}\n\t}\n}\n\n")

	b.WriteString("// evict removes the key from the map BEFORE announcing the entry's death.\n")
	b.WriteString("// Reversed, an acquirer that lost the join race would retry, look the key\n")
	b.WriteString("// up, find this same dying entry, and never terminate.\n")
	fmt.Fprintf(b, "func (e *%s) evict() {\n", se.EntryName)
	b.WriteString("\te.scope.beginTeardown(e)\n\tclose(e.dead)\n")
	b.WriteString("\te.stopResult = e.teardown()\n")
	b.WriteString("\te.scope.endTeardown(e, e.stopResult)\n\tclose(e.torn)\n}\n\n")
}

func (e *emitter) writeEntryBuild(b *strings.Builder, se *scopeEmit) {
	fmt.Fprintf(b, "// build constructs this entry's instances and runs their Init phase, in\n")
	b.WriteString("// the same level order and with the same rollback shape as the App's own\n")
	b.WriteString("// constructor. It runs in the acquiring goroutine, not under the scope's\n")
	b.WriteString("// lock, so one slow constructor cannot freeze every other key.\n")
	fmt.Fprintf(b, "func (e *%s) build() error {\n", se.EntryName)
	if scopeUsesApp(se) {
		b.WriteString("\ta := e.scope.app\n\n")
	}

	for i, m := range se.Members {
		e.writeMemberConstruction(b, se, i, m)
	}
	e.writeScopeInitPhase(b, se)
	e.writeScopeRunLaunch(b, se)
	b.WriteString("\treturn nil\n}\n\n")
}

func (e *emitter) writeMemberConstruction(b *strings.Builder, se *scopeEmit, index int, m *memberEmit) {
	args := make([]string, len(m.N.Deps))
	for i, dep := range m.N.Deps {
		args[i] = e.entryArg(dep)
	}
	call := fmt.Sprintf("%s(%s)", e.qualifiedFuncString(m.N.Provider), strings.Join(args, ", "))
	name := m.Field

	switch {
	case m.N.Provider.HasCleanup && m.N.Provider.HasError:
		fmt.Fprintf(b, "\t%s, %sCleanup, err := %s\n", name, name, call)
	case m.N.Provider.HasCleanup:
		fmt.Fprintf(b, "\t%s, %sCleanup := %s\n", name, name, call)
	case m.N.Provider.HasError:
		fmt.Fprintf(b, "\t%s, err := %s\n", name, call)
	default:
		fmt.Fprintf(b, "\t%s := %s\n", name, call)
	}
	if m.N.Provider.HasError {
		b.WriteString("\tif err != nil {\n")
		e.writeScopeRollback(b, se, index)
		b.WriteString("\t\treturn err\n\t}\n")
	}
	fmt.Fprintf(b, "\te.%s = %s\n", name, name)
	if m.N.Provider.HasCleanup {
		fmt.Fprintf(b, "\te.%sCleanup = %sCleanup\n", name, name)
	}
	fmt.Fprintf(b, "\te.built = %d\n\n", index+1)
}

// writeScopeRollback stops members 0..index-1 in reverse — the set known
// to have been constructed before this failure — by literal, unrolled
// calls, exactly as the App's own construction rollback does.
func (e *emitter) writeScopeRollback(b *strings.Builder, se *scopeEmit, index int) {
	for i := index - 1; i >= 0; i-- {
		e.writeMemberRollback(b, se.Members[i])
	}
}

// writeMemberRollback unwinds one already-built member, drain included, so
// an instance abandoned during Init is stopped through exactly the same
// sequence a live one is.
func (e *emitter) writeMemberRollback(b *strings.Builder, m *memberEmit) {
	if hasCapability(m.N, "Drainer") {
		fmt.Fprintf(b, "\t\te.drain%s(context.Background())\n", capitalize(m.Field))
	}
	if e.needsStopMethod(m.N) {
		fmt.Fprintf(b, "\t\te.stop%s(context.Background())\n", capitalize(m.Field))
	}
}

func (e *emitter) writeScopeInitPhase(b *strings.Builder, se *scopeEmit) {
	nodes := make([]*resolve.Node, 0, len(se.Members))
	for _, m := range se.Members {
		nodes = append(nodes, m.N)
	}
	groups := scopeLevelGroups(nodes, func(n *resolve.Node) bool { return hasCapability(n, "Initializer") })
	for _, level := range groups {
		if len(level) == 1 {
			fmt.Fprintf(b, "\tif err := e.%s.Init(e.ctx); err != nil {\n", e.memberField[level[0]])
			e.writeInitRollback(b, se)
			b.WriteString("\t\treturn err\n\t}\n")
			continue
		}
		e.imports.Add("golang.org/x/sync/errgroup", "errgroup")
		b.WriteString("\t{\n\t\tg, gctx := errgroup.WithContext(e.ctx)\n")
		b.WriteString("\t\t// Recovered inside the goroutine: errgroup does not, and a\n")
		b.WriteString("\t\t// panic escaping here would take the process down from a\n")
		b.WriteString("\t\t// goroutine no caller can recover — where the same panic on a\n")
		b.WriteString("\t\t// single-node level would simply fail this one acquire.\n")
		for _, n := range level {
			fmt.Fprintf(b, "\t\tg.Go(func() (err error) {\n")
			fmt.Fprintf(b, "\t\t\tdefer func() {\n\t\t\t\tif r := recover(); r != nil {\n\t\t\t\t\terr = fmt.Errorf(%q, r)\n\t\t\t\t}\n\t\t\t}()\n", "servo: panic in Init of "+n.Key.String()+": %v")
			fmt.Fprintf(b, "\t\t\treturn e.%s.Init(gctx)\n\t\t})\n", e.memberField[n])
		}
		b.WriteString("\t\tif err := g.Wait(); err != nil {\n")
		e.writeInitRollback(b, se)
		b.WriteString("\t\t\treturn err\n\t\t}\n\t}\n")
	}
}

// writeInitRollback stops every member, in reverse construction order.
// Unlike the construction phase above, every instance exists by the time
// Init begins, so there is no prefix to compute.
func (e *emitter) writeInitRollback(b *strings.Builder, se *scopeEmit) {
	for i := len(se.Members) - 1; i >= 0; i-- {
		e.writeMemberRollback(b, se.Members[i])
	}
}

func (e *emitter) writeScopeRunLaunch(b *strings.Builder, se *scopeEmit) {
	var runners []*memberEmit
	for _, m := range se.Members {
		if hasCapability(m.N, "Runner") {
			runners = append(runners, m)
		}
	}
	if len(runners) == 0 {
		return
	}
	b.WriteString("\n\t// One goroutine per running instance, on the entry's own context. A\n")
	b.WriteString("\t// Run that returns because that context was cancelled is a normal\n")
	b.WriteString("\t// teardown, not a failure, and is not reported as one.\n")
	for _, m := range runners {
		fmt.Fprintf(b, "\te.runWG.Add(1)\n\tgo func() {\n\t\tdefer e.runWG.Done()\n\t\tif err := e.%s.Run(e.ctx); err != nil && !errors.Is(err, context.Canceled) {\n", m.Field)
		b.WriteString("\t\t\te.runMu.Lock()\n\t\t\te.runErrs = append(e.runErrs, err)\n\t\t\te.runMu.Unlock()\n\t\t}\n\t}()\n")
	}
	b.WriteString("\n")
}

func (e *emitter) writeEntryTeardown(b *strings.Builder, se *scopeEmit) {
	// Per-member stop, mirroring the App's own stop methods, so the
	// rollback paths above have something to call.
	for _, m := range se.Members {
		if !e.needsStopMethod(m.N) {
			continue
		}
		label := m.N.Key.String()
		if hasCapability(m.N, "Drainer") {
			fmt.Fprintf(b, "func (e *%s) drain%s(ctx context.Context) []%s.NodeResult {\n", se.EntryName, capitalize(m.Field), e.servoAlias)
			fmt.Fprintf(b, "\treturn []%s.NodeResult{%s.RunStop(ctx, %s.DefaultStopBudget, %q, e.%s.Drain)}\n}\n\n", e.servoAlias, e.servoAlias, e.servoAlias, label, m.Field)
		}
		fmt.Fprintf(b, "func (e *%s) stop%s(ctx context.Context) []%s.NodeResult {\n", se.EntryName, capitalize(m.Field), e.servoAlias)
		fmt.Fprintf(b, "\tvar results []%s.NodeResult\n", e.servoAlias)
		if hasCapability(m.N, "Flusher") {
			fmt.Fprintf(b, "\tresults = append(results, %s.RunStop(ctx, %s.DefaultStopBudget, %q, e.%s.Flush))\n", e.servoAlias, e.servoAlias, label, m.Field)
		}
		if hasCapability(m.N, "Finalizer") {
			fmt.Fprintf(b, "\tresults = append(results, %s.RunStop(ctx, %s.DefaultStopBudget, %q, e.%s.Stop))\n", e.servoAlias, e.servoAlias, label, m.Field)
		}
		if m.N.Provider.HasCleanup {
			fmt.Fprintf(b, "\tresults = append(results, %s.RunStop(ctx, %s.DefaultStopBudget, %q, func(context.Context) error { e.%sCleanup(); return nil }))\n", e.servoAlias, e.servoAlias, label, m.Field)
		}
		b.WriteString("\treturn results\n}\n\n")
	}

	b.WriteString("// rollback stops whatever build() managed to construct, newest first.\n")
	b.WriteString("// The error paths inside build() unroll a known prefix inline; this is\n")
	b.WriteString("// for the panic path, which cannot know how far it got.\n")
	var body strings.Builder
	for i := len(se.Members) - 1; i >= 0; i-- {
		m := se.Members[i]
		if !hasCapability(m.N, "Drainer") && !e.needsStopMethod(m.N) {
			continue
		}
		fmt.Fprintf(&body, "\tif e.built > %d {\n", i)
		if hasCapability(m.N, "Drainer") {
			fmt.Fprintf(&body, "\t\te.drain%s(ctx)\n", capitalize(m.Field))
		}
		if e.needsStopMethod(m.N) {
			fmt.Fprintf(&body, "\t\te.stop%s(ctx)\n", capitalize(m.Field))
		}
		body.WriteString("\t}\n")
	}
	fmt.Fprintf(b, "func (e *%s) rollback() {\n", se.EntryName)
	// The context is only declared when something uses it: a scope whose
	// members have nothing to stop would otherwise get an unused variable
	// and a generated file that does not compile.
	if body.Len() > 0 {
		b.WriteString("\tctx := context.Background()\n")
		b.WriteString(body.String())
	}
	b.WriteString("}\n\n")

	b.WriteString("// teardown runs on a fresh context, for the same reason main hands\n")
	b.WriteString("// Shutdown one: the drain has to survive the cancellation that triggered\n")
	b.WriteString("// it. No deadline is set here because there is no caller to take one\n")
	b.WriteString("// from — a linger timer can fire this — and servo.RunStop bounds every\n")
	b.WriteString("// call below at servo.DefaultStopBudget either way. Drain comes first so\n")
	b.WriteString("// a streaming consumer unblocks before its context is pulled out from\n")
	b.WriteString("// under it; Flush comes after the Run goroutines have returned, so\n")
	b.WriteString("// anything Run buffered is flushed rather than discarded.\n")
	fmt.Fprintf(b, "func (e *%s) teardown() %s.NodeResult {\n", se.EntryName, e.servoAlias)
	b.WriteString("\tctx := context.Background()\n")
	fmt.Fprintf(b, "\tvar results []%s.NodeResult\n", e.servoAlias)
	b.WriteString("\t// Set only when the reference drain overran its budget, so this\n")
	b.WriteString("\t// instance is being stopped while someone still holds it.\n")
	fmt.Fprintf(b, "\tif e.drainResult.Status != %s.StatusOK {\n\t\tresults = append(results, e.drainResult)\n\t}\n", e.servoAlias)
	for i := len(se.Members) - 1; i >= 0; i-- {
		m := se.Members[i]
		if hasCapability(m.N, "Drainer") {
			fmt.Fprintf(b, "\tresults = append(results, e.drain%s(ctx)...)\n", capitalize(m.Field))
		}
	}
	b.WriteString("\te.cancel()\n")
	fmt.Fprintf(b, "\tresults = append(results, %s.RunStop(ctx, %s.DefaultStopBudget, e.name(), e.waitRun))\n", e.servoAlias, e.servoAlias)
	for i := len(se.Members) - 1; i >= 0; i-- {
		m := se.Members[i]
		if !e.needsStopMethod(m.N) {
			continue
		}
		fmt.Fprintf(b, "\tresults = append(results, e.stop%s(ctx)...)\n", capitalize(m.Field))
	}
	fmt.Fprintf(b, "\treturn %s.MergeNodeResults(e.name(), results...)\n}\n\n", e.servoAlias)

	fmt.Fprintf(b, "func (e *%s) waitRun(context.Context) error {\n", se.EntryName)
	b.WriteString("\te.runWG.Wait()\n\te.runMu.Lock()\n\tdefer e.runMu.Unlock()\n\treturn errors.Join(e.runErrs...)\n}\n\n")

	b.WriteString("// waitTorn blocks until this entry has finished tearing down. Shutdown\n")
	b.WriteString("// calls it through servo.RunStop so the wait is bounded.\n")
	fmt.Fprintf(b, "func (e *%s) waitTorn(ctx context.Context) error {\n", se.EntryName)
	b.WriteString("\tselect {\n\tcase <-e.torn:\n\t\treturn nil\n\tcase <-ctx.Done():\n\t\treturn ctx.Err()\n\t}\n}\n\n")
}

func (e *emitter) writeAcquire(b *strings.Builder, se *scopeEmit, re *rootEmit) {
	implType := e.qualifiedTypeString(re.R.Node.Provider.ResultType)
	field := e.memberField[re.R.Node]

	extractorArgs := []string{"ctx"}
	for _, d := range re.R.ExtractorDeps {
		// An accessor is not built by a provider, so it has no varName —
		// it is a field scopeSetup assigns at the very top of New, long
		// before any extractor runs.
		if d.Kind == resolve.NodeScopeAccessor {
			extractorArgs = append(extractorArgs, "s.app."+e.rootByAccessor[d.ScopeRoot].AccessorField)
			continue
		}
		extractorArgs = append(extractorArgs, "s.app."+e.varName[d.Key])
	}

	fmt.Fprintf(b, "// %s resolves ctx to a key, then hands back the instance for that\n", re.AcquireMethod)
	b.WriteString("// key and a closure that releases it. The reference unit is the caller's\n")
	b.WriteString("// use of the instance, not the lifetime of ctx: cancellation is not\n")
	b.WriteString("// completion, and a client disconnecting mid-handler must not free an\n")
	b.WriteString("// instance whose deferred cleanups are still running.\n")
	fmt.Fprintf(b, "func (s *%s) %s(ctx context.Context) (%s, func(), error) {\n", se.TypeName, re.AcquireMethod, implType)
	b.WriteString("\t// A context that can never be done disables the release backstop\n")
	b.WriteString("\t// below, so a caller who forgets the closure would pin this instance\n")
	b.WriteString("\t// for the life of the process. Refusing is the only way to say so.\n")
	fmt.Fprintf(b, "\tif ctx.Done() == nil {\n\t\treturn nil, nil, %s.ErrNoLifetime\n\t}\n", e.servoAlias)
	b.WriteString("\t// Called on a typed nil: the key has to be known before an instance\n")
	b.WriteString("\t// can be chosen, so there is no instance to call it on. Safe because\n")
	fmt.Fprintf(b, "\t// servo rejects a %s method whose receiver its body could reach.\n", graph.ScopeKeyMethodName)
	fmt.Fprintf(b, "\tvar zero %s\n", implType)
	fmt.Fprintf(b, "\tkey, err := zero.%s(%s)\n", graph.ScopeKeyMethodName, strings.Join(extractorArgs, ", "))
	b.WriteString("\tif err != nil {\n\t\treturn nil, nil, err\n\t}\n\n")

	b.WriteString("\tfor {\n")
	b.WriteString("\t\te, created, err := s.lookupOrCreate(key)\n")
	b.WriteString("\t\tif err != nil {\n\t\t\treturn nil, nil, err\n\t\t}\n")
	b.WriteString("\t\tif created {\n")
	b.WriteString("\t\t\tif err := s.start(e); err != nil {\n\t\t\t\treturn nil, nil, err\n\t\t\t}\n")
	b.WriteString("\t\t\t// The loop is running now, and its very first select may have\n")
	b.WriteString("\t\t\t// taken quit. Handing back an instance that is already torn\n")
	b.WriteString("\t\t\t// down would be worse than refusing: the retry sees the scope\n")
	b.WriteString("\t\t\t// closed and says so.\n")
	b.WriteString("\t\t\tselect {\n\t\t\tcase <-e.dead:\n\t\t\t\tcontinue\n\t\t\tdefault:\n\t\t\t}\n")
	b.WriteString("\t\t\ts.refs.Add(1)\n\t\t\ts.acquires.Add(1)\n")
	fmt.Fprintf(b, "\t\t\treturn e.%s, e.releaser(ctx), nil\n", field)
	b.WriteString("\t\t}\n\n")
	b.WriteString("\t\tselect {\n\t\tcase <-e.ready:\n")
	b.WriteString("\t\tcase <-e.dead:\n")
	b.WriteString("\t\t\tif e.buildErr != nil {\n\t\t\t\treturn nil, nil, e.buildErr\n\t\t\t}\n\t\t\tcontinue\n")
	b.WriteString("\t\tcase <-ctx.Done():\n\t\t\treturn nil, nil, ctx.Err()\n\t\t}\n\n")
	b.WriteString("\t\tselect {\n")
	b.WriteString("\t\tcase e.joins <- struct{}{}:\n")
	b.WriteString("\t\t\ts.refs.Add(1)\n\t\t\ts.acquires.Add(1)\n")
	fmt.Fprintf(b, "\t\t\treturn e.%s, e.releaser(ctx), nil\n", field)
	b.WriteString("\t\tcase <-e.dead:\n")
	b.WriteString("\t\t\t// Lost the race with eviction. The retry misses the map — the\n")
	b.WriteString("\t\t\t// key was removed before dead closed — and creates fresh.\n")
	b.WriteString("\t\t\tcontinue\n")
	b.WriteString("\t\tcase <-s.quit:\n")
	b.WriteString("\t\t\t// Shutdown began while this acquirer was waiting to join. The\n")
	b.WriteString("\t\t\t// loop has stopped reading joins and is draining the holders it\n")
	b.WriteString("\t\t\t// already has, so waiting here would block for that drain only\n")
	b.WriteString("\t\t\t// to be told the scope is closed. Say so now. If the join and\n")
	b.WriteString("\t\t\t// this case are ready together the choice is arbitrary and\n")
	b.WriteString("\t\t\t// either answer is correct: a join that lands is a counted\n")
	b.WriteString("\t\t\t// reference the drain will wait for.\n")
	fmt.Fprintf(b, "\t\t\treturn nil, nil, %s.ErrScopeClosed\n", e.servoAlias)
	b.WriteString("\t\tcase <-ctx.Done():\n\t\t\treturn nil, nil, ctx.Err()\n")
	b.WriteString("\t\t}\n\t}\n}\n\n")

	if re == se.Roots[0] {
		e.writeStart(b, se)
		e.writeReleaser(b, se)
	}
}

// writeStart emits the guarded construction path. It exists as its own
// method for the defer: a panic in a user constructor or Init would
// otherwise unwind straight past both abandon and `go e.loop()`, leaving
// the entry in the map with nobody to tear it down — every later acquire
// of that key blocks until its own context dies, and Shutdown waits for a
// torn channel that will never close.
func (e *emitter) writeStart(b *strings.Builder, se *scopeEmit) {
	fmt.Fprintf(b, "func (s *%s) start(entry *%s) (err error) {\n", se.TypeName, se.EntryName)
	b.WriteString("\tstarted := false\n")
	b.WriteString("\tdefer func() {\n")
	b.WriteString("\t\tif started {\n\t\t\treturn\n\t\t}\n")
	b.WriteString("\t\tif r := recover(); r != nil {\n")
	b.WriteString("\t\t\tentry.rollback()\n")
	b.WriteString("\t\t\terr = s.abandon(entry, fmt.Errorf(\"servo: panic constructing %s: %v\", entry.name(), r))\n")
	b.WriteString("\t\t}\n")
	b.WriteString("\t}()\n\n")
	b.WriteString("\tif err := entry.build(); err != nil {\n\t\tstarted = true // the unwind below is not ours; build already rolled back\n\t\treturn s.abandon(entry, err)\n\t}\n")
	b.WriteString("\tgo entry.loop()\n\tclose(entry.ready)\n\tstarted = true\n\treturn nil\n}\n\n")
}

func (e *emitter) writeReleaser(b *strings.Builder, se *scopeEmit) {
	b.WriteString("// releaser returns the closure the caller defers, with ctx ending as a\n")
	b.WriteString("// backstop behind it. Both paths run the same sync.Once, so a caller who\n")
	b.WriteString("// forgets the closure still releases when their request ends — later\n")
	b.WriteString("// than ideal, but not never.\n")
	fmt.Fprintf(b, "func (e *%s) releaser(ctx context.Context) func() {\n", se.EntryName)
	b.WriteString("\tvar once sync.Once\n")
	b.WriteString("\trelease := func() {\n\t\tonce.Do(func() {\n\t\t\te.scope.refs.Add(-1)\n")
	b.WriteString("\t\t\tselect {\n\t\t\tcase e.leaves <- struct{}{}:\n\t\t\tcase <-e.dead:\n\t\t\t}\n\t\t})\n\t}\n")
	b.WriteString("\tstop := context.AfterFunc(ctx, release)\n")
	b.WriteString("\treturn func() {\n\t\tstop()\n\t\trelease()\n\t}\n}\n\n")
}

func (e *emitter) writeAccessorType(b *strings.Builder, se *scopeEmit, re *rootEmit) {
	implType := e.qualifiedTypeString(re.R.Node.Provider.ResultType)
	fmt.Fprintf(b, "// %s is what satisfies %s. It is a distinct type per\n", re.AccessorType, re.R.Iface.String())
	b.WriteString("// exposed root so that two roots sharing one scope can each have a method\n")
	b.WriteString("// literally named Acquire.\n")
	fmt.Fprintf(b, "type %s struct{ s *%s }\n\n", re.AccessorType, se.TypeName)
	fmt.Fprintf(b, "func (x %s) Acquire(ctx context.Context) (%s, func(), error) { return x.s.%s(ctx) }\n\n", re.AccessorType, implType, re.AcquireMethod)
	fmt.Fprintf(b, "func (x %s) Stats() %s.ScopeStats { return x.s.Stats() }\n\n", re.AccessorType, e.servoAlias)
}

// scopeSetup is the first thing New does: a scope registry needs nothing
// but the App itself, and its accessors have to exist before any consumer
// constructor that takes one. Instances are created lazily on the first
// Acquire, by which point construction has long finished.
func (e *emitter) scopeSetup() string {
	var b strings.Builder
	for _, se := range e.scopes {
		fmt.Fprintf(&b, "\ta.%s = %s(ctx, a)\n", se.Field, se.NewFunc)
		for _, re := range se.Roots {
			fmt.Fprintf(&b, "\ta.%s = %s{s: a.%s}\n", re.AccessorField, re.AccessorType, se.Field)
		}
	}
	if b.Len() > 0 {
		b.WriteString("\n")
	}
	return b.String()
}

func (e *emitter) scopeAppFields() string {
	var b strings.Builder
	for _, se := range e.scopes {
		fmt.Fprintf(&b, "\t%s *%s\n", se.Field, se.TypeName)
		for _, re := range se.Roots {
			fmt.Fprintf(&b, "\t%s %s\n", re.AccessorField, re.AccessorType)
		}
		fmt.Fprintf(&b, "\t%sStopOnce sync.Once\n", se.Field)
		fmt.Fprintf(&b, "\t%sStopResult %s.NodeResult\n", se.Field, e.servoAlias)
	}
	return b.String()
}

// scopeStopMethods emits one stop method per scope. It closes the scope to
// new acquires, then waits for every entry it was holding to finish
// tearing down, and reports the whole scope as one node rather than one
// node per live instance — a report with an entry per chat room is not a
// report.
func (e *emitter) scopeStopMethods() string {
	var b strings.Builder
	for _, se := range e.scopes {
		fmt.Fprintf(&b, "func (a *%s) %s(ctx context.Context) %s.NodeResult {\n", e.appType(), se.StopMethod, e.servoAlias)
		fmt.Fprintf(&b, "\ta.%sStopOnce.Do(func() {\n", se.Field)
		fmt.Fprintf(&b, "\t\ts := a.%s\n", se.Field)
		b.WriteString("\t\ts.mu.Lock()\n")
		b.WriteString("\t\tif !s.closed {\n\t\t\ts.closed = true\n\t\t\tclose(s.quit)\n\t\t}\n")
		b.WriteString("\t\t// Both sets, not just items: an entry that began evicting a moment\n")
		b.WriteString("\t\t// ago has already left the map, and waiting only on what is mapped\n")
		b.WriteString("\t\t// would let Shutdown return while its Drain and Flush were still\n")
		b.WriteString("\t\t// running — against singletons this very function is about to stop.\n")
		fmt.Fprintf(&b, "\t\tentries := make([]*%s, 0, s.liveLocked())\n", se.EntryName)
		b.WriteString("\t\tfor _, e := range s.items {\n\t\t\tentries = append(entries, e)\n\t\t}\n")
		b.WriteString("\t\tfor e := range s.tearing {\n\t\t\tentries = append(entries, e)\n\t\t}\n")
		b.WriteString("\t\ts.mu.Unlock()\n\n")
		fmt.Fprintf(&b, "\t\tresults := make([]%s.NodeResult, 0, len(entries))\n", e.servoAlias)
		b.WriteString("\t\t// Budgeted like every other stop call, so an instance wedged in a\n")
		b.WriteString("\t\t// constructor or a Drain is reported abandoned rather than holding\n")
		b.WriteString("\t\t// the whole shutdown open past the deadline its caller passed. The\n")
		fmt.Fprintf(&b, "\t\t// multiplier is the number of budgeted calls one teardown makes\n\t\t// (%d here): a single budget would time out on an entry that was\n\t\t// tearing down perfectly correctly, and report it abandoned.\n", se.teardownPhases())
		b.WriteString("\t\tfor _, e := range entries {\n")
		fmt.Fprintf(&b, "\t\t\tif res := %s.RunStop(ctx, %d*%s.DefaultStopBudget, e.name(), e.waitTorn); res.Status != %s.StatusOK {\n", e.servoAlias, se.teardownPhases(), e.servoAlias, e.servoAlias)
		b.WriteString("\t\t\t\tresults = append(results, res)\n\t\t\t\tcontinue\n\t\t\t}\n")
		b.WriteString("\t\t\tresults = append(results, e.stopResult)\n\t\t}\n")
		fmt.Fprintf(&b, "\t\ta.%sStopResult = %s.MergeNodeResults(%q, results...)\n", se.Field, e.servoAlias, se.S.Name)
		b.WriteString("\t})\n")
		fmt.Fprintf(&b, "\treturn a.%sStopResult\n}\n\n", se.Field)
	}
	return b.String()
}

// entryArg renders one constructor argument inside an entry's build: a
// sibling instance, the key itself, a shared singleton off the App, or
// another scope's accessor.
func (e *emitter) entryArg(dep *resolve.Node) string {
	switch dep.Kind {
	case resolve.NodeScopeKey:
		return "e.key"
	case resolve.NodeScopeAccessor:
		return "a." + e.rootByAccessor[dep.ScopeRoot].AccessorField
	default:
		if field, ok := e.memberField[dep]; ok {
			return "e." + field
		}
		return "a." + e.varName[dep.Key]
	}
}

// scopeLevelGroups is levelGroups over ScopeLevel rather than Level, so a
// scope's Init phases are counted from its own floor and don't shift
// because the singletons it borrows happen to sit deep in the app graph.
func scopeLevelGroups(nodes []*resolve.Node, pred func(*resolve.Node) bool) [][]*resolve.Node {
	byLevel := map[int][]*resolve.Node{}
	var levels []int
	for _, n := range nodes {
		if !pred(n) {
			continue
		}
		if _, ok := byLevel[n.ScopeLevel]; !ok {
			levels = append(levels, n.ScopeLevel)
		}
		byLevel[n.ScopeLevel] = append(byLevel[n.ScopeLevel], n)
	}
	sort.Ints(levels)
	groups := make([][]*resolve.Node, len(levels))
	for i, lvl := range levels {
		groups[i] = byLevel[lvl]
	}
	return groups
}

// durationLiteral renders d as the expression a human would have written,
// so the emitted policy reads as "30 * time.Second" rather than as a
// nanosecond count nobody can check at a glance.
func durationLiteral(d time.Duration) string {
	if d == 0 {
		return "0"
	}
	for _, u := range []struct {
		d    time.Duration
		name string
	}{
		{time.Hour, "time.Hour"},
		{time.Minute, "time.Minute"},
		{time.Second, "time.Second"},
		{time.Millisecond, "time.Millisecond"},
		{time.Microsecond, "time.Microsecond"},
	} {
		if d%u.d == 0 {
			if n := d / u.d; n == 1 {
				return u.name
			} else {
				return fmt.Sprintf("%d * %s", n, u.name)
			}
		}
	}
	return fmt.Sprintf("%d * time.Nanosecond", int64(d))
}

// shortTypeName is a type's own declared name, without package
// qualification or pointer, for deriving identifiers from.
func shortTypeName(t types.Type) string {
	if named := unwrapToNamed(t); named != nil {
		return named.Obj().Name()
	}
	return "scope"
}

// scopeUsesApp reports whether any member's constructor takes something
// off the App — a borrowed singleton or another scope's accessor. When
// none does, binding `a` would be an unused variable and a compile error.
func scopeUsesApp(se *scopeEmit) bool {
	for _, m := range se.Members {
		for _, d := range m.N.Deps {
			if d.Kind == resolve.NodeScopeAccessor || (d.Kind == resolve.NodeProvider && d.Scope == nil) {
				return true
			}
		}
	}
	return false
}

// teardownPhases counts the budgeted calls one entry's teardown makes:
// the drain of any references still outstanding when Shutdown arrived,
// every Drainer's Drain, the wait for the Run goroutines, and every
// Flush/Stop/cleanup. Shutdown waits for an entry with this many budgets,
// because waiting with one would time out on a teardown that was
// proceeding exactly as designed.
func (se *scopeEmit) teardownPhases() int {
	n := 2 // drainRefs, then waitRun
	for _, m := range se.Members {
		if hasCapability(m.N, "Drainer") {
			n++
		}
		if hasCapability(m.N, "Flusher") {
			n++
		}
		if hasCapability(m.N, "Finalizer") {
			n++
		}
		if m.N.Provider.HasCleanup {
			n++
		}
	}
	return n
}
