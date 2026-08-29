package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVariantFileName(t *testing.T) {
	cases := []struct {
		name string
		tags []string
		test bool
		want string
	}{
		// The tagless case must stay exactly what servo has always
		// written, or every committed generated file in every existing
		// project moves.
		{"no tags", nil, false, "servo_gen.go"},
		{"no tags, test variant", nil, true, "servo_gen_test.go"},
		{"one tag", []string{"prod"}, false, "servo.prod_gen.go"},
		{"one tag, test variant", []string{"prod"}, true, "servo.prod_gen_test.go"},
		{"several tags", []string{"integration", "prod"}, false, "servo.integration-prod_gen.go"},
		// "_" is legal in a build tag, so joining with it would map
		// {a_b} and {a, b} to one file and lose a variant silently. "-"
		// cannot appear in a tag, so the join stays injective.
		{"underscore in a tag survives", []string{"sqlite_omit"}, false, "servo.sqlite_omit_gen.go"},
		{"dot in a tag survives", []string{"v2.1"}, false, "servo.v2.1_gen.go"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := variantFileName(c.tags, c.test); got != c.want {
				t.Errorf("variantFileName(%v, %v) = %q, want %q", c.tags, c.test, got, c.want)
			}
		})
	}
}

// TestVariantFileNameAvoidsImplicitConstraints is the reason for the dot.
// Go gives a file an implicit GOOS/GOARCH constraint derived from its
// underscore-separated suffix, so a variant named servo_gen_linux.go would
// be generated, committed, and then silently ignored on every non-Linux
// machine. goodOSArchFile cuts the name at the *first* dot before looking
// for underscores, so putting the tag text after "servo." makes it
// structurally invisible to that rule — for any tag, with no list of
// reserved names to keep current.
func TestVariantFileNameAvoidsImplicitConstraints(t *testing.T) {
	// Names servo rejects as tags (see load.BuildFlags.Validate), used
	// here only to prove the naming scheme would survive them anyway.
	for _, tag := range []string{"linux", "windows", "js", "wasm", "arm64", "amd64", "test"} {
		name := variantFileName([]string{tag}, false)
		trimmed, _, _ := strings.Cut(name, ".")
		if strings.Contains(trimmed, "_") {
			t.Errorf("variantFileName(%q) = %q: the part before the first dot is %q, which contains an underscore and so is subject to Go's implicit GOOS/GOARCH filename constraints", tag, name, trimmed)
		}
		if strings.HasSuffix(name, "_test.go") {
			t.Errorf("variantFileName(%q) = %q, which Go would treat as a test file — it would never reach the binary", tag, name)
		}
	}
}

// TestGenerateWritesOneVariantPerBuildConfiguration is the feature,
// end to end: two spec files gated on mutually exclusive constraints
// produce two generated files that coexist, each wiring the providers
// visible in its own configuration, and the module compiles both ways.
func TestGenerateWritesOneVariantPerBuildConfiguration(t *testing.T) {
	dir := writeVariantModule(t, "example.com/variantgen")
	appDir := filepath.Join(dir, "cmd", "app")

	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("generate (default): %v", err)
	}
	if err := runGenerate(taggedCfg(dir, "prod")); err != nil {
		t.Fatalf("generate -tags=prod: %v", err)
	}

	defaultGen := filepath.Join(appDir, "servo_gen.go")
	prodGen := filepath.Join(appDir, "servo.prod_gen.go")

	for _, path := range []string{defaultGen, prodGen} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s to exist: %v", filepath.Base(path), err)
		}
	}

	// The constraints must exclude each other, or building with -tags=prod
	// compiles both files and the program fails with duplicate App/New
	// declarations. servo never invents the negation: it comes from the
	// spec files' own constraints.
	assertFirstLine(t, defaultGen, "//go:build !servoinject && !prod")
	assertFirstLine(t, prodGen, "//go:build !servoinject && prod")

	// Each variant wires the implementation that exists in its own
	// configuration — the graph really was resolved under the tags.
	assertContains(t, defaultGen, "memory.New()")
	assertNotContains(t, defaultGen, "postgres.")
	assertContains(t, prodGen, "postgres.New()")
	assertNotContains(t, prodGen, "memory.")

	// The assertion that would catch everything else: both configurations
	// have to actually compile, with both files committed side by side.
	runGoBuild(t, dir, "")
	runGoBuild(t, dir, "prod")

	// And each variant is clean under check with its own flags, which is
	// how CI verifies a multi-variant module: one check run per variant.
	if err := runCheck(cfg(dir)); err != nil {
		t.Errorf("check (default): %v", err)
	}
	if err := runCheck(taggedCfg(dir, "prod")); err != nil {
		t.Errorf("check -tags=prod: %v", err)
	}
}

