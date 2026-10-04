// Package gitdiff reads changes from a git repository: which files differ
// between two commits, which lines changed, and what the files contained at a
// given commit.
//
// Git is used through the git command (see git.go), and only read-only
// commands are run. The diff format itself is parsed by Parse, which does not
// depend on git being installed.
package gitdiff

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Status says how a file changed between two commits.
type Status string

const (
	Added    Status = "added"
	Modified Status = "modified"
	Deleted  Status = "deleted"
	Renamed  Status = "renamed"
	Copied   Status = "copied"
)

// File is one file of a diff.
type File struct {
	OldPath string // path in the base commit; empty for added files
	NewPath string // path in the head commit; empty for deleted files
	Status  Status
	Hunks   []Hunk // changed line ranges; none for pure renames, mode or binary changes
}

// Path returns the path of the file in the head commit, or in the base
// commit for a deleted file.
func (f File) Path() string {
	if f.NewPath != "" {
		return f.NewPath
	}
	return f.OldPath
}

// Hunk is a block of changed lines: Old lines of the base file are replaced
// by New lines of the head file.
type Hunk struct {
	Old     Range
	New     Range
	Removed []string // text of the Old lines, without the "-" marker
	Added   []string // text of the New lines, without the "+" marker
}

// Range is a span of lines, as written in a hunk header "@@ -12,3 +12,4 @@".
// A range with Count 0 holds no lines: it marks a position just after line
// Start, where lines were inserted (Old) or removed (New).
type Range struct {
	Start int
	Count int
}

// End returns the last line of the range. It is Start-1 for an empty range.
func (r Range) End() int { return r.Start + r.Count - 1 }

// Empty reports whether the range holds no lines.
func (r Range) Empty() bool { return r.Count == 0 }

