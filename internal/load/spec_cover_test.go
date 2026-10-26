package load

import (
	"go/token"
	"go/types"
	"strings"
	"testing"

	"github.com/okian/servo/v3/internal/graph"
)

// writeSkewRuntime is writeStubRuntimeModule without the spec, and without
// a servo.Root of its own: the stand-in runtime declares Marker, Build and
// an interface Iface, plus runtimeSrc, so any marker — Root included — can
// take another release's shape. It returns the app module's directory,
// which holds an api package and the spec file the caller writes.
func writeSkewRuntime(t *testing.T, runtimeSrc string) string {
	t.Helper()
	dir := t.TempDir()
	mustWriteFile(t, dir, "runtime/go.mod", "module github.com/okian/servo/v3\n\ngo 1.23\n")
	mustWriteFile(t, dir, "runtime/servo/servo.go", "package servo\n\ntype Marker struct{}\n\nfunc Build(...Marker) {}\n\ntype Iface interface{ Do() }\n\n"+runtimeSrc)
	mustWriteFile(t, dir, "app/go.mod", "module example.com/skew\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => ../runtime\n")
	mustWriteFile(t, dir, "app/api/api.go", "package api\n\ntype Server struct{}\n\nfunc New() *Server { return &Server{} }\n")
	return dir + "/app"
}

// skewSpec is a spec file for writeSkewRuntime's module: imports added to
// its own two, and a Build call holding args.
func skewSpec(imports, args string) string {
	return "//go:build servoinject\n\npackage spec\n\nimport (\n" + imports + "\t\"example.com/skew/api\"\n\t\"github.com/okian/servo/v3/servo\"\n)\n\nvar _ = api.New\n\nfunc Wire() {\n\tservo.Build(" + args + ")\n}\n"
}

// TestFindSpecAgainstAnotherRuntimesMarkerShapes covers a runtime release
// whose markers differ from the ones this CLI reads. A marker taking a
// different number of type arguments is named with the count the CLI
// expects, rather than read with an index out of range; an option or
// selector the CLI has never heard of is the version skew it is — named
// with the list it was found in; and an option the CLI knows but the
// runtime lacks is the opposite skew, even inside servo.Scoped(...).
func TestFindSpecAgainstAnotherRuntimesMarkerShapes(t *testing.T) {
	const (
		httpRuntime  = "type HTTPOption struct{}\n\nfunc HTTP(...HTTPOption) Marker { return Marker{} }\n\n"
		scopeRuntime = "type ScopeOption struct{}\n\nfunc Scoped[T, I any](...ScopeOption) Marker { return Marker{} }\n\n"
		traceRuntime = "type TraceOption struct{}\n\n"
	)
	for _, c := range []struct {
		name, runtime, args, want string
	}{
		{"Root of two", "func Root[A, B any]() Marker { return Marker{} }\n", "servo.Root[*api.Server, *api.Server]()", "servo.Root expects exactly one type argument"},
		{"Value of two", "func Value[A, B any]() Marker { return Marker{} }\n", "servo.Value[*api.Server, *api.Server]()", "servo.Value expects exactly one type argument"},
		{"Bind of one", "func Bind[I any]() Marker { return Marker{} }\n", "servo.Bind[servo.Iface]()", "servo.Bind expects exactly two type arguments"},
		{"Extract of two", "func Extract[A, B any]() Marker { return Marker{} }\n", "servo.Extract[*api.Server, *api.Server]()", "servo.Extract expects exactly one type argument"},
		{"Trace of two", traceRuntime + "func Trace[A, B any](...TraceOption) Marker { return Marker{} }\n", "servo.Trace[servo.Iface, servo.Iface]()", "servo.Trace expects exactly one type argument"},
		{"Scoped of one", "type ScopeOption struct{}\n\nfunc Scoped[T any](...ScopeOption) Marker { return Marker{} }\n", "servo.Scoped[*api.Server]()", "servo.Scoped expects exactly two type arguments"},
		{"Use of two", httpRuntime + "func Use[A, B any](...HTTPOption) HTTPOption { return HTTPOption{} }\n", "servo.HTTP(servo.Use[*api.Server, *api.Server]())", "servo.Use expects exactly one type argument"},
		{
			"a Trace option the CLI does not know",
			traceRuntime + "func Trace[I any](...TraceOption) Marker { return Marker{} }\n\nfunc Sample(float64) TraceOption { return TraceOption{} }\n",
			"servo.Trace[servo.Iface](servo.Sample(0.5))",
			"servo.Sample is a servo.Trace option " + graph.ServoPackagePath + ", which the module requires, declares and this servo CLI does not know",
		},
		{
			"an HTTP option the CLI does not know",
			httpRuntime + "func Listen(string) HTTPOption { return HTTPOption{} }\n",
			`servo.HTTP(servo.Listen(":8080"))`,
			"servo.Listen is a servo.HTTP option " + graph.ServoPackagePath + ", which the module requires, declares and this servo CLI does not know",
		},
		{
			"a Use selector the CLI does not know",
			httpRuntime + "func Use[T any](...HTTPOption) HTTPOption { return HTTPOption{} }\n\nfunc Methods(...string) HTTPOption { return HTTPOption{} }\n",
			`servo.HTTP(servo.Use[*api.Server](servo.Methods("GET")))`,
			"servo.Methods is a servo.Use selector " + graph.ServoPackagePath + ", which the module requires, declares and this servo CLI does not know",
		},
		{
			"a Scoped option the CLI does not know",
			scopeRuntime + "func Jitter(int) ScopeOption { return ScopeOption{} }\n",
			"servo.Scoped[*api.Server, servo.Iface](servo.Jitter(3))",
			"servo.Jitter is a servo.Scoped option " + graph.ServoPackagePath + ", which the module requires, declares and this servo CLI does not know",
		},
		{
			"a Scoped option the runtime does not declare",
			scopeRuntime,
			"servo.Scoped[*api.Server, servo.Iface](servo.Linger(1))",
			"servo.Linger is a marker this servo CLI knows, but " + graph.ServoPackagePath + ", which the module requires, does not declare it",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := writeSkewRuntime(t, c.runtime)
			mustWriteFile(t, dir, "spec/spec.go", skewSpec("", c.args))
			_, err := FindSpec(mustLoad(t, Config{Dir: dir}))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v\nwant it to contain %q", err, c.want)
			}
			if !strings.Contains(err.Error(), "spec.go:") {
				t.Errorf("got %v, want it positioned in the spec file", err)
			}
		})
	}
}

