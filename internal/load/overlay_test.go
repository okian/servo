package load

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeOverlayFile renders replace as the go command's -overlay JSON and
// returns the path it was written to. Marshalled rather than formatted by
// hand because the paths are absolute and would otherwise need escaping.
func writeOverlayFile(t *testing.T, dir string, replace map[string]string) string {
	t.Helper()
	raw, err := json.Marshal(overlayFile{Replace: replace})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "overlay.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestReadOverlayReturnsTheReplacementsContent pins the one thing that
// makes -overlay work at all: the bytes in the map must come from the
// replacement file, not from the path being replaced. Handing go/packages
// the original's content would leave `go list` honouring the overlay while
// servo went on resolving the graph from the real files — the flag would
// appear to work and quietly answer about the wrong program.
func TestReadOverlayReturnsTheReplacementsContent(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir, "real/spec.go", "package spec\n\nconst From = \"disk\"\n")
	mustWriteFile(t, dir, "fake/spec.go", "package spec\n\nconst From = \"overlay\"\n")
	original := filepath.Join(dir, "real", "spec.go")
	path := writeOverlayFile(t, dir, map[string]string{
		original: filepath.Join(dir, "fake", "spec.go"),
	})

	overlay, err := readOverlay(path)
	if err != nil {
		t.Fatalf("readOverlay: %v", err)
	}
	if len(overlay) != 1 {
		t.Fatalf("got %d overlaid files, want 1", len(overlay))
	}
	content, ok := overlay[original]
	if !ok {
		t.Fatalf("overlay is not keyed by the path being replaced: got %v", overlay)
	}
	if !strings.Contains(string(content), `"overlay"`) {
		t.Errorf("overlaid content = %q, want the replacement file's content", content)
	}
}

// TestReadOverlayRejectsWhatItCannotRepresent covers every way an overlay
// file can fail to produce a usable map. Each has to be reported: an
// overlay servo silently ignored would resolve a different graph than the
// one the user asked about, and write it into a committed file.
func TestReadOverlayRejectsWhatItCannotRepresent(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(t *testing.T, dir string) string // returns the -overlay path
		wantErr string
	}{
		{
			name: "the overlay file itself does not exist",
			setup: func(t *testing.T, dir string) string {
				return filepath.Join(dir, "no-such-overlay.json")
			},
			wantErr: "reading -overlay file",
		},
		{
			name: "the overlay file is not JSON",
			setup: func(t *testing.T, dir string) string {
				path := filepath.Join(dir, "overlay.json")
				if err := os.WriteFile(path, []byte("replace: nope\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				return path
			},
			wantErr: "parsing -overlay file",
		},
		{
			// The go command reads an empty replacement as "this file is
			// deleted", which packages.Config.Overlay cannot express: it
			// maps a path to content, and absence means "not overlaid".
			// Refusing beats resolving a graph that still contains the
			// file the user deleted.
			name: "a deletion, which the in-memory form cannot express",
			setup: func(t *testing.T, dir string) string {
				return writeOverlayFile(t, dir, map[string]string{
					filepath.Join(dir, "spec.go"): "",
				})
			},
			wantErr: "which servo cannot represent",
		},
		{
			name: "a replacement file that does not exist",
			setup: func(t *testing.T, dir string) string {
				return writeOverlayFile(t, dir, map[string]string{
					filepath.Join(dir, "spec.go"): filepath.Join(dir, "no-such-replacement.go"),
				})
			},
			wantErr: "reading -overlay replacement for",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := readOverlay(c.setup(t, t.TempDir()))
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("got err=%v, want an error containing %q", err, c.wantErr)
			}
		})
	}
}

// TestLoadResolvesTheSpecFromTheOverlayRatherThanFromDisk is the whole
// point of applying -overlay through packages.Config.Overlay instead of
// passing the flag through to the go command: `go list` would honour it
// while go/packages went on reading syntax from disk, and the spec servo
// parsed would be the one on disk. Here the overlaid spec declares an
// Override the file on disk does not, so seeing it can only mean the
// overlay's content was the content that got type-checked.
func TestLoadResolvesTheSpecFromTheOverlayRatherThanFromDisk(t *testing.T) {
	dir := writeFixtureModule(t, "")
	// The replacement lives outside the module: a second spec file inside
	// it would be a second injector, which is a different diagnostic
	// entirely.
	outside := t.TempDir()
	mustWriteFile(t, outside, "spec.go", `//go:build servoinject

package spec

import (
	"example.com/app/api"
	"example.com/app/memory"
	"example.com/app/store"
	"github.com/okian/servo/v3/servo"
)

func Wire() {
	servo.Build(
		servo.Root[*api.Server](),
		servo.Bind[store.Store, *memory.Mem](),
		servo.Override[store.Store, *memory.Mem](),
	)
}
`)
	overlay := writeOverlayFile(t, outside, map[string]string{
		filepath.Join(dir, "spec", "spec.go"): filepath.Join(outside, "spec.go"),
	})

	loaded, err := Load(Config{Dir: dir, Build: BuildFlags{Overlay: overlay}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	spec, err := FindSpec(loaded)
	if err != nil {
		t.Fatalf("FindSpec: %v", err)
	}
	if len(spec.Overrides) != 1 {
		t.Fatalf("got %d overrides, want the one only the overlaid spec declares — the graph was resolved from the file on disk", len(spec.Overrides))
	}
}

// TestLoadRefusesAnUnusableOverlay confirms readOverlay is wired into Load
// rather than only being callable: an overlay that cannot be read has to
// stop the run, because the alternative is answering about the unoverlaid
// program while the user believes otherwise.
func TestLoadRefusesAnUnusableOverlay(t *testing.T) {
	dir := t.TempDir()
	_, err := Load(Config{Dir: dir, Build: BuildFlags{Overlay: filepath.Join(dir, "absent.json")}})
	if err == nil || !strings.Contains(err.Error(), "reading -overlay file") {
		t.Fatalf("got err=%v, want Load to fail on the unreadable overlay", err)
	}
}
