package load

import (
	"encoding/json"
	"fmt"
	"os"
)

// overlayFile is the go command's -overlay JSON shape: a map from the path
// being replaced to the path holding the replacement content.
type overlayFile struct {
	Replace map[string]string
}

// readOverlay loads a go-command -overlay file into the in-memory form
// go/packages needs.
//
// Passing -overlay straight through to the go command is not enough, and
// fails in the worst way: `go list` honours it, so the package *metadata*
// servo receives reflects the overlay, but go/packages reads syntax and
// types from disk itself. The resolved graph would come from the real
// files while the file list came from the overlaid ones — the flag would
// appear to work, silently resolve the wrong graph, and for an overlay
// that adds a file, fail with "no such file or directory" naming a path
// the user never wrote. Setting packages.Config.Overlay instead makes
// go/packages write its own overlay for the go command *and* parse from
// the same content, so the two can't disagree.
func readOverlay(path string) (map[string][]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("servo: reading -overlay file: %w", err)
	}
	var parsed overlayFile
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("servo: parsing -overlay file %s: %w", path, err)
	}

	overlay := make(map[string][]byte, len(parsed.Replace))
	for original, replacement := range parsed.Replace {
		if replacement == "" {
			// The go command reads an empty value as "this file is
			// deleted", which packages.Config.Overlay has no way to
			// express — it maps a path to content, and absence means
			// "not overlaid", not "removed". Refusing beats resolving a
			// graph that still contains the deleted file.
			return nil, fmt.Errorf("servo: -overlay file %s deletes %s, which servo cannot represent — use an overlay that replaces files rather than removing them", path, original)
		}
		content, err := os.ReadFile(replacement)
		if err != nil {
			return nil, fmt.Errorf("servo: reading -overlay replacement for %s: %w", original, err)
		}
		overlay[original] = content
	}
	return overlay, nil
}
