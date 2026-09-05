package load

import (
	"strings"
	"testing"
)

// loadSpecWithConfigFile materializes a module whose spec carries the
// given extra marker lines and parses it, so each test reads exactly like
// the spec file a user would write.
func loadSpecWithConfigFile(t *testing.T, modName, markerLines string) (*Spec, error) {
	t.Helper()
	dir := t.TempDir()
	root := repoRoot(t)
	mustWriteFile(t, dir, "go.mod", "module example.com/"+modName+"\n\ngo 1.23\n\nrequire github.com/okian/servo/v3 v3.0.0\n\nreplace github.com/okian/servo/v3 => "+root+"\n")
	mustWriteFile(t, dir, "api/api.go", "package api\n\ntype Server struct{}\n\nfunc New() *Server { return &Server{} }\n")
	mustWriteFile(t, dir, "spec/spec.go", `//go:build servoinject

package spec

import (
	"example.com/`+modName+`/api"
	"github.com/okian/servo/v3/servo"
)

func Wire() {
	servo.Build(
		servo.Root[*api.Server](),
`+markerLines+`	)
}
`)
	runGoModTidy(t, dir)

	loaded, err := Load(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return FindSpec(loaded)
}

func TestFindSpecParsesConfigFile(t *testing.T) {
	spec, err := loadSpecWithConfigFile(t, "cfgfile", "\t\tservo.ConfigFile(\"config.yaml\"),\n")
	if err != nil {
		t.Fatalf("FindSpec: %v", err)
	}
	if spec.ConfigFile == nil || spec.ConfigFile.Path != "config.yaml" {
		t.Fatalf("ConfigFile = %+v, want path config.yaml", spec.ConfigFile)
	}
	if spec.ConfigFile.Pos.Line == 0 {
		t.Fatal("ConfigFile declaration position not captured")
	}
}

func TestFindSpecConfigFileErrors(t *testing.T) {
	cases := []struct{ name, mod, markers, want string }{
		{"bad extension", "cfgext", "\t\tservo.ConfigFile(\"config.ini\"),\n", "the extension must be .json, .yaml, .yml, or .toml"},
		{"not a literal", "cfgvar", "\t\tservo.ConfigFile(path),\n", "must be a string literal"},
		{"duplicate", "cfgdup", "\t\tservo.ConfigFile(\"a.yaml\"),\n\t\tservo.ConfigFile(\"b.yaml\"),\n", "declared twice"},
		{"empty path", "cfgempty", "\t\tservo.ConfigFile(\"\"),\n", "not a usable path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The "not a literal" spec leaves `path` undefined — that's
			// fine: spec parsing is syntax, and package errors inside an
			// injector package are tolerated by design.
			_, err := loadSpecWithConfigFile(t, tc.mod, tc.markers)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got err=%v, want it to contain %q", err, tc.want)
			}
		})
	}
}