// TestCheckReportsAVariantStaleIndependently confirms check compares the
// variant's own file rather than always looking at servo_gen.go: a
// hand-edited prod variant must fail `check -tags=prod` while the default
// variant stays clean.
func TestCheckReportsAVariantStaleIndependently(t *testing.T) {
	dir := writeVariantModule(t, "example.com/variantstale")
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("generate (default): %v", err)
	}
	if err := runGenerate(taggedCfg(dir, "prod")); err != nil {
		t.Fatalf("generate -tags=prod: %v", err)
	}

	prodGen := filepath.Join(dir, "cmd", "app", "servo.prod_gen.go")
	body, err := os.ReadFile(prodGen)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(body), "// Code generated by servo generate. DO NOT EDIT.", "// Code generated by servo generate. DO NOT EDIT. (hand-edited)", 1)
	if edited == string(body) {
		t.Fatal("fixture did not change; the generated header comment moved")
	}
	if err := os.WriteFile(prodGen, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	err = runCheck(taggedCfg(dir, "prod"))
	if err == nil || !strings.Contains(err.Error(), "servo.prod_gen.go is stale") {
		t.Fatalf("check -tags=prod = %v, want a staleness report naming servo.prod_gen.go", err)
	}
	if err := runCheck(cfg(dir)); err != nil {
		t.Errorf("check (default) = %v, want the untouched default variant to stay clean", err)
	}
}

// TestCheckReportsAMissingVariant covers the other half: generating only
// the default variant leaves nothing for `check -tags=prod` to compare,
// and the message has to name the variant's own file.
func TestCheckReportsAMissingVariant(t *testing.T) {
	dir := writeVariantModule(t, "example.com/variantmissing")
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("generate (default): %v", err)
	}

	err := runCheck(taggedCfg(dir, "prod"))
	if err == nil || !strings.Contains(err.Error(), "servo.prod_gen.go does not exist") {
		t.Fatalf("check -tags=prod = %v, want a missing-file report naming servo.prod_gen.go", err)
	}
}

// TestInspectionCommandsHonourTags confirms -tags is not a generate-only
// flag: asking what the prod graph looks like must answer about prod.
func TestInspectionCommandsHonourTags(t *testing.T) {
	dir := writeVariantModule(t, "example.com/variantinspect")

	defaultGraph := captureStdout(t, func() {
		if err := runGraph(cfg(dir), "text"); err != nil {
			t.Fatalf("graph (default): %v", err)
		}
	})
	prodGraph := captureStdout(t, func() {
		if err := runGraph(taggedCfg(dir, "prod"), "text"); err != nil {
			t.Fatalf("graph -tags=prod: %v", err)
		}
	})

	if !strings.Contains(defaultGraph, "memory.Mem") || strings.Contains(defaultGraph, "postgres.PG") {
		t.Errorf("default graph should wire memory.Mem and not postgres.PG, got:\n%s", defaultGraph)
	}
	if !strings.Contains(prodGraph, "postgres.PG") || strings.Contains(prodGraph, "memory.Mem") {
		t.Errorf("prod graph should wire postgres.PG and not memory.Mem, got:\n%s", prodGraph)
	}

	explain := captureStdout(t, func() {
		if err := runExplain(taggedCfg(dir, "prod"), "postgres.PG", false); err != nil {
			t.Fatalf("explain -tags=prod: %v", err)
		}
	})
	if !strings.Contains(explain, "postgres.PG") {
		t.Errorf("explain -tags=prod should describe postgres.PG, got:\n%s", explain)
	}
}

