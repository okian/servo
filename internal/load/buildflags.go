package load

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// BuildFlags are the go command build flags servo passes through to
// go/packages, named and spelled exactly as the go command spells them.
// The zero value passes nothing through beyond BuildTag, which is what an
// invocation with no flags at all gets.
//
// Only flags that change *which files or packages get loaded* are here.
// Flags that only affect what the compiler or linker emits — -race,
// -cover, -trimpath, -pgo — are deliberately absent: servo runs neither.
// (-race does activate the `race` build tag, so `-tags=race` expresses it
// for anyone who genuinely wants a race-specific graph.)
type BuildFlags struct {
	// Tags is -tags: a comma-separated list of additional build tags to
	// consider satisfied during the load. BuildTag is always added on top
	// and cannot be switched off. The deprecated space-separated form the
	// go command still accepts is accepted here too.
	//
	// Deliberately NOT seeded from GOFLAGS, unlike the way the go command
	// treats its own -tags. Servo's output is a committed artifact, so it
	// has to be a pure function of the repository and the flags actually
	// typed. Letting a GOFLAGS=-tags=prod in a shell profile or a CI image
	// through would resolve a different graph and write it into the file
	// named for the default configuration — carrying prod-only providers
	// under a `!servoinject` constraint that claims to compile everywhere,
	// with nothing in the diff to explain it. An inherited tag can change
	// what you build; it must not change what servo commits.
	Tags string

	// Mod is -mod: "", "readonly", "vendor" or "mod". Empty omits the
	// flag entirely, leaving the go command its own default (vendor when
	// a vendor directory is present, else readonly).
	Mod string

	// ModFile is -modfile: an alternate go.mod to read. servo never
	// writes it.
	ModFile string

	// Overlay is -overlay: a JSON config file providing an overlay for
	// build operations. Safe only while servo leaves
	// packages.Config.Overlay unset — go/packages appends its own
	// -overlay after Config.BuildFlags and the go command takes the last
	// one, so an in-memory overlay added later would silently win.
	Overlay string
}

// TagList is the canonical form of Tags: split, deduplicated, sorted, and
// with BuildTag removed. Sorting is what makes a variant's identity — its
// generated file's name and build constraint — independent of the order
// the tags were typed in, so `-tags=a,b` and `-tags=b,a` are the same
// variant rather than two files that overwrite each other.
func (b BuildFlags) TagList() []string {
	return canonicalTags(b.Tags)
}

