package load

import (
	"slices"
	"strings"
	"testing"
)

func TestBuildFlagsTagList(t *testing.T) {
	cases := []struct {
		name string
		tags string
		want []string
	}{
		{"empty", "", nil},
		{"single", "prod", []string{"prod"}},
		{"comma separated", "prod,integration", []string{"integration", "prod"}},
		// The go command still accepts the deprecated space-separated
		// form, so servo has to as well or a copy-pasted flag changes
		// meaning between the two tools.
		{"space separated", "prod integration", []string{"integration", "prod"}},
		{"mixed separators", "prod, integration", []string{"integration", "prod"}},
		{"empty fields dropped", "prod,,integration,", []string{"integration", "prod"}},
		{"deduplicated", "prod,prod", []string{"prod"}},
		// Sorted, so -tags=a,b and -tags=b,a are one variant rather than
		// two files that overwrite each other.
		{"sorted", "zeta,alpha,mid", []string{"alpha", "mid", "zeta"}},
		// servo adds servoinject itself; a user repeating it is redundant,
		// not the author of a `servoinject` variant with its own file.
		{"servoinject dropped", "servoinject,prod", []string{"prod"}},
		{"only servoinject", "servoinject", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := BuildFlags{Tags: c.tags}.TagList()
			// slices.Equal already treats a nil slice and an empty one as
			// equal — it compares lengths first — so no separate
			// empty-vs-nil case is needed here.
			if !slices.Equal(got, c.want) {
				t.Errorf("TagList(%q) = %v, want %v", c.tags, got, c.want)
			}
		})
	}
}

func TestBuildFlagsArgs(t *testing.T) {
	cases := []struct {
		name  string
		flags BuildFlags
		want  []string
	}{
		{"zero value still carries the build tag", BuildFlags{}, []string{"-tags=servoinject"}},
		{"tags merged into one flag", BuildFlags{Tags: "prod"}, []string{"-tags=servoinject,prod"}},
		{"tags canonicalised", BuildFlags{Tags: "b,a"}, []string{"-tags=servoinject,a,b"}},
		{"mod passed through", BuildFlags{Mod: "vendor"}, []string{"-tags=servoinject", "-mod=vendor"}},
		{"modfile passed through", BuildFlags{ModFile: "tools.mod"}, []string{"-tags=servoinject", "-modfile=tools.mod"}},
		// -overlay is applied through packages.Config.Overlay, not as a
		// flag: go list would honour a passed-through -overlay while
		// go/packages went on parsing the real files from disk, so the
		// file list and the syntax would disagree.
		{"overlay is NOT passed through as a flag", BuildFlags{Overlay: "o.json"}, []string{"-tags=servoinject"}},
		{
			"all together",
			BuildFlags{Tags: "prod", Mod: "mod", ModFile: "tools.mod", Overlay: "o.json"},
			[]string{"-tags=servoinject,prod", "-mod=mod", "-modfile=tools.mod"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.flags.Args(); !slices.Equal(got, c.want) {
				t.Errorf("Args() = %v, want %v", got, c.want)
			}
		})
	}
}

// TestBuildFlagsArgsEmitsExactlyOneTagsFlag pins the reason BuildTag is
// merged into the single -tags value instead of being appended as its own
// flag: the go command takes the last -tags and ignores every earlier one,
// so a second flag would silently drop servoinject and make every spec
// file invisible.
func TestBuildFlagsArgsEmitsExactlyOneTagsFlag(t *testing.T) {
	args := BuildFlags{Tags: "prod,integration"}.Args()
	n := 0
	for _, a := range args {
		if strings.HasPrefix(a, "-tags=") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("got %d -tags flags in %v, want exactly 1", n, args)
	}
	if !strings.Contains(args[0], BuildTag) {
		t.Errorf("got %q, want it to carry %s", args[0], BuildTag)
	}
}

