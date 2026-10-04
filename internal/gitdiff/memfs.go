package gitdiff

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"path"
	"slices"
	"strings"
	"time"
)

// memFS is a read-only in-memory file system holding the files of a
// snapshot.
//
// Unlike fstest.MapFS, which goes through every file to list a directory or
// to report that a file is missing, it indexes directories: walking a tree
// of D directories and F files, looking for a go.mod in each directory,
// takes time proportional to D + F rather than D × F.
type memFS struct {
	files map[string][]byte        // regular files, by path
	dirs  map[string][]fs.DirEntry // directories, by path ("." for the root), entries sorted by name
}

// newMemFS returns a file system with the given files, contents[i] being
// the contents of paths[i]. Paths are slash-separated, relative and valid
// for fs.ValidPath, as git prints them; directories are implied by them.
func newMemFS(paths []string, contents [][]byte) *memFS {
	m := &memFS{
		files: make(map[string][]byte, len(paths)),
		dirs:  map[string][]fs.DirEntry{".": nil},
	}
	for i, p := range paths {
		m.files[p] = contents[i]
		m.addEntry(p, fileInfo{name: path.Base(p), size: len(contents[i])})
	}
	for _, entries := range m.dirs {
		slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	}
	return m
}

// addEntry adds the entry for name to its parent directory, creating the
// parent, and its own parents, when they do not exist yet.
func (m *memFS) addEntry(name string, info fileInfo) {
	dir := path.Dir(name)
	_, exists := m.dirs[dir]
	m.dirs[dir] = append(m.dirs[dir], fs.FileInfoToDirEntry(info))
	if !exists {
		m.addEntry(dir, fileInfo{name: path.Base(dir), dir: true})
	}
}

var errIsDir = errors.New("is a directory")

func (m *memFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	if data, ok := m.files[name]; ok {
		return &memFile{Reader: bytes.NewReader(data), info: fileInfo{name: path.Base(name), size: len(data)}}, nil
	}
	if entries, ok := m.dirs[name]; ok {
		return &memDir{path: name, entries: entries}, nil
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

// ReadFile implements fs.ReadFileFS, so that fs.ReadFile needs no Open.
func (m *memFS) ReadFile(name string) ([]byte, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "read", Path: name, Err: fs.ErrInvalid}
	}
	if data, ok := m.files[name]; ok {
		return slices.Clone(data), nil // the caller may modify it
	}
	if _, ok := m.dirs[name]; ok {
		return nil, &fs.PathError{Op: "read", Path: name, Err: errIsDir}
	}
	return nil, &fs.PathError{Op: "read", Path: name, Err: fs.ErrNotExist}
}

// ReadDir implements fs.ReadDirFS, so that fs.WalkDir needs no Open.
func (m *memFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrInvalid}
	}
	entries, ok := m.dirs[name]
	if !ok {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}
	return slices.Clone(entries), nil
}

// Stat implements fs.StatFS.
func (m *memFS) Stat(name string) (fs.FileInfo, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrInvalid}
	}
	if data, ok := m.files[name]; ok {
		return fileInfo{name: path.Base(name), size: len(data)}, nil
	}
	if _, ok := m.dirs[name]; ok {
		return fileInfo{name: path.Base(name), dir: true}, nil
	}
	return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
}

// fileInfo describes a file or directory of a memFS.
type fileInfo struct {
	name string
	size int
	dir  bool
}

func (i fileInfo) Name() string       { return i.name }
func (i fileInfo) Size() int64        { return int64(i.size) }
func (i fileInfo) ModTime() time.Time { return time.Time{} }
func (i fileInfo) IsDir() bool        { return i.dir }
func (i fileInfo) Sys() any           { return nil }

func (i fileInfo) Mode() fs.FileMode {
	if i.dir {
		return fs.ModeDir | 0o555
	}
	return 0o444
}

// memFile is an open regular file.
type memFile struct {
	*bytes.Reader
	info fileInfo
}

func (f *memFile) Stat() (fs.FileInfo, error) { return f.info, nil }
func (f *memFile) Close() error               { return nil }

// memDir is an open directory.
type memDir struct {
	path    string
	entries []fs.DirEntry
	offset  int // entries already returned by ReadDir
}

func (d *memDir) Stat() (fs.FileInfo, error) {
	return fileInfo{name: path.Base(d.path), dir: true}, nil
}

func (d *memDir) Close() error { return nil }

func (d *memDir) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: d.path, Err: errIsDir}
}

// ReadDir follows fs.ReadDirFile: with n > 0 it returns at most n entries
// and io.EOF at the end; otherwise it returns all remaining entries.
func (d *memDir) ReadDir(n int) ([]fs.DirEntry, error) {
	rest := d.entries[d.offset:]
	if n <= 0 {
		d.offset = len(d.entries)
		return slices.Clone(rest), nil
	}
	if len(rest) == 0 {
		return nil, io.EOF
	}
	n = min(n, len(rest))
	d.offset += n
	return slices.Clone(rest[:n]), nil
}
