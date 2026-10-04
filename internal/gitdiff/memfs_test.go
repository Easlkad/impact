package gitdiff

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"testing"
	"testing/fstest"
)

func TestMemFS(t *testing.T) {
	paths := []string{"go.mod", "a.go", "dir/b.go", "dir/sub/c.go", "dir/sub/empty.txt", "other/d.go"}
	contents := [][]byte{[]byte("module x\n"), []byte("package a\n"), []byte("package dir\n"), []byte("package sub\n"), nil, []byte("package other\n")}
	fsys := newMemFS(paths, contents)

	// TestFS checks the whole fs.FS contract, including ReadDirFS,
	// ReadFileFS and StatFS, against the expected files.
	if err := fstest.TestFS(fsys, paths...); err != nil {
		t.Fatal(err)
	}

	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, fmt.Sprintf("%s:%v", e.Name(), e.IsDir()))
	}
	if got, want := fmt.Sprint(names), "[a.go:false dir:true go.mod:false other:true]"; got != want {
		t.Errorf("root entries = %s, want %s", got, want)
	}

	for _, name := range []string{"missing.go", "dir/go.mod", "dir/missing/x.go"} {
		if _, err := fs.ReadFile(fsys, name); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("ReadFile(%s) error = %v, want not exist", name, err)
		}
		if _, err := fsys.Open(name); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Open(%s) error = %v, want not exist", name, err)
		}
	}
	if _, err := fs.ReadDir(fsys, "a.go"); err == nil {
		t.Error("ReadDir of a file succeeded")
	}
	if _, err := fs.ReadFile(fsys, "dir"); err == nil {
		t.Error("ReadFile of a directory succeeded")
	}
	if _, err := fsys.Open("../a.go"); !errors.Is(err, fs.ErrInvalid) {
		t.Errorf("Open(../a.go) error = %v, want invalid", err)
	}
}

func TestMemFSEmpty(t *testing.T) {
	if err := fstest.TestFS(newMemFS(nil, nil)); err != nil {
		t.Fatal(err)
	}
}

// BenchmarkMemFSWalk walks a tree as the analyzer does, looking for a
// go.mod in every directory. Its time per file stays flat as the tree
// grows, where fstest.MapFS grows with the number of directories.
func BenchmarkMemFSWalk(b *testing.B) {
	for _, dirs := range []int{100, 1000} {
		var paths []string
		var contents [][]byte
		for d := 0; d < dirs; d++ {
			for f := 0; f < 5; f++ {
				paths = append(paths, fmt.Sprintf("pkg%d/file%d.go", d, f))
				contents = append(contents, []byte("package p\n"))
			}
		}
		fsys := newMemFS(paths, contents)
		b.Run(fmt.Sprintf("dirs=%d", dirs), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
					if d.IsDir() {
						fs.ReadFile(fsys, path.Join(name, "go.mod"))
					}
					return err
				})
			}
		})
	}
}
