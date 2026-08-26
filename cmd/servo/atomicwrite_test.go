package main

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestWriteFileAtomicWritesContentAndPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")

	if err := writeFileAtomic(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("content = %q, want %q", got, "hello")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, want 0644", info.Mode().Perm())
	}
}

func TestWriteFileAtomicLeavesNoTempFileBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")

	if err := writeFileAtomic(path, []byte("v1"), 0o644); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}
	if err := writeFileAtomic(path, []byte("v2"), 0o644); err != nil {
		t.Fatalf("writeFileAtomic (overwrite): %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "out.txt" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("directory contains %v, want exactly [out.txt] (no leftover temp files)", names)
	}
}

// TestWriteFileAtomicIsAtomicUnderConcurrentWriters covers the actual
// motivation: two CI jobs, or a save-triggered watcher racing a manual
// `servo generate`, writing the same output path at once. Every writer
// must observe a complete file — the final content must be entirely one
// writer's payload, never a byte-level interleaving of two. This is a
// best-effort check (a fast local disk rarely tears a single write() this
// small regardless of implementation) — TestWriteFileAtomicSurvivesAReadOnlyTarget
// below is the deterministic proof that this really goes through a
// rename, not an in-place truncate.
func TestWriteFileAtomicIsAtomicUnderConcurrentWriters(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")

	const writers = 20
	const size = 64 * 1024
	payloads := make([][]byte, writers)
	for i := range payloads {
		payloads[i] = bytes.Repeat([]byte{byte('A' + i)}, size)
	}

	var wg sync.WaitGroup
	for _, p := range payloads {
		wg.Add(1)
		go func(p []byte) {
			defer wg.Done()
			if err := writeFileAtomic(path, p, 0o644); err != nil {
				t.Errorf("writeFileAtomic: %v", err)
			}
		}(p)
	}
	wg.Wait()

	final, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(final) != size {
		t.Fatalf("final file is %d bytes, want %d (a torn write would likely produce a different size)", len(final), size)
	}
	want := final[0]
	for i, b := range final {
		if b != want {
			t.Fatalf("final file is not a single writer's payload: byte %d is %q, first byte was %q — content from two writers was interleaved", i, b, want)
		}
	}
}

// TestWriteFileAtomicSurvivesAReadOnlyTarget deterministically proves
// writeFileAtomic replaces the target via rename rather than opening and
// truncating it in place: renaming a new file over an existing path only
// requires write permission on the *directory*, never on the target file
// itself, whereas an in-place os.WriteFile against a read-only existing
// file fails with "permission denied". This stands in for the property
// the concurrency test above can't reliably force on a fast local disk:
// a writer never depends on being able to modify the previous version of
// the file it's replacing.
func TestWriteFileAtomicSurvivesAReadOnlyTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}

	if err := writeFileAtomic(path, []byte("new"), 0o644); err != nil {
		t.Fatalf("writeFileAtomic against a read-only target: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "new" {
		t.Errorf("content = %q, want %q", got, "new")
	}
}