// Parse reads a diff in git's unified format, as printed by "git diff".
// Context lines are not kept, so the diff is best produced with --unified=0:
// hunk ranges then cover exactly the changed lines.
func Parse(r io.Reader) ([]File, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 256*1024*1024) // allow very long lines
	var (
		files            []File
		cur              *File
		oldLeft, newLeft int // lines of the current hunk still to be read
		lineNo           int
	)
	flush := func() {
		if cur != nil {
			files = append(files, cur.finish())
		}
	}

	for sc.Scan() {
		line := sc.Text()
		lineNo++

		// Inside a hunk, lines are consumed by count rather than by prefix:
		// a removed line "-- x" reads "--- x", like a file header.
		if oldLeft > 0 || newLeft > 0 {
			h := &cur.Hunks[len(cur.Hunks)-1]
			switch {
			case strings.HasPrefix(line, "-"):
				h.Removed = append(h.Removed, line[1:])
				oldLeft--
			case strings.HasPrefix(line, "+"):
				h.Added = append(h.Added, line[1:])
				newLeft--
			case strings.HasPrefix(line, " "):
				oldLeft--
				newLeft--
			case strings.HasPrefix(line, `\`): // "\ No newline at end of file"
			default:
				return nil, fmt.Errorf("diff line %d: unexpected line inside hunk: %q", lineNo, line)
			}
			continue
		}

		if rest, ok := strings.CutPrefix(line, "diff --git "); ok {
			flush()
			cur = &File{}
			cur.OldPath, cur.NewPath = parseGitHeader(rest)
			continue
		}
		if cur == nil {
			continue
		}
		switch {
		case strings.HasPrefix(line, "new file mode "):
			cur.Status = Added
		case strings.HasPrefix(line, "deleted file mode "):
			cur.Status = Deleted
		case strings.HasPrefix(line, "rename from "):
			cur.OldPath, cur.Status = parsePath(line[len("rename from "):], ""), Renamed
		case strings.HasPrefix(line, "rename to "):
			cur.NewPath = parsePath(line[len("rename to "):], "")
		case strings.HasPrefix(line, "copy from "):
			cur.OldPath, cur.Status = parsePath(line[len("copy from "):], ""), Copied
		case strings.HasPrefix(line, "copy to "):
			cur.NewPath = parsePath(line[len("copy to "):], "")
		case strings.HasPrefix(line, "--- "):
			cur.OldPath = parsePath(line[len("--- "):], "a/")
		case strings.HasPrefix(line, "+++ "):
			cur.NewPath = parsePath(line[len("+++ "):], "b/")
		case strings.HasPrefix(line, "@@ "):
			h, err := parseHunkHeader(line)
			if err != nil {
				return nil, fmt.Errorf("diff line %d: %v", lineNo, err)
			}
			cur.Hunks = append(cur.Hunks, h)
			oldLeft, newLeft = h.Old.Count, h.New.Count
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if oldLeft > 0 || newLeft > 0 {
		return nil, fmt.Errorf("diff ends inside a hunk")
	}
	flush()
	return files, nil
}

func (f *File) finish() File {
	switch f.Status {
	case "":
		f.Status = Modified
	case Added:
		f.OldPath = ""
	case Deleted:
		f.NewPath = ""
	}
	return *f
}

// parseGitHeader extracts the paths from the rest of a "diff --git a/x b/x"
// line. The line is ambiguous when paths contain spaces, so it only serves
// as a fallback for files without "---"/"+++" or rename lines, which happens
// for mode-only changes, binary files and empty added or deleted files. For
// those the two paths are identical, which resolves the ambiguity.
func parseGitHeader(rest string) (oldPath, newPath string) {
	if strings.HasPrefix(rest, `"`) {
		q, err := strconv.QuotedPrefix(rest)
		if err != nil {
			return "", ""
		}
		return parsePath(q, "a/"), parsePath(strings.TrimSpace(rest[len(q):]), "b/")
	}
	if len(rest)%2 == 1 {
		half := len(rest) / 2
		a, b := rest[:half], rest[half+1:]
		if rest[half] == ' ' && strings.HasPrefix(a, "a/") && strings.HasPrefix(b, "b/") && a[2:] == b[2:] {
			return a[2:], b[2:]
		}
	}
	return "", ""
}

// parsePath decodes a path from a diff header line: it removes the trailing
// tab git adds after names containing spaces, undoes C-style quoting of
// unusual names, and strips the "a/" or "b/" prefix. /dev/null becomes "".
func parsePath(s, prefix string) string {
	s = strings.TrimSuffix(s, "\t")
	if s == "/dev/null" {
		return ""
	}
	if strings.HasPrefix(s, `"`) {
		if unquoted, err := strconv.Unquote(s); err == nil {
			s = unquoted
		}
	}
	return strings.TrimPrefix(s, prefix)
}

// parseHunkHeader parses "@@ -oldStart[,oldCount] +newStart[,newCount] @@".
func parseHunkHeader(line string) (Hunk, error) {
	fields := strings.Fields(line)
	if len(fields) < 4 || fields[0] != "@@" || fields[3] != "@@" {
		return Hunk{}, fmt.Errorf("malformed hunk header %q", line)
	}
	oldRange, err := parseRange(fields[1], "-")
	if err != nil {
		return Hunk{}, fmt.Errorf("malformed hunk header %q: %v", line, err)
	}
	newRange, err := parseRange(fields[2], "+")
	if err != nil {
		return Hunk{}, fmt.Errorf("malformed hunk header %q: %v", line, err)
	}
	return Hunk{Old: oldRange, New: newRange}, nil
}

// parseRange parses "-12,3" or "+12" (a count of 1 is omitted by git).
func parseRange(s, sign string) (Range, error) {
	s, ok := strings.CutPrefix(s, sign)
	if !ok {
		return Range{}, fmt.Errorf("range %q does not start with %q", s, sign)
	}
	start, count, hasCount := strings.Cut(s, ",")
	r := Range{Count: 1}
	var err error
	if r.Start, err = strconv.Atoi(start); err != nil {
		return Range{}, err
	}
	if hasCount {
		if r.Count, err = strconv.Atoi(count); err != nil {
			return Range{}, err
		}
	}
	return r, nil
}
