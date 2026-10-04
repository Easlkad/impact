// Package gittest creates throwaway git repositories for tests.
package gittest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Repo is a git repository in a temporary directory.
type Repo struct {
	Dir    string
	t      testing.TB
	config string // empty global config, isolating tests from the user's
}

// New creates an empty repository, or skips the test when git is missing.
func New(t testing.TB) *Repo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	config := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(config, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	r := &Repo{Dir: t.TempDir(), t: t, config: config}
	r.Git("init", "-q")
	return r
}

// Write creates or overwrites files, given by slash-separated paths.
func (r *Repo) Write(files map[string]string) {
	r.t.Helper()
	for name, content := range files {
		path := filepath.Join(r.Dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
}

// Remove deletes files, given by slash-separated paths.
func (r *Repo) Remove(names ...string) {
	r.t.Helper()
	for _, name := range names {
		if err := os.Remove(filepath.Join(r.Dir, filepath.FromSlash(name))); err != nil {
			r.t.Fatal(err)
		}
	}
}

// Commit commits every change in the working tree and returns the hash.
func (r *Repo) Commit(message string) string {
	r.t.Helper()
	r.Git("add", "-A")
	r.Git("commit", "-q", "--allow-empty", "-m", message)
	return r.Git("rev-parse", "HEAD")
}

// CommitOn creates a commit whose parent is the given commit, with the
// parent's files plus the given ones, and returns its hash. It builds the
// commit with plumbing commands and a separate index, so the working tree,
// the index and HEAD are left as they are: this is how a test creates a
// side branch without checking it out.
func (r *Repo) CommitOn(parent, message string, files map[string]string) string {
	r.t.Helper()
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(r.t.TempDir(), "index")}
	r.run(env, "", "read-tree", parent)
	for name, content := range files {
		blob := r.run(nil, content, "hash-object", "-w", "--stdin")
		r.run(env, "", "update-index", "--add", "--cacheinfo", "100644,"+blob+","+name)
	}
	tree := r.run(env, "", "write-tree")
	return r.run(nil, "", "commit-tree", tree, "-p", parent, "-m", message)
}

// Git runs git in the repository and returns its trimmed output.
func (r *Repo) Git(args ...string) string {
	r.t.Helper()
	return r.run(nil, "", args...)
}

// run runs git with extra environment variables and standard input.
func (r *Repo) run(env []string, stdin string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", r.Dir,
		"-c", "user.name=test", "-c", "user.email=test@example.com",
		"-c", "commit.gpgsign=false", "-c", "core.autocrlf=false",
	}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+r.config, "GIT_CONFIG_NOSYSTEM=1")
	cmd.Env = append(cmd.Env, env...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}
