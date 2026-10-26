package emit

import (
	"regexp"
	"strings"
	"testing"

	"github.com/okian/servo/v3/internal/load"
	"github.com/okian/servo/v3/internal/resolve"
)

// lifecycleFixtures is every fixture whose emitted file has an Init phase,
// runners or scopes, in both variants, so the assertions below hold for
// the singleton, scoped, HTTP and traced shapes alike.
func lifecycleFixtures(t *testing.T) map[string]string {
	t.Helper()
	builders := map[string]func(*testing.T) (*resolve.Resolved, *load.Spec){
		"full":   buildFullAppResolved,
		"scoped": buildScopedResolved,
		"http":   buildHTTPResolved,
		"traced": buildTracedAppResolved,
	}
	out := map[string]string{}
	for name, build := range builders {
		resolved, spec := build(t)
		for _, testMode := range []bool{false, true} {
			src, err := Emit(resolved, spec, testMode)
			if err != nil {
				t.Fatalf("%s (test mode %v): Emit: %v", name, testMode, err)
			}
			key := name
			if testMode {
				key += " (test variant)"
			}
			out[key] = string(src)
		}
	}
	return out
}

// TestEmitNoLifecycleCallIsMadeBare pins that every Init and Run goes
// through the servoimpl helpers. A bare call had no recover between it and
// the process: a runner's panic killed the process with Shutdown never
// run, a shared Init level's panic did the same, and a lone Init's unwound
// into main — none of them ran the rollback.
func TestEmitNoLifecycleCallIsMadeBare(t *testing.T) {
	bare := regexp.MustCompile(`\b[ae]\.\w+\.(Init|Run|run)\((g?ctx|e\.ctx)\)`)
	for name, src := range lifecycleFixtures(t) {
		if calls := bare.FindAllString(src, -1); len(calls) > 0 {
			t.Errorf("%s: lifecycle method called bare, outside servoimpl: %q\n---\n%s", name, calls, src)
		}
	}
}

// TestEmitDoesNotImportErrgroup: generated code is stdlib-only plus the
// servo module. The errgroup import used to appear with the first level of
// two Initializers, and a consumer that required only servo got generate
// exiting 0 and then "missing go.sum entry … errgroup" from go build.
func TestEmitDoesNotImportErrgroup(t *testing.T) {
	for name, src := range lifecycleFixtures(t) {
		if strings.Contains(src, "errgroup") || strings.Contains(src, "golang.org/x/sync") {
			t.Errorf("%s: generated code still reaches for errgroup:\n%s", name, src)
		}
		if !strings.Contains(src, "\t\"github.com/okian/servo/v3/servoimpl\"\n") {
			t.Errorf("%s: generated code does not import servoimpl:\n%s", name, src)
		}
	}
}

// TestEmitInitPhaseIsOneCallInLevelOrder: the whole phase is one
// servoimpl.Init, a slice per level in ascending order — DB and Cache share
// L2, the Server sits alone at L3 — followed by a single rollback.
func TestEmitInitPhaseIsOneCallInLevelOrder(t *testing.T) {
	resolved, spec := buildFullAppResolved(t)
	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	want := "\tif err := servoimpl.Init(ctx, servo.DefaultInitBudget, &a.startupReport,\n" +
		"\t\t[]servoimpl.Call{\n" +
		"\t\t\t{Node: \"*example.com/app.DB\", Fn: a.db.Init},\n" +
		"\t\t\t{Node: \"*example.com/app.Cache\", Fn: a.cache.Init},\n" +
		"\t\t},\n" +
		"\t\t[]servoimpl.Call{\n" +
		"\t\t\t{Node: \"*example.com/app.Server\", Fn: a.server.Init},\n" +
		"\t\t},\n" +
		"\t); err != nil {\n" +
		"\t\treturn nil, servoimpl.RolledBack(err, a.Shutdown(context.WithoutCancel(ctx)))\n" +
		"\t}\n" +
		"\treturn a, nil\n"
	if !strings.Contains(string(out), want) {
		t.Errorf("Init phase is not the expected shape, want:\n%s\n---\n%s", want, out)
	}
}