// TestDoctorReportsTheVariantItWasAskedAbout confirms doctor's freshness
// and existence checks follow the tags too, rather than always inspecting
// the default variant's file.
func TestDoctorReportsTheVariantItWasAskedAbout(t *testing.T) {
	dir := writeVariantModule(t, "example.com/variantdoctor")
	if err := runGenerate(taggedCfg(dir, "prod")); err != nil {
		t.Fatalf("generate -tags=prod: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runDoctor(taggedCfg(dir, "prod")); err != nil {
			t.Fatalf("doctor -tags=prod: %v", err)
		}
	})
	if !strings.Contains(out, "servo.prod_gen.go") {
		t.Errorf("doctor -tags=prod should report on servo.prod_gen.go, got:\n%s", out)
	}

	// The default variant was never generated, so doctor without tags must
	// report a problem rather than reusing the prod variant's file.
	defaultOut := captureStdout(t, func() {
		if err := runDoctor(cfg(dir)); err == nil {
			t.Error("doctor (default) = nil, want a problem: servo_gen.go was never generated")
		}
	})
	if !strings.Contains(defaultOut, "servo_gen.go") {
		t.Errorf("doctor (default) should report on servo_gen.go, got:\n%s", defaultOut)
	}
}

// TestGenerateRejectsAnUnusableTag confirms the flag validation reaches the
// command: the go command would accept -tags=linux and then fail inside the
// standard library with nothing pointing back at servo.
func TestGenerateRejectsAnUnusableTag(t *testing.T) {
	dir := writeVariantModule(t, "example.com/variantbadtag")
	err := runGenerate(taggedCfg(dir, "linux"))
	if err == nil || !strings.Contains(err.Error(), "GOOS/GOARCH") {
		t.Fatalf("generate -tags=linux = %v, want the GOOS/GOARCH rejection", err)
	}
}

func assertFirstLine(t *testing.T, path, want string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, _, _ := strings.Cut(string(body), "\n")
	if got != want {
		t.Errorf("%s: first line = %q, want %q", filepath.Base(path), got, want)
	}
}

func assertContains(t *testing.T, path, want string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), want) {
		t.Errorf("%s does not contain %q", filepath.Base(path), want)
	}
}

func assertNotContains(t *testing.T, path, unwanted string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), unwanted) {
		t.Errorf("%s unexpectedly contains %q", filepath.Base(path), unwanted)
	}
}

// TestGenerateRefusesOverlappingVariants is the guard on the one sharp edge
// of deriving constraints from spec files: nothing stops an author from
// generating two variants that are not actually exclusive. The plain
// `//go:build servoinject` that `servo init` scaffolds, generated again
// with --tags=prod, yields `!servoinject` beside `!servoinject && prod`,
// and `go build -tags=prod` would compile both and fail with App and New
// declared twice.
func TestGenerateRefusesOverlappingVariants(t *testing.T) {
	dir := writeAppModule(t, "example.com/overlapgen", true, "")
	appDir := filepath.Join(dir, "cmd", "app")

	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("generate (default): %v", err)
	}

	err := runGenerate(taggedCfg(dir, "prod"))
	if err == nil {
		t.Fatal("generate --tags=prod = nil, want a refusal: the spec is not gated against prod, so both variants would compile together")
	}
	for _, want := range []string{"servo.prod_gen.go", "servo_gen.go", "would both compile"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%v", want, err)
		}
	}
	// The paste-in fix has to be a build tag. It named the generated file
	// instead, because an explicit fmt argument index picked argument one
	// and also suppressed the %!(EXTRA) marker that would have shown the
	// build tag going unused.
	for _, want := range []string{"`//go:build servoinject && !prod`", "`//go:build servoinject && prod`"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the suggested fix is not a usable build constraint: want %q in:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "//go:build servo.prod_gen.go") {
		t.Errorf("the suggested fix names the generated file where it means the build tag:\n%v", err)
	}

	// Nothing may be left behind: a refused generation that still wrote the
	// file would hand the user the broken build it just warned about.
	if _, statErr := os.Stat(filepath.Join(appDir, "servo.prod_gen.go")); !os.IsNotExist(statErr) {
		t.Errorf("servo.prod_gen.go exists after a refused generation (stat err = %v)", statErr)
	}

	// check must report it too — an overlapping pair committed before this
	// guard existed is exactly what CI should refuse.
	if err := runCheck(taggedCfg(dir, "prod")); err == nil {
		t.Error("check --tags=prod = nil, want the same refusal")
	}
}

