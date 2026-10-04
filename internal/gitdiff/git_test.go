package gitdiff

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Easlkad/impact/internal/gittest"
)

func TestRepo(t *testing.T) {
	g := gittest.New(t)
	g.Write(map[string]string{
		"a.go":      "package a\n\nfunc A() {}\n",
		"dir/b.go":  "package dir\n\nfunc B() {}\n",
		"notes.txt": "notes\n",
	})
	base := g.Commit("base")
	g.Remove("dir/b.go")
	g.Write(map[string]string{
		"dir/c.go": "package dir\n\nfunc B() {}\n",
		"a.go":     "package a\n\nfunc A() { B() }\n",
	})
	head := g.Commit("head")
	// An uncommitted change must not influence anything.
	g.Write(map[string]string{"a.go": "package a // uncommitted\n"})

	repo, err := Open(filepath.Join(g.Dir, "dir"))
	if err != nil {
		t.Fatal(err)
	}
	if !sameDir(t, repo.Root, g.Dir) {
		t.Errorf("Root = %s, want %s", repo.Root, g.Dir)
	}

	t.Run("ResolveCommit", func(t *testing.T) {
		for ref, want := range map[string]string{"HEAD": head, "HEAD~1": base, base[:10]: base} {
			if got, err := repo.ResolveCommit(ref); err != nil || got != want {
				t.Errorf("ResolveCommit(%q) = %q, %v; want %q", ref, got, err, want)
			}
		}
		for _, ref := range []string{"no-such-branch", "", "--all", "HEAD~5"} {
			if _, err := repo.ResolveCommit(ref); err == nil {
				t.Errorf("ResolveCommit(%q) succeeded", ref)
			}
		}
	})

	t.Run("Diff", func(t *testing.T) {
		files, err := repo.Diff(base, head)
		if err != nil {
			t.Fatal(err)
		}
		want := []File{
			{OldPath: "a.go", NewPath: "a.go", Status: Modified, Hunks: []Hunk{
				{Old: Range{3, 1}, New: Range{3, 1}, Removed: []string{"func A() {}"}, Added: []string{"func A() { B() }"}},
			}},
			{OldPath: "dir/b.go", NewPath: "dir/c.go", Status: Renamed},
		}
		if !reflect.DeepEqual(files, want) {
			t.Errorf("Diff:\n got: %+v\nwant: %+v", files, want)
		}
	})

	t.Run("Snapshot", func(t *testing.T) {
		fsys, err := repo.Snapshot(base, func(path string) bool { return strings.HasSuffix(path, ".go") })
		if err != nil {
			t.Fatal(err)
		}
		var paths []string
		fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
			if !d.IsDir() {
				paths = append(paths, path)
			}
			return err
		})
		if want := []string{"a.go", "dir/b.go"}; !reflect.DeepEqual(paths, want) {
			t.Errorf("files = %q, want %q", paths, want)
		}
		data, err := fs.ReadFile(fsys, "a.go")
		if err != nil || string(data) != "package a\n\nfunc A() {}\n" {
			t.Errorf("a.go at base = %q, %v", data, err)
		}
	})

	// Nothing above touched the working tree or moved HEAD.
	data, err := os.ReadFile(filepath.Join(g.Dir, "a.go"))
	if err != nil || string(data) != "package a // uncommitted\n" {
		t.Errorf("working tree a.go = %q, %v", data, err)
	}
	if got := g.Git("rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved to %s", got)
	}
	if got := g.Git("status", "--porcelain"); got != "M a.go" {
		t.Errorf("git status = %q, want only the uncommitted a.go", got)
	}
}

func TestOpenErrors(t *testing.T) {
	gittest.New(t) // skips without git
	if _, err := Open(t.TempDir()); err == nil {
		t.Error("Open of a directory outside git succeeded")
	}
	if _, err := Open(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("Open of a missing directory succeeded")
	}
}

func sameDir(t *testing.T, a, b string) bool {
	t.Helper()
	sa, err := os.Stat(a)
	if err != nil {
		t.Fatal(err)
	}
	sb, err := os.Stat(b)
	if err != nil {
		t.Fatal(err)
	}
	return os.SameFile(sa, sb)
}
