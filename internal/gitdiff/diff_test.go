package gitdiff

import (
	"reflect"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	patch := strings.Join([]string{
		// Modified file with three hunks: a replacement, a pure insertion,
		// and a pure deletion of lines that look like file headers.
		"diff --git a/user/user.go b/user/user.go",
		"index 1111111..2222222 100644",
		"--- a/user/user.go",
		"+++ b/user/user.go",
		"@@ -4 +4 @@ func CreateUser() {",
		"-\tvalidate()",
		"+\tValidateUser()",
		"@@ -10,0 +11,3 @@ func SaveUser() {}",
		"+",
		"+func Extra() {",
		"+}",
		"@@ -20,2 +22,0 @@",
		"--- removed line that looks like a header",
		"-+++ another one",
		// Added file without a trailing newline.
		"diff --git a/new.go b/new.go",
		"new file mode 100644",
		"index 0000000..3333333",
		"--- /dev/null",
		"+++ b/new.go",
		"@@ -0,0 +1,2 @@",
		"+package app",
		"+func New() {}",
		`\ No newline at end of file`,
		// Deleted file.
		"diff --git a/old.go b/old.go",
		"deleted file mode 100644",
		"index 4444444..0000000",
		"--- a/old.go",
		"+++ /dev/null",
		"@@ -1,2 +0,0 @@",
		"-package app",
		"-func Old() {}",
		// Renamed and modified.
		"diff --git a/a/moved.go b/b/moved.go",
		"similarity index 90%",
		"rename from a/moved.go",
		"rename to b/moved.go",
		"index 5555555..6666666 100644",
		"--- a/a/moved.go",
		"+++ b/b/moved.go",
		"@@ -3 +3 @@",
		"-x",
		"+y",
		// Pure rename: no hunks.
		"diff --git a/same.go b/renamed.go",
		"similarity index 100%",
		"rename from same.go",
		"rename to renamed.go",
		// Quoted path (git escapes the tab) and a path with a space (git
		// appends a tab after the name).
		`diff --git "a/odd\tname.go" "b/odd\tname.go"`,
		`--- "a/odd\tname.go"`,
		`+++ "b/odd\tname.go"`,
		"@@ -1 +1 @@",
		"-a",
		"+b",
		"diff --git a/my file.go b/my file.go",
		"--- a/my file.go\t",
		"+++ b/my file.go\t",
		"@@ -1 +1 @@",
		"-a",
		"+b",
		// Empty added file and mode change: paths only in the git header.
		"diff --git a/empty.go b/empty.go",
		"new file mode 100644",
		"index 0000000..e69de29",
		"diff --git a/script.go b/script.go",
		"old mode 100644",
		"new mode 100755",
		"",
	}, "\n")

	got, err := Parse(strings.NewReader(patch))
	if err != nil {
		t.Fatal(err)
	}
	want := []File{
		{OldPath: "user/user.go", NewPath: "user/user.go", Status: Modified, Hunks: []Hunk{
			{Old: Range{4, 1}, New: Range{4, 1}, Removed: []string{"\tvalidate()"}, Added: []string{"\tValidateUser()"}},
			{Old: Range{10, 0}, New: Range{11, 3}, Added: []string{"", "func Extra() {", "}"}},
			{Old: Range{20, 2}, New: Range{22, 0}, Removed: []string{"-- removed line that looks like a header", "+++ another one"}},
		}},
		{NewPath: "new.go", Status: Added, Hunks: []Hunk{
			{Old: Range{0, 0}, New: Range{1, 2}, Added: []string{"package app", "func New() {}"}},
		}},
		{OldPath: "old.go", Status: Deleted, Hunks: []Hunk{
			{Old: Range{1, 2}, New: Range{0, 0}, Removed: []string{"package app", "func Old() {}"}},
		}},
		{OldPath: "a/moved.go", NewPath: "b/moved.go", Status: Renamed, Hunks: []Hunk{
			{Old: Range{3, 1}, New: Range{3, 1}, Removed: []string{"x"}, Added: []string{"y"}},
		}},
		{OldPath: "same.go", NewPath: "renamed.go", Status: Renamed},
		{OldPath: "odd\tname.go", NewPath: "odd\tname.go", Status: Modified, Hunks: []Hunk{
			{Old: Range{1, 1}, New: Range{1, 1}, Removed: []string{"a"}, Added: []string{"b"}},
		}},
		{OldPath: "my file.go", NewPath: "my file.go", Status: Modified, Hunks: []Hunk{
			{Old: Range{1, 1}, New: Range{1, 1}, Removed: []string{"a"}, Added: []string{"b"}},
		}},
		{NewPath: "empty.go", Status: Added},
		{OldPath: "script.go", NewPath: "script.go", Status: Modified},
	}

	if len(got) != len(want) {
		t.Fatalf("got %d files, want %d:\n%+v", len(got), len(want), got)
	}
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("file %d:\n got: %+v\nwant: %+v", i, got[i], want[i])
		}
	}
}

func TestParseEmpty(t *testing.T) {
	files, err := Parse(strings.NewReader(""))
	if err != nil || len(files) != 0 {
		t.Errorf("Parse(\"\") = %v, %v; want no files", files, err)
	}
}

func TestParseErrors(t *testing.T) {
	tests := map[string]string{
		"malformed header": "diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -a +1 @@\n",
		"truncated hunk":   "diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1,2 +1 @@\n-a\n",
		"bad hunk line":    "diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n?\n",
	}
	for name, patch := range tests {
		if _, err := Parse(strings.NewReader(patch)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestRange(t *testing.T) {
	r := Range{Start: 10, Count: 3}
	if r.End() != 12 || r.Empty() {
		t.Errorf("Range{10, 3}: End() = %d, Empty() = %v", r.End(), r.Empty())
	}
	empty := Range{Start: 10, Count: 0}
	if empty.End() != 9 || !empty.Empty() {
		t.Errorf("Range{10, 0}: End() = %d, Empty() = %v", empty.End(), empty.Empty())
	}
}

func TestFilePath(t *testing.T) {
	if p := (File{OldPath: "a.go", NewPath: "b.go"}).Path(); p != "b.go" {
		t.Errorf("renamed file Path() = %q", p)
	}
	if p := (File{OldPath: "a.go"}).Path(); p != "a.go" {
		t.Errorf("deleted file Path() = %q", p)
	}
}