// TestGenerateAllowsProperlyExcludedVariants is the other half: once the
// spec files exclude each other, the same two variants are fine.
func TestGenerateAllowsProperlyExcludedVariants(t *testing.T) {
	dir := writeVariantModule(t, "example.com/nooverlapgen")
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("generate (default): %v", err)
	}
	if err := runGenerate(taggedCfg(dir, "prod")); err != nil {
		t.Fatalf("generate --tags=prod: %v", err)
	}
	runGoBuild(t, dir, "")
	runGoBuild(t, dir, "prod")
}

// TestDoctorReportsAnInjectorMissingThisVariant covers the multi-injector
// case the docs recommend: generating the whole module with --tags=prod
// when only one injector has a prod spec. Generation must succeed for the
// injector that has one, and doctor must name the one that does not —
// otherwise the first sign is `undefined: New` from the compiler.
func TestDoctorReportsAnInjectorMissingThisVariant(t *testing.T) {
	dir := writeTwoInjectorVariantModule(t, "example.com/missingvariant")

	if err := runGenerate(taggedCfg(dir, "prod")); err != nil {
		t.Fatalf("generate --tags=prod: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "cmd", "app", "servo.prod_gen.go")); err != nil {
		t.Fatalf("expected cmd/app's prod variant to be generated: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runDoctor(taggedCfg(dir, "prod")); err == nil {
			t.Error("doctor --tags=prod = nil, want a problem: cmd/worker has no prod variant")
		}
	})
	if !strings.Contains(out, "cmd/worker") {
		t.Errorf("doctor --tags=prod should name cmd/worker, got:\n%s", out)
	}

	// The default configuration has both injectors, so it stays clean.
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("generate (default): %v", err)
	}
	if err := runDoctor(cfg(dir)); err != nil {
		t.Errorf("doctor (default) = %v, want clean", err)
	}
}

func TestIsVariantFileName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"servo_gen.go", true},
		{"servo.prod_gen.go", true},
		{"servo.integration-prod_gen.go", true},
		{"servo.sqlite_omit_gen.go", true},
		// Another generator's output that happens to start with "servo".
		// Treating it as a variant would compare its constraint, find an
		// overlap, and brick generate and check for the whole package.
		{"servo_mocks_gen.go", false},
		{"servo_stringer_gen.go", false},
		{"servo.Prod_gen.go", false}, // uppercase is not a tag servo accepts
		{"servo.a-b_gen.go", true},
		{"servo._gen.go", false},
		{"servo_gen_test.go", false}, // the override variant, handled separately
		{"other_gen.go", false},
		{"servo_gen.txt", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isVariantFileName(c.name); got != c.want {
				t.Errorf("isVariantFileName(%q) = %v, want %v", c.name, got, c.want)
			}
		})
	}
}

// TestGenerateIgnoresForeignGeneratedFiles guards the overlap check against
// its own worst failure mode. The glob that finds sibling variants is
// loose, and a moq/stringer file named servo_mocks_gen.go would otherwise
// be compared, found to overlap, and block every generate and check in the
// package — a regression far worse than the collision the check prevents.
func TestGenerateIgnoresForeignGeneratedFiles(t *testing.T) {
	dir := writeVariantModule(t, "example.com/foreigngen")
	mustWriteFile(t, dir, "cmd/app/servo_mocks_gen.go", `// Code generated by moq. DO NOT EDIT.

package main

type StoreMock struct{}
`)

	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("generate (default): %v", err)
	}
	if err := runGenerate(taggedCfg(dir, "prod")); err != nil {
		t.Fatalf("generate --tags=prod: %v", err)
	}
	if err := runCheck(taggedCfg(dir, "prod")); err != nil {
		t.Errorf("check --tags=prod: %v", err)
	}
}

// TestRemediationNamesTheTags: telling someone whose prod variant is
// missing to "run `servo generate`" sends them to a command that
// regenerates the default variant and leaves the missing file missing.
func TestRemediationNamesTheTags(t *testing.T) {
	dir := writeVariantModule(t, "example.com/remediation")
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("generate (default): %v", err)
	}

	err := runCheck(taggedCfg(dir, "prod"))
	if err == nil {
		t.Fatal("check --tags=prod = nil, want a missing-variant report")
	}
	if !strings.Contains(err.Error(), "servo generate --tags=prod") {
		t.Errorf("remediation should name the flags that would produce this variant, got:\n%v", err)
	}
}