// TestFindSpecRefusesAConstraintItCannotMirror: `servoinject || !prod`
// passes the build-tag check — it cannot hold without servoinject when
// every other tag is set — but its mirror image, `!servoinject || !prod`,
// still holds with servoinject set, so servo would read its own output on
// the next run. The refusal names the Build call it belongs to.
func TestFindSpecRefusesAConstraintItCannotMirror(t *testing.T) {
	dir := writeSkewRuntime(t, "func Root[T any]() Marker { return Marker{} }\n")
	spec := strings.Replace(skewSpec("", "servo.Root[*api.Server]()"), "//go:build servoinject", "//go:build servoinject || !prod", 1)
	mustWriteFile(t, dir, "spec/spec.go", spec)
	_, err := FindSpecs(mustLoad(t, Config{Dir: dir}))
	if err == nil || !strings.Contains(err.Error(), "spec.go:13:2: spec file's build constraint does not require servoinject in every case it allows") {
		t.Fatalf("got %v, want the unmirrorable constraint refused at the Build call", err)
	}
}

// TestFindSpecNamesAnUninstantiatedMarkerOutsideASpecFile: the type
// checker's errors are offered only from spec files — the rest of a package
// is the build's to report. A Build call in an ungated file therefore has
// none to show, and a marker written without its type arguments gets the
// plain explanation instead.
func TestFindSpecNamesAnUninstantiatedMarkerOutsideASpecFile(t *testing.T) {
	dir := writeSkewRuntime(t, "func Root[T any]() Marker { return Marker{} }\n")
	mustWriteFile(t, dir, "wire/wire.go", "package wire\n\nimport \"github.com/okian/servo/v3/servo\"\n\nfunc Wire() { servo.Build(servo.Root()) }\n")
	_, err := FindSpecs(mustLoad(t, Config{Dir: dir}))
	if err == nil || !strings.Contains(err.Error(), "wire.go:5:27: servo.Root must be instantiated with explicit type arguments") {
		t.Fatalf("got %v, want servo.Root named as uninstantiated", err)
	}
}

// TestFindSpecRefusesAnIncludeWhoseBodyItCannotRead: a function servo.Include
// names is read as syntax, so one that arrives as export data — a standard
// library function, with no body in the load — is refused by name rather
// than spliced in as nothing.
func TestFindSpecRefusesAnIncludeWhoseBodyItCannotRead(t *testing.T) {
	dir := writeSkewRuntime(t, "func Include(func() []Marker) Marker { return Marker{} }\n")
	mustWriteFile(t, dir, "spec/spec.go", skewSpec("\t\"os\"\n", "servo.Include(os.Environ)"))
	_, err := FindSpec(mustLoad(t, Config{Dir: dir}))
	if err == nil || !strings.Contains(err.Error(), "servo.Include names os.Environ, whose declaration is not in this build") {
		t.Fatalf("got %v, want the Include refused by name", err)
	}
}

// TestBindToAStandardLibraryTypeHasNoMockHint: the mock hint is for types
// that read like generated code. A standard-library type is none, and its
// package — export data, without syntax — has no file header to read
// either, so the plain reason stands alone.
func TestBindToAStandardLibraryTypeHasNoMockHint(t *testing.T) {
	dir := writeSkewRuntime(t, "func Bind[I, C any]() Marker { return Marker{} }\n")
	mustWriteFile(t, dir, "spec/spec.go", skewSpec("\t\"strings\"\n", "servo.Bind[servo.Iface, *strings.Builder]()"))
	_, err := FindSpec(mustLoad(t, Config{Dir: dir}))
	if err == nil || !strings.Contains(err.Error(), "*strings.Builder does not implement servo.Iface (missing method Do)") {
		t.Fatalf("got %v, want the missing method named", err)
	}
	if strings.Contains(err.Error(), "regenerate it") {
		t.Errorf("got %v, want no mock hint for a standard-library type", err)
	}
}

// TestReturnsServoTypeOnlyForTheRuntimesOwnFunctions: returning a type named
// Marker is not enough to be a marker of a newer runtime — the function has
// to be the runtime package's own, and no function at all is none.
func TestReturnsServoTypeOnlyForTheRuntimesOwnFunctions(t *testing.T) {
	if returnsServoType(nil, "Marker") {
		t.Error("returnsServoType(nil) = true")
	}
	other := types.NewPackage("example.com/other", "other")
	marker := types.NewNamed(types.NewTypeName(token.NoPos, other, "Marker", nil), types.NewStruct(nil, nil), nil)
	sig := types.NewSignatureType(nil, nil, nil, nil, types.NewTuple(types.NewParam(token.NoPos, other, "", marker)), false)
	if returnsServoType(types.NewFunc(token.NoPos, other, "Root", sig), "Marker") {
		t.Error("returnsServoType(other.Root) = true for a function outside the runtime")
	}
}