func canonicalTags(tags string) []string {
	fields := strings.FieldsFunc(tags, func(r rune) bool {
		return r == ',' || unicode.IsSpace(r)
	})
	// BuildTag is seeded as already-seen: servo adds it unconditionally,
	// so a user who passes it explicitly is redundant, not the author of
	// a distinct `servoinject` variant.
	seen := map[string]bool{BuildTag: true}
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// Args renders b as the argv the go command would receive — exactly what
// goes into packages.Config.BuildFlags.
//
// BuildTag is merged into the single -tags value rather than appended as a
// second -tags flag, because the go command takes the *last* -tags and a
// second one would drop servoinject, making every spec file invisible.
func (b BuildFlags) Args() []string {
	tags := append([]string{BuildTag}, b.TagList()...)
	args := []string{"-tags=" + strings.Join(tags, ",")}
	for _, f := range []struct{ name, value string }{
		{"mod", b.Mod},
		{"modfile", b.ModFile},
	} {
		if f.value != "" {
			args = append(args, "-"+f.name+"="+f.value)
		}
	}
	// -overlay is deliberately absent: it is applied through
	// packages.Config.Overlay instead, so that go/packages parses the same
	// content it tells the go command about. See readOverlay.
	return args
}

// String renders b as the flags a user would have typed, omitting
// BuildTag, or "" for the zero value — for diagnostics that need to name
// the configuration a file was generated under.
func (b BuildFlags) String() string {
	var parts []string
	if tags := b.TagList(); len(tags) > 0 {
		parts = append(parts, "-tags="+strings.Join(tags, ","))
	}
	for _, f := range []struct{ name, value string }{
		{"mod", b.Mod},
		{"modfile", b.ModFile},
		{"overlay", b.Overlay},
	} {
		if f.value != "" {
			parts = append(parts, "-"+f.name+"="+f.value)
		}
	}
	return strings.Join(parts, " ")
}

// Validate rejects tags servo cannot honour, before they reach the go
// command and fail in a way that points at the standard library instead of
// at the flag. Returns nil for the zero value.
//
// Every other GOFLAGS entry reaches the go command untouched, and a
// GOFLAGS the go command itself accepts is not servo's to veto.
func (b BuildFlags) Validate() error {
	for _, tag := range b.TagList() {
		if err := validateTag(tag); err != nil {
			return err
		}
	}
	return nil
}

// ValidTag reports whether tag is one servo will accept, returning the
// same error Validate would. Exported so a caller reading generated file
// names back off disk applies exactly the rule that produced them.
func ValidTag(tag string) error { return validateTag(tag) }

func validateTag(tag string) error {
	// Matches go/build/constraint's own isValidTag: a tag that fails this
	// cannot appear in a //go:build line, so servo could never emit a
	// constraint selecting the variant. The go command accepts such a tag
	// on the command line without complaint, which is precisely why servo
	// has to be the one to say so.
	for _, c := range tag {
		if !unicode.IsLetter(c) && !unicode.IsDigit(c) && c != '_' && c != '.' {
			return fmt.Errorf("servo: build tag %q contains %q, which is not a legal build tag character (letters, digits, _ and . only) — no //go:build line could select it", tag, string(c))
		}
	}
	if tag != strings.ToLower(tag) {
		// Not a Go rule — a servo one, and the reason is the generated
		// file's name. Variant filenames are derived from the tag set, and
		// on a case-insensitive filesystem (APFS, NTFS) `prod` and `Prod`
		// would name the same file and silently destroy each other.
		return fmt.Errorf("servo: build tag %q must be lowercase — variant file names are derived from the tag set, and %q would collide with %q on a case-insensitive filesystem", tag, tag, strings.ToLower(tag))
	}
	if why, reserved := reservedTags[tag]; reserved {
		return fmt.Errorf("servo: build tag %q cannot gate a variant: %s", tag, why)
	}
	if isGoVersionTag(tag) {
		return fmt.Errorf("servo: build tag %q cannot gate a variant: the toolchain sets a tag for its own release and every earlier one, so it is already true without being passed", tag)
	}
	return nil
}

// isGoVersionTag matches the go1.N tags the toolchain sets for itself and
// every earlier release.
func isGoVersionTag(tag string) bool {
	rest, ok := strings.CutPrefix(tag, "go1.")
	if !ok || rest == "" {
		return false
	}
	for _, c := range rest {
		if !unicode.IsDigit(c) {
			return false
		}
	}
	return true
}

// reservedTags are tags that cannot meaningfully gate a variant, mapped to
// the reason, because the failure they produce otherwise points somewhere
// unhelpful.
//
// The GOOS/GOARCH names are the sharp case: passing one through -tags does
// not select a platform, it *adds* a second one, and the build then fails
// inside the standard library ("GOOS redeclared in this block") with
// nothing pointing back at servo. go/build keeps the authoritative lists in
// internal/syslist with no exported accessor, so this is a copy. It is
// append-only upstream, and a GOOS added after this was written simply
// falls through to the go command's own error rather than getting the good
// message — a stale entry cannot cause a wrong result.
var reservedTags = func() map[string]string {
	const (
		platform = "it names a GOOS/GOARCH, and passing one as a build tag adds a second platform rather than selecting it, breaking the standard library — set the GOOS/GOARCH environment variable when running servo instead"
		implicit = "the toolchain already sets it for the build it describes, so it is true without being passed and cannot distinguish one variant from another"
	)
	m := map[string]string{}
	for _, os := range []string{
		"aix", "android", "darwin", "dragonfly", "freebsd", "hurd", "illumos",
		"ios", "js", "linux", "nacl", "netbsd", "openbsd", "plan9", "solaris",
		"wasip1", "windows", "zos",
	} {
		m[os] = platform
	}
	for _, arch := range []string{
		"386", "amd64", "amd64p32", "arm", "armbe", "arm64", "arm64be",
		"loong64", "mips", "mipsle", "mips64", "mips64le", "mips64p32",
		"mips64p32le", "ppc", "ppc64", "ppc64le", "riscv", "riscv64",
		"s390", "s390x", "sparc", "sparc64", "wasm",
	} {
		m[arch] = platform
	}
	// race/msan/asan are deliberately absent: the toolchain sets those only
	// when -race/-msan/-asan is passed, so unlike the tags below they are
	// false by default and can gate a variant like any other. `-tags=race`
	// is how servo expresses "the graph as it is under go test -race".
	for _, tag := range []string{
		"unix", "cgo", "gc", "gccgo", "boringcrypto",
	} {
		m[tag] = implicit
	}
	// `ignore` is the ecosystem's universal "never build this file" tag —
	// the standard library's own generator sources use it. Passing it does
	// not select anything; it switches every deliberately-excluded file in
	// the module and its dependencies back on, and the build then fails
	// somewhere in the standard library. Same shape of failure as the GOOS
	// names, same reason to catch it here.
	m["ignore"] = "it conventionally means `never build this file`, so setting it compiles files that were written to be excluded — including ones in the standard library"
	return m
}()

// describeConfiguration names the build configuration in prose, for error
// messages that have to tell a user which one they were in.
func (b BuildFlags) describeConfiguration() string {
	tags := b.TagList()
	if len(tags) == 0 {
		return "the default one, with no build tags"
	}
	return "-tags=" + strings.Join(tags, ",")
}