// TestGenerateWritesNothingWhenAnyInjectorOverlaps: an overlap is a
// property of the output layout, so discovering one halfway through a
// multi-injector module would leave some injectors with a new variant and
// others without, from a command that exited non-zero.
func TestGenerateWritesNothingWhenAnyInjectorOverlaps(t *testing.T) {
	dir := writeTwoInjectorVariantModule(t, "example.com/allornothing")
	// cmd/worker's spec is gated `servoinject && !prod`; widening it to a
	// plain `servoinject` makes its prod variant overlap its default one,
	// while cmd/app stays perfectly well-formed.
	mustWriteFile(t, dir, "cmd/worker/spec.go", strings.Replace(
		readFile(t, filepath.Join(dir, "cmd", "worker", "spec.go")),
		"//go:build servoinject && !prod", "//go:build servoinject", 1))

	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("generate (default): %v", err)
	}
	before := readFile(t, filepath.Join(dir, "cmd", "app", "servo_gen.go"))

	if err := runGenerate(taggedCfg(dir, "prod")); err == nil {
		t.Fatal("generate --tags=prod = nil, want the overlap refusal from cmd/worker")
	}

	// cmd/app is well-formed, but nothing may have been written for it.
	if _, err := os.Stat(filepath.Join(dir, "cmd", "app", "servo.prod_gen.go")); !os.IsNotExist(err) {
		t.Errorf("cmd/app's prod variant was written despite cmd/worker failing (stat err = %v)", err)
	}
	if after := readFile(t, filepath.Join(dir, "cmd", "app", "servo_gen.go")); after != before {
		t.Error("cmd/app's default variant was rewritten by a run that failed")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestVariantTags(t *testing.T) {
	cases := []struct {
		name string
		want []string
		ok   bool
	}{
		{"servo_gen.go", nil, true},
		{"servo_gen_test.go", nil, true},
		{"servo.prod_gen.go", []string{"prod"}, true},
		{"servo.prod_gen_test.go", []string{"prod"}, true},
		{"servo.integration-prod_gen.go", []string{"integration", "prod"}, true},
		{"servo_mocks_gen.go", nil, false},
		{"other.go", nil, false},
		// Every segment has to be a tag servo itself would accept, or the
		// inventory invents a tag set no `servo generate --tags=...` can
		// reproduce — and then tells the user to run exactly that command
		// to refresh the file. Uppercase is not a tag servo accepts, and
		// one bad segment disqualifies the whole name.
		{"servo.Prod_gen.go", nil, false},
		{"servo.prod-Staging_gen.go", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := variantTags(c.name)
			if ok != c.ok {
				t.Fatalf("variantTags(%q) ok = %v, want %v", c.name, ok, c.ok)
			}
			if ok && strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Errorf("variantTags(%q) = %v, want %v", c.name, got, c.want)
			}
		})
	}
}

// TestDoctorReportsAnOrphanedVariant covers the one hole no other command
// can see: delete a variant's spec and its generated file stays behind,
// still compiling into whichever build satisfies its constraint and never
// regenerated again. generate, check and doctor all used to report clean
// next to a build that had silently frozen.
func TestDoctorReportsAnOrphanedVariant(t *testing.T) {
	dir := writeVariantModule(t, "example.com/orphanvariant")
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("generate (default): %v", err)
	}
	if err := runGenerate(taggedCfg(dir, "prod")); err != nil {
		t.Fatalf("generate --tags=prod: %v", err)
	}

	// Healthy first: two variants, both owned, nothing to report.
	healthy := captureStdout(t, func() {
		if err := runDoctor(cfg(dir)); err != nil {
			t.Errorf("doctor on a healthy two-variant project = %v, want clean", err)
		}
	})
	if strings.Contains(healthy, "no longer exists") {
		t.Errorf("healthy project reported an orphan:\n%s", healthy)
	}
	// It must still say the prod variant exists and was not checked here,
	// or a stale one stays invisible to anyone not passing its flags.
	if !strings.Contains(healthy, "servo.prod_gen.go") {
		t.Errorf("doctor should list the variants it did not check, got:\n%s", healthy)
	}

	if err := os.Remove(filepath.Join(dir, "cmd", "app", "spec_prod.go")); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if err := runDoctor(cfg(dir)); err == nil {
			t.Error("doctor = nil after the prod spec was deleted, want the orphan reported")
		}
	})
	if !strings.Contains(out, "servo.prod_gen.go is generated from a spec that no longer exists") {
		t.Errorf("expected an orphan report naming servo.prod_gen.go, got:\n%s", out)
	}
}

