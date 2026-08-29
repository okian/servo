package main

import (
	"bufio"
	"fmt"
	"go/build/constraint"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/okian/servo/v3/internal/load"
)

// variantFileName is the emitted file's name, placed alongside the spec
// file it was generated from. test selects the servotest override variant
// — a _test.go file so it compiles only during `go test`, since
// NewTestApp/TestApp have no reason to exist in the real binary.
//
// With no tags the names are the historical servo_gen.go and
// servo_gen_test.go. With tags they gain a dot-separated segment naming
// the variant: servo.prod_gen.go, servo.integration-prod_gen.go.
//
// The two separators are both load-bearing, and both are the opposite of
// the obvious choice:
//
// The leading dot, not an underscore, because Go gives a file an implicit
// GOOS/GOARCH constraint from its underscore-separated suffix. A
// servo_gen_linux.go would be silently ignored on every non-Linux machine
// — generated, committed and invisible. go/build's goodOSArchFile cuts the
// name at the *first* dot before looking for underscores, so everything
// after "servo." is structurally invisible to that rule, for any tag, with
// no list of reserved names to keep up to date.
//
// The dash between tags, because "-" is the one separator that cannot
// appear in a build tag (go/build/constraint rejects it), while "_" can:
// joining with "_" would map the tag sets {a_b} and {a, b} to the same
// file, and one would silently overwrite the other.
func variantFileName(tags []string, test bool) string {
	suffix := generatedSuffix
	if test {
		suffix = generatedTestSuffix
	}
	if len(tags) == 0 {
		return generatedPrefix + suffix
	}
	return generatedPrefix + "." + strings.Join(tags, "-") + suffix
}

const (
	generatedPrefix     = "servo"
	generatedSuffix     = "_gen.go"
	generatedTestSuffix = "_gen_test.go"

	// generatedFileName and generatedTestFileName are what a generation
	// with no build tags writes — the names servo has always used, kept
	// as named constants because that case is the overwhelmingly common
	// one and must stay byte-for-byte unchanged.
	generatedFileName     = generatedPrefix + generatedSuffix
	generatedTestFileName = generatedPrefix + generatedTestSuffix
)

// checkVariantOverlap refuses to write a generated file that could compile
// alongside one already sitting next to it.
//
// Servo derives a variant's constraint from the spec file's own and never
// invents a negation, which is what frees it from having to know the full
// variant set. The cost is that nothing stops an author from generating
// two variants whose constraints are both satisfiable at once — the plain
// `//go:build servoinject` that `servo init` scaffolds, generated a second
// time with --tags=prod, yields `!servoinject` beside `!servoinject &&
// prod`, and `go build -tags=prod` then compiles both and fails with App
// and New declared twice.
//
// Detecting that is servo's job; resolving it is not. Rewriting a sibling
// file the caller did not name — to insert the `&& !prod` that would fix
// it — would make generation depend on which files happen to be in the
// working tree. So this reports, precisely, and stops.
func checkVariantOverlap(dir, ownName, ownConstraint string) error {
	own, err := load.ParseConstraint("//go:build " + ownConstraint)
	if err != nil {
		return fmt.Errorf("servo: generated build constraint %q is not parseable (this is a servo bug): %w", ownConstraint, err)
	}

	siblings, err := filepath.Glob(filepath.Join(dir, generatedPrefix+"*"+generatedSuffix))
	if err != nil {
		return err
	}
	sort.Strings(siblings)

	for _, path := range siblings {
		name := filepath.Base(path)
		if name == ownName || !isVariantFileName(name) {
			continue
		}
		line, ok := servoGeneratedConstraint(path)
		if !ok {
			// Not servo's output: another generator's file that happens to
			// start with "servo", or one with no constraint at all.
			continue
		}
		other, err := load.ParseConstraint(line)
		if err != nil {
			continue
		}
		if !load.ConstraintsOverlap(own, other) {
			continue
		}
		// The two example constraints are built before the message rather
		// than interpolated inside it: writing them with an explicit
		// argument index picked up the generated *file* name instead of
		// the build tag, and because an explicit index also suppresses
		// fmt's %!(EXTRA) marker, the message printed a paste-in fix that
		// was not a build tag at all — with nothing to say so.
		exclusive := fmt.Sprintf("`//go:build %s && !prod`", load.BuildTag)
		variant := fmt.Sprintf("`//go:build %s && prod`", load.BuildTag)
		return fmt.Errorf(`servo: %s and %s would both compile in the same build

  %-*s %s
  %-*s //go:build %s

Some build satisfies both constraints at once, and the package would then
declare App and New twice. Servo mirrors each spec file's own constraint and
never invents a negation, so the exclusion has to come from the spec files.
Either gate them so no configuration matches two — %s
on the default spec, %s on the other — or, if this
injector does not vary with these tags at all, leave it alone and scope the
run to the one that does with --dir`,
			ownName, name,
			len(ownName)+1, name+":", line,
			len(ownName)+1, ownName+":", ownConstraint,
			exclusive, variant)
	}
	return nil
}