func TestBuildFlagsString(t *testing.T) {
	cases := []struct {
		name  string
		flags BuildFlags
		want  string
	}{
		{"zero value renders empty", BuildFlags{}, ""},
		// servoinject is invariant, so naming it would be noise in every
		// message that reports the configuration.
		{"build tag omitted", BuildFlags{Tags: "prod"}, "-tags=prod"},
		{"canonicalised", BuildFlags{Tags: "b,a"}, "-tags=a,b"},
		{"other flags included", BuildFlags{Tags: "prod", Mod: "vendor"}, "-tags=prod -mod=vendor"},
		{"only mod", BuildFlags{Mod: "readonly"}, "-mod=readonly"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.flags.String(); got != c.want {
				t.Errorf("String() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestTagsAreNotInheritedFromTheEnvironment pins a deliberate divergence
// from the go command. servo's output is committed, so it has to be a pure
// function of the repository and the flags actually typed: a
// GOFLAGS=-tags=prod in a shell profile or CI image must not resolve a
// different graph and write it into the file named for the default
// configuration, under a `!servoinject` constraint claiming to compile
// everywhere.
func TestTagsAreNotInheritedFromTheEnvironment(t *testing.T) {
	t.Setenv("GOFLAGS", "-tags=prod")

	b := BuildFlags{}
	if got := b.TagList(); len(got) != 0 {
		t.Errorf("TagList() = %v, want empty: the environment must not name a variant", got)
	}
	if got, want := b.Args()[0], "-tags="+BuildTag; got != want {
		t.Errorf("Args()[0] = %q, want %q: the environment must not reach the loader either", got, want)
	}
}

func TestBuildFlagsValidate(t *testing.T) {
	cases := []struct {
		name    string
		tags    string
		wantErr string // substring; "" means the tags must be accepted
	}{
		{"zero value", "", ""},
		{"ordinary tag", "prod", ""},
		{"underscore and dot are legal build tag characters", "sqlite_omit.v2", ""},
		{"digits", "v2", ""},
		{
			"dash is not a legal build tag character",
			"a-b",
			"not a legal build tag character",
		},
		{
			"uppercase would collide on a case-insensitive filesystem",
			"Prod",
			"must be lowercase",
		},
		// Passing a GOOS through -tags adds a second platform rather than
		// selecting one, and the build then fails inside the standard
		// library with nothing pointing back at servo.
		{"GOOS rejected", "linux", "GOOS/GOARCH"},
		{"GOARCH rejected", "arm64", "GOOS/GOARCH"},
		{"unix rejected", "unix", "already sets it"},
		{"cgo rejected", "cgo", "already sets it"},
		// race/msan/asan are set only when -race/-msan/-asan is passed, so
		// unlike unix and cgo they are false by default and can genuinely
		// distinguish one variant from another.
		{"race accepted", "race", ""},
		{"msan accepted", "msan", ""},
		{"go version tag rejected", "go1.21", "every earlier one"},
		// `ignore` is the ecosystem's "never build this file" tag; passing
		// it switches deliberately-excluded files back on across the whole
		// module and the standard library.
		{"ignore rejected", "ignore", "never build this file"},
		// go1 without a minor is an ordinary tag, not a version tag.
		{"go1 alone is ordinary", "go1", ""},
		{"goat is not a version tag", "goat", ""},
		// The version check must not swallow every tag that merely starts
		// like one: `go1.` names no release, and neither does `go1.x`.
		// Rejecting them would refuse a legal tag on the strength of its
		// prefix alone.
		{"go1. with no release number is ordinary", "go1.", ""},
		{"go1.x is not a version tag", "go1.x", ""},
		{"one bad tag among good ones is still reported", "prod,linux", "GOOS/GOARCH"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := BuildFlags{Tags: c.tags}.Validate()
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate(%q) = %v, want nil", c.tags, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("Validate(%q) = %v, want an error containing %q", c.tags, err, c.wantErr)
			}
		})
	}
}

// TestValidTagAgreesWithValidate pins the two entry points to one rule.
// ValidTag exists for the caller that reads generated file names back off
// disk and has to decide which tag set produced them; if it and Validate
// ever disagreed, servo would either refuse to recognize a file it wrote
// itself or claim one it could never have written.
func TestValidTagAgreesWithValidate(t *testing.T) {
	tags := []string{"prod", "integration", "sqlite_omit.v2", "race", "Prod", "a-b", "linux", "arm64", "unix", "ignore", "go1.21"}
	for _, tag := range tags {
		t.Run(tag, func(t *testing.T) {
			direct, viaFlags := ValidTag(tag), BuildFlags{Tags: tag}.Validate()
			if (direct == nil) != (viaFlags == nil) {
				t.Fatalf("ValidTag(%q) = %v but Validate = %v", tag, direct, viaFlags)
			}
			if direct != nil && direct.Error() != viaFlags.Error() {
				t.Errorf("ValidTag(%q) = %q, want the identical diagnostic Validate gives: %q", tag, direct, viaFlags)
			}
		})
	}
}

// TestLoadRejectsUnusableTags confirms Validate is actually wired into
// Load, so a bad tag fails before go/packages is ever invoked — the whole
// point of the check is that the go command's own error for these points
// at the standard library instead of at the flag.
func TestLoadRejectsUnusableTags(t *testing.T) {
	_, err := Load(Config{Dir: t.TempDir(), Build: BuildFlags{Tags: "linux"}})
	if err == nil || !strings.Contains(err.Error(), "GOOS/GOARCH") {
		t.Fatalf("got err=%v, want the GOOS/GOARCH rejection", err)
	}
}

func TestDescribeConfiguration(t *testing.T) {
	cases := []struct {
		name  string
		flags BuildFlags
		want  string
	}{
		{"no tags", BuildFlags{}, "the default one, with no build tags"},
		{"explicit tags", BuildFlags{Tags: "prod"}, "-tags=prod"},
		{"several tags", BuildFlags{Tags: "prod,integration"}, "-tags=integration,prod"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.flags.describeConfiguration(); got != c.want {
				t.Errorf("describeConfiguration() = %q, want %q", got, c.want)
			}
		})
	}
}
