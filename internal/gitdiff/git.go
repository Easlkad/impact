package gitdiff

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing/fstest"
)

// Repo is a git repository accessed through the git command.
//
// Only commands that read the repository are run: nothing is checked out,
// and the working tree, the index and refs are left untouched.
type Repo struct {
	Root string // absolute path of the top-level directory
}

// Open returns the repository containing the directory dir.
func Open(dir string) (*Repo, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, errors.New("git is not installed or not in PATH")
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}
	out, err := run(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("%s is not inside a git work tree (%v)", dir, err)
	}
	return &Repo{Root: filepath.FromSlash(strings.TrimSpace(string(out)))}, nil
}

// ResolveCommit returns the hash of the commit that ref (a branch, tag,
// hash, or expression such as HEAD~2) points to.
func (r *Repo) ResolveCommit(ref string) (string, error) {
	// Refuse refs that git would read as options.
	if ref == "" || strings.HasPrefix(ref, "-") {
		return "", fmt.Errorf("invalid ref %q", ref)
	}
	out, err := r.git("rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("ref %q does not exist or is not a commit", ref)
	}
	return strings.TrimSpace(string(out)), nil
}

// MergeBase returns the best common ancestor of two commits: the commit a
// branch started from, as used by pull request diffs.
func (r *Repo) MergeBase(a, b string) (string, error) {
	out, err := r.git("merge-base", a, b)
	if err != nil {
		return "", fmt.Errorf("no merge base between %s and %s: %v", a, b, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Diff returns the files that differ between two commits, with renames
// detected and zero lines of context, so that hunks hold only changed lines.
//
// Files that git considers binary, because of their contents or of a -diff
// attribute, come without hunks. Those for which text reports true for the
// old or new path are diffed again as text, so that their changed lines are
// known whatever the repository's attributes say. text may be nil.
func (r *Repo) Diff(base, head string, text func(path string) bool) ([]File, error) {
	files, err := r.diff(base, head, false, nil)
	if err != nil || text == nil {
		return files, err
	}
	wanted := func(f File) bool {
		return f.Binary && (f.OldPath != "" && text(f.OldPath) || f.NewPath != "" && text(f.NewPath))
	}
	var paths []string
	for _, f := range files {
		if wanted(f) {
			for _, p := range []string{f.OldPath, f.NewPath} {
				if p != "" {
					paths = append(paths, p)
				}
			}
		}
	}
	if len(paths) == 0 {
		return files, nil
	}
	// Both paths of a rename are included, so the second diff pairs the
	// files as the first one did.
	textFiles, err := r.diff(base, head, true, paths)
	if err != nil {
		return nil, err
	}
	// Each text diff takes the place of the binary one for the same paths,
	// keeping git's order. Should git pair the files differently, the
	// leftovers are appended rather than lost.
	type key struct{ old, new string }
	byPaths := make(map[key]File, len(textFiles))
	for _, f := range textFiles {
		byPaths[key{f.OldPath, f.NewPath}] = f
	}
	out := make([]File, 0, len(files)+len(textFiles))
	for _, f := range files {
		if !wanted(f) {
			out = append(out, f)
			continue
		}
		k := key{f.OldPath, f.NewPath}
		if tf, ok := byPaths[k]; ok {
			out = append(out, tf)
			delete(byPaths, k)
		}
	}
	for _, f := range textFiles {
		if _, ok := byPaths[key{f.OldPath, f.NewPath}]; ok {
			out = append(out, f)
		}
	}
	return out, nil
}

// diff runs git diff between two commits, limited to the given paths when
// there are any. With text set, every file is diffed as text.
func (r *Repo) diff(base, head string, text bool, paths []string) ([]File, error) {
	args := []string{"diff",
		"--no-color", "--no-ext-diff", "--no-textconv", // plain output, whatever the user's config
		"--src-prefix=a/", "--dst-prefix=b/",
		"--find-renames", "--unified=0",
	}
	if text {
		args = append(args, "--text")
	}
	args = append(args, base, head, "--")
	for _, p := range paths {
		args = append(args, ":(literal)"+p) // no glob matching in file names
	}
	out, err := r.git(args...)
	if err != nil {
		return nil, err
	}
	return Parse(bytes.NewReader(out))
}

// Snapshot returns the files of a commit for which keep reports true, as an
// in-memory file system with paths relative to the repository root. The
// contents are read from git's object database, so the working tree does
// not need to match the commit.
func (r *Repo) Snapshot(commit string, keep func(path string) bool) (fs.FS, error) {
	out, err := r.git("ls-tree", "-r", "-z", "--full-tree", commit)
	if err != nil {
		return nil, err
	}
	var paths, objects []string
	for _, entry := range strings.Split(string(out), "\x00") {
		// "<mode> <type> <object>\t<path>"
		meta, path, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 || fields[1] != "blob" || fields[0] == "120000" || !keep(path) {
			continue // not a regular file (directory, submodule, symlink), or not wanted
		}
		paths = append(paths, path)
		objects = append(objects, fields[2])
	}

	contents, err := r.readBlobs(objects)
	if err != nil {
		return nil, err
	}
	// fstest.MapFS is a complete in-memory fs.FS; despite its package name it
	// does not depend on the testing package.
	fsys := make(fstest.MapFS, len(paths))
	for i, p := range paths {
		fsys[p] = &fstest.MapFile{Data: contents[i]}
	}
	return fsys, nil
}

// readBlobs returns the contents of the given blobs, read through a single
// "git cat-file --batch" process.
func (r *Repo) readBlobs(objects []string) ([][]byte, error) {
	if len(objects) == 0 {
		return nil, nil
	}
	cmd := command(r.Root, "cat-file", "--batch")
	cmd.Stdin = strings.NewReader(strings.Join(objects, "\n") + "\n")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	contents, readErr := readBatch(bufio.NewReader(stdout), len(objects))
	if readErr != nil {
		cmd.Process.Kill()
	}
	if err := cmd.Wait(); err != nil && readErr == nil {
		readErr = fmt.Errorf("git cat-file: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return contents, readErr
}

// readBatch reads n objects in the output format of "git cat-file --batch":
// a "<object> <type> <size>" line, the content, and a newline.
func readBatch(r *bufio.Reader, n int) ([][]byte, error) {
	contents := make([][]byte, n)
	for i := range contents {
		header, err := r.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("git cat-file: %v", err)
		}
		fields := strings.Fields(header)
		if len(fields) != 3 {
			return nil, fmt.Errorf("git cat-file: %s", strings.TrimSpace(header))
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil {
			return nil, fmt.Errorf("git cat-file: bad header %q", header)
		}
		data := make([]byte, size+1)
		if _, err := io.ReadFull(r, data); err != nil {
			return nil, fmt.Errorf("git cat-file: %v", err)
		}
		contents[i] = data[:size]
	}
	return contents, nil
}

func (r *Repo) git(args ...string) ([]byte, error) {
	return run(r.Root, args...)
}

// run runs git in dir and returns its standard output. On failure, the error
// carries git's own message.
func run(dir string, args ...string) ([]byte, error) {
	cmd := command(dir, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("git %s: %s", args[0], msg)
	}
	return out, nil
}

func command(dir string, args ...string) *exec.Cmd {
	// core.quotePath=false keeps non-ASCII paths readable instead of
	// octal-escaped.
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "core.quotePath=false"}, args...)...)
	// Optional locks would let read-only commands refresh the index.
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	return cmd
}