// isVariantFileName reports whether name is one servo itself would write:
// exactly servo_gen.go, or servo.<tag>[-<tag>...]_gen.go with every segment
// a legal build tag.
//
// The glob that finds candidates is deliberately loose, and on its own it
// is wrong: a servo_mocks_gen.go from moq, or any other generator whose
// output happens to start with "servo", is not a variant of anything. Left
// unfiltered it would be compared, found to overlap (a mocks file declares
// neither App nor New, but its constraint does not say so), and every
// generate and check in that package would fail with no way to proceed.
func isVariantFileName(name string) bool {
	rest, ok := strings.CutSuffix(name, generatedSuffix)
	if !ok {
		return false
	}
	if rest == generatedPrefix {
		return true
	}
	segment, ok := strings.CutPrefix(rest, generatedPrefix+".")
	if !ok || segment == "" {
		return false
	}
	for tag := range strings.SplitSeq(segment, "-") {
		if load.ValidTag(tag) != nil {
			return false
		}
	}
	return true
}

// servoGeneratedConstraint returns the //go:build line of a file servo
// generated, or false for anything else.
//
// Both halves matter. Only the header is scanned, because a //go:build
// below the package clause is not a constraint. And the file must carry
// servo's own generated-code marker: matching the name is not proof of
// authorship, and refusing to generate on account of a file servo did not
// write would be worse than the collision the check exists to prevent.
func servoGeneratedConstraint(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()

	constraintLine := ""
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case strings.HasPrefix(line, "//go:build"):
			constraintLine = line
		case strings.HasPrefix(line, generatedMarker):
			return constraintLine, constraintLine != ""
		case line == "" || strings.HasPrefix(line, "//"):
			continue
		default:
			return "", false // reached the package clause without a marker
		}
	}
	return "", false
}

// generatedMarker is the standard banner every servo-generated file
// carries, and the only reliable evidence that servo wrote it.
const generatedMarker = "// Code generated by servo generate. DO NOT EDIT."

// regenerateCommand is the command that would actually produce this
// variant. `run `+"`servo generate`"+“ is wrong advice for a tagged one — it
// regenerates the default variant and leaves the missing file missing.
func regenerateCommand(tags []string) string {
	if len(tags) == 0 {
		return "`servo generate`"
	}
	return "`servo generate --tags=" + strings.Join(tags, ",") + "`"
}

// variantTags is the inverse of variantFileName: the tag set a generated
// file's name encodes, or false when the name is not one servo writes.
func variantTags(name string) ([]string, bool) {
	rest, ok := strings.CutSuffix(name, generatedTestSuffix)
	if !ok {
		rest, ok = strings.CutSuffix(name, generatedSuffix)
	}
	if !ok {
		return nil, false
	}
	if rest == generatedPrefix {
		return nil, true
	}
	segment, ok := strings.CutPrefix(rest, generatedPrefix+".")
	if !ok || segment == "" {
		return nil, false
	}
	tags := strings.Split(segment, "-")
	for _, tag := range tags {
		if load.ValidTag(tag) != nil {
			return nil, false
		}
	}
	return tags, true
}

// generatedVariant is one servo-generated file found next to a spec.
type generatedVariant struct {
	name string
	tags []string
	// owned is false when no spec file in the directory — visible under
	// these tags or excluded by them — would produce this file. That means
	// the spec it came from is gone while the generated file remains: it
	// still compiles into whichever build satisfies its constraint, and
	// nothing regenerates it ever again.
	owned bool
}

// discoverVariants inventories the servo-generated files sitting next to a
// spec, and decides which of them any spec still accounts for.
//
// This is the one place servo reads its own output back, and it is
// deliberately limited to reporting. A variant is "owned" when running
// servo with the tags its name encodes would make some spec file in that
// directory visible — which is exactly the question "could this file have
// been generated from what is here now?".
func discoverVariants(dir string, specConstraints []constraint.Expr) ([]generatedVariant, error) {
	paths, err := filepath.Glob(filepath.Join(dir, generatedPrefix+"*"+"_gen*.go"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)

	var out []generatedVariant
	for _, path := range paths {
		name := filepath.Base(path)
		tags, ok := variantTags(name)
		if !ok {
			continue
		}
		if _, isServo := servoGeneratedConstraint(path); !isServo && !strings.HasSuffix(name, generatedTestSuffix) {
			continue
		}
		owned := false
		for _, expr := range specConstraints {
			if load.ConstraintSatisfiedBy(expr, append([]string{load.BuildTag}, tags...)) {
				owned = true
				break
			}
		}
		out = append(out, generatedVariant{name: name, tags: tags, owned: owned})
	}
	return out, nil
}
