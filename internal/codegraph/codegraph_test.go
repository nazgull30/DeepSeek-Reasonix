package codegraph

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeExec writes an executable file at path with the given content and +x.
func writeExec(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestResolveOverride(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("override path test uses a unix +x bit")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "codegraph")
	writeExec(t, bin, "#!/bin/sh\nexit 0\n")

	got, ok := Resolve(bin)
	if !ok || got != bin {
		t.Fatalf("Resolve(%q) = %q, %v; want %q, true", bin, got, ok, bin)
	}
}

func TestResolveOverrideMissingFallsThrough(t *testing.T) {
	// A non-existent override must not resolve to itself; with no bundle/PATH
	// match either, ok is false (a real codegraph on PATH could make this true,
	// so only assert the override itself is not returned).
	missing := filepath.Join(t.TempDir(), "nope")
	if got, _ := Resolve(missing); got == missing {
		t.Fatalf("Resolve returned the missing override path %q", got)
	}
}

func TestExpandTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	if got := expand("~/foo/bar"); got != filepath.Join(home, "foo", "bar") {
		t.Fatalf("expand(~/foo/bar) = %q", got)
	}
}

func TestEnsureInitSkipsWhenPresent(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".codegraph"), 0o755); err != nil {
		t.Fatal(err)
	}
	// bin points at nothing runnable; EnsureInit must short-circuit before exec.
	if err := EnsureInit(context.Background(), filepath.Join(root, "no-such-bin"), root); err != nil {
		t.Fatalf("EnsureInit with existing .codegraph should be a no-op, got %v", err)
	}
}

func TestEnsureInitRunsWhenAbsent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake launcher is a POSIX-sh script")
	}
	root := t.TempDir()
	// A fake codegraph that creates .codegraph in its working directory — EnsureInit
	// runs it with cmd.Dir = root, so this is independent of the exact arguments.
	bin := filepath.Join(t.TempDir(), "fakecg")
	writeExec(t, bin, "#!/bin/sh\nmkdir -p .codegraph\n")

	if err := EnsureInit(context.Background(), bin, root); err != nil {
		t.Fatalf("EnsureInit = %v", err)
	}
	if fi, err := os.Stat(filepath.Join(root, ".codegraph")); err != nil || !fi.IsDir() {
		t.Fatalf(".codegraph not created: err=%v", err)
	}
}

func TestEnsureInitPropagatesFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake launcher is a POSIX-sh script")
	}
	root := t.TempDir()
	bin := filepath.Join(t.TempDir(), "failcg")
	writeExec(t, bin, "#!/bin/sh\necho boom 1>&2\nexit 3\n")

	if err := EnsureInit(context.Background(), bin, root); err == nil {
		t.Fatal("EnsureInit should return the init failure")
	}
}

func TestInitialized(t *testing.T) {
	if Initialized("") {
		t.Fatal("Initialized(\"\") should be false")
	}
	// Non-existent dir.
	unknown := filepath.Join(t.TempDir(), "nope")
	if Initialized(unknown) {
		t.Fatal("Initialized(non-existent) should be false")
	}
	// .codegraph is a file, not a dir.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".codegraph"), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	if Initialized(root) {
		t.Fatal("Initialized(file) should be false")
	}
	// .codegraph is a directory.
	root2 := t.TempDir()
	if err := os.Mkdir(filepath.Join(root2, ".codegraph"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !Initialized(root2) {
		t.Fatal("Initialized(existing dir) should be true")
	}
}

func TestEnsureInitEmptyRoot(t *testing.T) {
	// Root "" is a no-op even with a nil binary.
	if err := EnsureInit(context.Background(), "/nonexistent/bin", ""); err != nil {
		t.Fatalf("EnsureInit with empty root = %v, want nil", err)
	}
}

func TestEnsureInitContextCancel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake launcher is a POSIX-sh script")
	}
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	bin := filepath.Join(t.TempDir(), "slowcg")
	writeExec(t, bin, "#!/bin/sh\nsleep 10\nmkdir -p .codegraph\n")

	cancel() // cancel immediately
	if err := EnsureInit(ctx, bin, root); err == nil {
		t.Fatal("EnsureInit with cancelled context should fail")
	}
}

func TestSyncEmptyRoot(t *testing.T) {
	out, err := Sync("/nonexistent/bin", "")
	if err != nil || out != "" {
		t.Fatalf("Sync with empty root = %q, %v; want empty", out, err)
	}
}

func TestSyncSkipsWhenNotInitialized(t *testing.T) {
	root := t.TempDir()
	// No .codegraph/ exists — Sync must be a no-op even if bin is bogus.
	out, err := Sync("/nonexistent/bin", root)
	if err != nil || out != "" {
		t.Fatalf("Sync without init = %q, %v; want empty", out, err)
	}
}

func TestSyncRunsWhenInitialized(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake launcher is a POSIX-sh script")
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".codegraph"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Fake sync that writes a sentinel and outputs a report.
	bin := filepath.Join(t.TempDir(), "synccg")
	writeExec(t, bin, "#!/bin/sh\necho synced > \"$2/.sync_ok\"\necho \"filesAdded: 3, filesModified: 1\"\n")

	out, err := Sync(bin, root)
	if err != nil {
		t.Fatalf("Sync = %v, out=%q", err, out)
	}
	if _, err := os.Stat(filepath.Join(root, ".sync_ok")); err != nil {
		t.Fatal("Sync did not run: sentinel file missing")
	}
	if !strings.Contains(out, "filesAdded: 3") {
		t.Fatalf("Sync output = %q, want sync report", out)
	}
}

func TestSyncPropagatesFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake launcher is a POSIX-sh script")
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".codegraph"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "failcg")
	writeExec(t, bin, "#!/bin/sh\necho \"some error\" 1>&2\nexit 1\n")

	out, err := Sync(bin, root)
	if err == nil {
		t.Fatal("Sync should propagate command failure")
	}
	if !strings.Contains(out, "some error") {
		t.Fatalf("Sync output = %q, want 'some error'", out)
	}
}

func TestSyncInitializedRace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake launcher is a POSIX-sh script")
	}
	// Simulate the scenario: partial init created .codegraph/ but init did not
	// complete. Sync should still be attempted (it is the codegraph binary's job
	// to know what it needs), but a fake binary that checks and rejects a
	// marker file confirms the guard in Sync itself does not over-skip.
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".codegraph"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "partialcg")
	writeExec(t, bin, "#!/bin/sh\nif [ ! -f \"$2/.codegraph/cg.db\" ]; then echo \"not fully initialized\" 1>&2; exit 1; fi\n")

	out, err := Sync(bin, root)
	if err == nil {
		t.Fatal("Sync should fail because cg.db is missing from the partial .codegraph/")
	}
	if !strings.Contains(out, "not fully initialized") {
		t.Fatalf("Sync output = %q, want 'not fully initialized'", out)
	}
}

func TestIndexableRootRejectsFilesystemRoots(t *testing.T) {
	if got := IndexableRoot(t.TempDir()); !got {
		t.Fatal("a real project dir must be indexable")
	}
	for _, root := range []string{"", "   "} {
		if IndexableRoot(root) {
			t.Fatalf("IndexableRoot(%q) = true; want false", root)
		}
	}
	var roots []string
	if runtime.GOOS == "windows" {
		roots = []string{`C:\`, `c:\`, `\\server\share`}
	} else {
		roots = []string{"/"}
	}
	for _, root := range roots {
		if IndexableRoot(root) {
			t.Fatalf("IndexableRoot(%q) = true; want false (filesystem root)", root)
		}
	}
}