// TestDoctorDoesNotCallAForeignFileAnOrphan: the inventory reads files
// servo did not write, so it has to recognise them as not its business.
func TestDoctorDoesNotCallAForeignFileAnOrphan(t *testing.T) {
	dir := writeVariantModule(t, "example.com/foreignorphan")
	mustWriteFile(t, dir, "cmd/app/servo_mocks_gen.go", `// Code generated by moq. DO NOT EDIT.

package main

type StoreMock struct{}
`)
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("generate: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runDoctor(cfg(dir)); err != nil {
			t.Errorf("doctor = %v, want clean", err)
		}
	})
	if strings.Contains(out, "servo_mocks_gen.go") {
		t.Errorf("doctor treated another generator's file as a servo variant:\n%s", out)
	}
}

// TestCheckVariantOverlapBlamesServoForItsOwnBadConstraint: ownConstraint
// is not user input — it is what servo's own emitter is about to write
// into the file. If it does not parse, servo was about to generate
// something the Go compiler would reject, and the message has to say so.
// Reporting it as an ordinary constraint problem would send the author
// hunting through their spec files for a mistake that is not there.
func TestCheckVariantOverlapBlamesServoForItsOwnBadConstraint(t *testing.T) {
	err := checkVariantOverlap(t.TempDir(), generatedFileName, "!servoinject &&")
	if err == nil {
		t.Fatal("checkVariantOverlap = nil, want a refusal for a constraint servo cannot parse")
	}
	for _, want := range []string{"this is a servo bug", "!servoinject &&"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%v", want, err)
		}
	}
}

// TestCheckVariantOverlapReportsADirectoryItCannotScan: the sibling scan
// is a glob over the injector's own directory, and a checkout whose path
// contains an unmatched '[' — legal on every filesystem servo runs on —
// makes that pattern invalid. Swallowing the failure would read as "no
// siblings, nothing to collide with", silently turning the collision check
// off for that project instead of failing where someone can see it.
func TestCheckVariantOverlapReportsADirectoryItCannotScan(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "proj[1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := checkVariantOverlap(dir, generatedFileName, "!servoinject"); err == nil {
		t.Fatal("checkVariantOverlap = nil, want the failed scan reported rather than treated as an empty directory")
	}
}

// TestCheckVariantOverlapIgnoresAFileServoDidNotWrite is the other half of
// TestGenerateIgnoresForeignGeneratedFiles, one layer down and on the case
// that filter cannot catch: a file named exactly the way servo names a
// variant, carrying a constraint that really does overlap, but without
// servo's generated-code banner. Matching the name is not proof of
// authorship, and refusing to generate on account of someone else's file
// would brick generate and check for the whole package — worse than the
// collision the check exists to prevent.
func TestCheckVariantOverlapIgnoresAFileServoDidNotWrite(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir, "servo.staging_gen.go", `//go:build !servoinject

package main

type Staging struct{}
`)

	if err := checkVariantOverlap(dir, generatedFileName, "!servoinject"); err != nil {
		t.Fatalf("checkVariantOverlap = %v, want nil: the sibling carries no servo banner, so servo did not write it", err)
	}
}

// TestCheckVariantOverlapSkipsASiblingConstraintItCannotParse: a file whose
// //go:build line is not valid build syntax does not compile in any
// configuration, so it can never be the second half of a collision.
// Erroring on it would block every generate in the package on a file the
// compiler is already rejecting for its own reasons.
func TestCheckVariantOverlapSkipsASiblingConstraintItCannotParse(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir, "servo.staging_gen.go", `//go:build !servoinject &&

// Code generated by servo generate. DO NOT EDIT.

package main

type Staging struct{}
`)

	if err := checkVariantOverlap(dir, generatedFileName, "!servoinject"); err != nil {
		t.Fatalf("checkVariantOverlap = %v, want nil for a sibling whose constraint does not parse", err)
	}
}