// TestEmitRollbackDoesNotJoinTheReport: a joined rollback report made
// errors.Is(err, context.DeadlineExceeded) true whenever a rollback Stop
// overran its budget, whatever the startup had actually failed on.
func TestEmitRollbackDoesNotJoinTheReport(t *testing.T) {
	for name, src := range lifecycleFixtures(t) {
		if strings.Contains(src, "errors.Join(err, report)") {
			t.Errorf("%s: a startup failure is still joined with its rollback report:\n%s", name, src)
		}
		// Only a file with an App-level Init phase unwinds through
		// Shutdown; the scoped fixture's singletons have no Init at all.
		if strings.Contains(src, "servoimpl.Init(ctx,") && !strings.Contains(src, "servoimpl.RolledBack(err, a.Shutdown(context.WithoutCancel(ctx)))") {
			t.Errorf("%s: no rollback through servoimpl.RolledBack:\n%s", name, src)
		}
	}
}

// TestEmitForceExitSaysWhyAndExitsBySignal: the second-signal force exit
// used to be a silent os.Exit(1). servoimpl.ForceExit names the signal on
// stderr and exits 128 plus its number.
func TestEmitForceExitSaysWhyAndExitsBySignal(t *testing.T) {
	for name, src := range lifecycleFixtures(t) {
		if !strings.Contains(src, "\t\tcase sig := <-forceExit:\n\t\t\tservoimpl.ForceExit(sig)\n") {
			t.Errorf("%s: Shutdown's watcher does not force-exit through servoimpl.ForceExit:\n%s", name, src)
		}
		if strings.Contains(src, "os.Exit(1)") {
			t.Errorf("%s: a silent os.Exit(1) is still emitted:\n%s", name, src)
		}
	}
}

// TestEmitStopsGoThroughServoimpl: the deprecated servo.RunStop,
// MergeNodeResults and LingerWindow stay callable for older files; nothing
// generated now calls them.
func TestEmitStopsGoThroughServoimpl(t *testing.T) {
	deprecated := regexp.MustCompile(`\bservo\.(RunStop|MergeNodeResults|LingerWindow)\(`)
	for name, src := range lifecycleFixtures(t) {
		// The emitted HTTP server's own stop is the HTTP emitter's, not
		// the lifecycle's; everything else must have moved.
		for _, line := range strings.Split(src, "\n") {
			if deprecated.MatchString(line) && !strings.Contains(line, ".srv.Shutdown") {
				t.Errorf("%s: deprecated runtime helper still called: %s", name, strings.TrimSpace(line))
			}
		}
	}
}

// TestEmitScopeEntryClaimsTheServoPackages: build() calls servoimpl and
// reads servo's budgets in the same flat scope its member locals live in,
// so a member type named Servoimpl or Servo must not take either local.
func TestEmitScopeEntryClaimsTheServoPackages(t *testing.T) {
	const src = `
package app

import "context"

type RoomKey string

type Room struct{}
func (*Room) ScopeKey(ctx context.Context) (RoomKey, error) { return "", nil }
func NewRoom(k RoomKey, s *Servoimpl, v *Servo) *Room { return &Room{} }
func (*Room) Init(context.Context) error { return nil }
func (*Room) Run(context.Context) error { return nil }

type Servoimpl struct{}
func NewServoimpl(k RoomKey) *Servoimpl { return &Servoimpl{} }
func (*Servoimpl) Init(context.Context) error { return nil }

type Servo struct{}
func NewServo(k RoomKey) *Servo { return &Servo{} }

type Rooms interface {
	Acquire(ctx context.Context) (*Room, func(), error)
}

type Server struct{}
func NewServer(r Rooms) *Server { return &Server{} }
`
	resolved, spec := buildResolvedFrom(t, src, []string{"Server"}, [][2]string{{"Room", "Rooms"}})
	out, err := Emit(resolved, spec, false)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	got := string(out)
	for _, shadowing := range []string{"\tservoimpl := NewServoimpl(", "\tservo := NewServo("} {
		if strings.Contains(got, shadowing) {
			t.Errorf("a member local shadows a package build() still calls into (%q):\n%s", shadowing, got)
		}
	}
	if !strings.Contains(got, "servoimpl.Init(e.ctx, servo.DefaultInitBudget, nil,") {
		t.Errorf("scoped Init phase missing:\n%s", got)
	}
}
