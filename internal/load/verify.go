package load

import (
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/tools/go/packages"
)

// Verify type-checks the packages generated code was written into, with
// that code overlaid, under the configuration the generated files are
// gated for — the user's tags without BuildTag, so the spec file is out
// and the generated file is in, exactly as go build sees them. It returns
// one "file:line:col: message" line per compile error positioned inside a
// file in files, which are the only errors that are servo's to answer
// for, and nothing when there are none. The second result is a failure to
// load at all.
//
// A package go list could not fully describe — a module the first import
// of errgroup or a config decoder has no go.sum line for yet — is skipped
// rather than judged: every type error after a failed import describes
// the import, not the generated code, and `go mod tidy` is the fix the
// compiler already names.
func Verify(cfg Config, files map[string][]byte, pkgPaths []string) ([]string, error) {
	overlay := map[string][]byte{}
	if cfg.Build.Overlay != "" {
		o, err := readOverlay(cfg.Build.Overlay)
		if err != nil {
			return nil, err
		}
		maps.Copy(overlay, o)
	}
	ours := make(map[string]bool, len(files))
	for path, content := range files {
		overlay[path] = content
		ours[filepath.Clean(path)] = true
	}

	pcfg := &packages.Config{
		Dir: cfg.Dir,
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedImports |
			packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo,
		BuildFlags: cfg.Build.GeneratedArgs(),
		Overlay:    overlay,
		// The override variant is a _test.go file, so it only exists in
		// the package's test build.
		Tests: true,
	}
	patterns := slices.Sorted(slices.Values(pkgPaths))
	patterns = slices.Compact(patterns)
	pkgs, err := packages.Load(pcfg, patterns...)
	if err != nil {
		return nil, fmt.Errorf("verify: %w", err)
	}

	var problems []string
	seen := map[string]bool{}
	for _, p := range pkgs {
		if unloadable(p) {
			continue
		}
		for _, e := range p.Errors {
			if e.Kind != packages.TypeError && e.Kind != packages.ParseError {
				continue
			}
			if !ours[filepath.Clean(errorFile(e.Pos))] || seen[e.Error()] {
				continue
			}
			seen[e.Error()] = true
			problems = append(problems, e.Error())
		}
	}
	return problems, nil
}

// unloadable reports whether an import of p could not be resolved — a
// module the go.sum has no line for, a package that is not there at all.
// The type checker still runs, but every error it then reports is the
// import's, not the generated code's: "could not import" at the import
// line and a cascade after it. The package's own list errors are not the
// signal: go list compiles what it loads, so a plain type error in the
// generated file arrives as a list error too, with the compiler's output
// as its message.
func unloadable(p *packages.Package) bool {
	for _, e := range p.Errors {
		if e.Kind == packages.TypeError && strings.HasPrefix(e.Msg, "could not import ") {
			return true
		}
	}
	for _, dep := range p.Imports {
		if slices.ContainsFunc(dep.Errors, func(e packages.Error) bool { return e.Kind == packages.ListError }) {
			return true
		}
	}
	return false
}

// errorPos is the "file:line:col" or "file:line" shape packages.Error.Pos
// takes; the file may itself contain colons on Windows, which is why the
// match is anchored on the trailing numbers rather than the first colon.
var errorPos = regexp.MustCompile(`^(.*?):\d+(?::\d+)?$`)

// errorFile is the file half of a packages.Error position, or "" for an
// error with no position.
func errorFile(pos string) string {
	if m := errorPos.FindStringSubmatch(pos); m != nil {
		return m[1]
	}
	return ""
}