// TestServoGeneratedConstraint covers the answers the overlap scan and
// doctor's inventory both hang off: a file is servo's only when its header
// carries both a //go:build line and servo's banner, and "not servo's" has
// to be the answer for everything else rather than an error that stops the
// scan.
func TestServoGeneratedConstraint(t *testing.T) {
	dir := t.TempDir()
	// A header that never reaches a package clause at all — the scanner
	// runs off the end of the file without ever seeing a banner.
	mustWriteFile(t, dir, "servo.headeronly_gen.go", "//go:build !servoinject\n\n// notes, and then nothing\n")
	mustWriteFile(t, dir, "servo.prod_gen.go", `//go:build !servoinject && prod

// Code generated by servo generate. DO NOT EDIT.

package main
`)

	cases := []struct {
		name string
		path string
		want string
		ok   bool
	}{
		{
			// A sibling deleted between the glob and the read — two
			// `servo generate` runs racing each other. A file that is not
			// there cannot collide with anything.
			name: "a file that is gone",
			path: filepath.Join(dir, "servo.vanished_gen.go"),
			ok:   false,
		},
		{
			name: "a header that ends before any banner",
			path: filepath.Join(dir, "servo.headeronly_gen.go"),
			ok:   false,
		},
		{
			name: "servo's own output",
			path: filepath.Join(dir, "servo.prod_gen.go"),
			want: "//go:build !servoinject && prod",
			ok:   true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := servoGeneratedConstraint(c.path)
			if ok != c.ok {
				t.Fatalf("servoGeneratedConstraint(%s) ok = %v, want %v", filepath.Base(c.path), ok, c.ok)
			}
			if got != c.want {
				t.Errorf("servoGeneratedConstraint(%s) = %q, want %q", filepath.Base(c.path), got, c.want)
			}
		})
	}
}

// TestDiscoverVariantsReportsADirectoryItCannotScan is doctor's side of
// the same glob failure. The inventory is the only thing in servo that
// ever sees an orphaned or unverified variant, and an empty one is exactly
// what a healthy single-variant project produces — so a directory that
// cannot be scanned has to come back as an error rather than as silence.
func TestDiscoverVariantsReportsADirectoryItCannotScan(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "proj[1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	variants, err := discoverVariants(dir, nil)
	if err == nil {
		t.Fatalf("discoverVariants = %v, nil error; want the failed scan reported rather than an empty inventory", variants)
	}
}

// TestGenerateOneRefusesAnOverlapItsCallerAlreadyChecked: runGenerate
// checks every injector's overlap up front, before writing anything, which
// makes generateOne's own check look redundant. It is not — it is what
// stops any future caller that skips the pre-pass from writing a pair of
// files that cannot compile together — and because runGenerate can never
// reach it, direct is the only way it is ever exercised.
func TestGenerateOneRefusesAnOverlapItsCallerAlreadyChecked(t *testing.T) {
	// The spec is a plain `//go:build servoinject`, so its prod variant
	// would not exclude its default one.
	dir := writeAppModule(t, "example.com/overlapgenone", true, "")
	if err := runGenerate(cfg(dir)); err != nil {
		t.Fatalf("generate (default): %v", err)
	}

	pipelines, err := buildPipelines(taggedCfg(dir, "prod"))
	if err != nil {
		t.Fatalf("buildPipelines --tags=prod: %v", err)
	}
	if len(pipelines) != 1 {
		t.Fatalf("got %d pipelines, want exactly 1", len(pipelines))
	}

	err = generateOne(pipelines[0])
	if err == nil || !strings.Contains(err.Error(), "would both compile") {
		t.Fatalf("generateOne = %v, want the overlap refusal", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "cmd", "app", "servo.prod_gen.go")); !os.IsNotExist(statErr) {
		t.Errorf("servo.prod_gen.go exists after a refused generation (stat err = %v)", statErr)
	}
}
