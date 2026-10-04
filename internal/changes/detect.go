// Package changes determines which Go functions and methods a git diff
// changes, by matching the diff's changed line ranges against the function
// source ranges found by the analyzer in the base and head commits.
package changes

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/Easlkad/impact/internal/gitdiff"
	"github.com/Easlkad/impact/internal/model"
)

// Kind says how a function changed.
type Kind string

const (
	Modified Kind = "modified" // exists in both commits, and changed lines overlap it
	Added    Kind = "added"    // exists only in the head commit
	Deleted  Kind = "deleted"  // exists only in the base commit
)

// FunctionChange is a function or method touched by the diff.
type FunctionChange struct {
	Kind Kind
	// Function comes from the head analysis, or from the base analysis for
	// deleted functions.
	Function *model.Function
	// Declaration is where the change was found: the declaration in the head
	// commit, or in the base commit for deleted functions.
	Declaration model.Declaration
}

// LineChange is a range of changed non-blank lines outside any function:
// package-level declarations, imports, or comments between functions.
type LineChange struct {
	File       string
	Start, End int
	// Removed means the lines exist only in the base commit: File and the
	// line numbers then refer to the base version of the file.
	Removed bool
}

func (c LineChange) String() string {
	s := fmt.Sprintf("%s:%d", c.File, c.Start)
	if c.End != c.Start {
		s += fmt.Sprintf("-%d", c.End)
	}
	return s
}

// Detect matches the hunks of the diff against the functions of the base and
// head analyses.
//
// Functions are identified by Function.ID, so a function is modified (not
// deleted and added) when it moves to another file of the same package. A
// function is only reported when its own lines changed: changes elsewhere in
// its file do not count. Changed lines outside every function are returned
// as LineChanges. In added and deleted files, where every line is new or
// gone, only the package-level declarations (types, constants, variables)
// are: deleting a file of constants still used elsewhere is a change too.
func Detect(diff []gitdiff.File, base, head *model.Repository) ([]FunctionChange, []LineChange) {
	headPath := make(map[string]string) // base path -> head path, for renamed files
	for _, f := range diff {
		if f.OldPath != "" && f.NewPath != "" {
			headPath[f.OldPath] = f.NewPath
		}
	}
	baseIdx := newIndex(base, func(p string) string {
		if n, ok := headPath[p]; ok {
			return n
		}
		return p
	})
	headIdx := newIndex(head, func(p string) string { return p })

	found := make(map[string]*FunctionChange) // by function key
	record := func(key string, kind Kind, d decl) {
		if found[key] == nil {
			found[key] = &FunctionChange{Kind: kind, Function: d.fn, Declaration: d.loc}
		}
	}
	var lines []LineChange

	for _, f := range diff {
		headDecls := headIdx.files[f.NewPath]
		baseDecls := baseIdx.files[f.OldPath]

		for _, d := range headDecls {
			if _, inBase := baseIdx.keys[d.key]; !inBase {
				if baseIdx.analyzed(f.OldPath) {
					record(d.key, Added, d)
				}
			} else if overlapsAny(d.loc, f.Hunks, newSide) {
				record(d.key, Modified, d)
			}
		}
		for _, d := range baseDecls {
			if hd, inHead := headIdx.keys[d.key]; !inHead {
				if headIdx.analyzed(f.NewPath) {
					record(d.key, Deleted, d)
				}
			} else if overlapsAny(d.loc, f.Hunks, oldSide) {
				// Only lines were removed from it, or it moved here from
				// another file: report it where it is now.
				record(d.key, Modified, hd)
			}
		}

		// In added and deleted files every line is new or gone: only the
		// declarations are reported, not the package clause, imports or
		// comments, which affect nothing outside the file.
		switch f.Status {
		case gitdiff.Added:
			for _, d := range headIdx.decls[f.NewPath] {
				lines = append(lines, LineChange{File: f.NewPath, Start: d.StartLine, End: d.EndLine})
			}
			continue
		case gitdiff.Deleted:
			for _, d := range baseIdx.decls[f.OldPath] {
				lines = append(lines, LineChange{File: f.OldPath, Start: d.StartLine, End: d.EndLine, Removed: true})
			}
			continue
		}
		for _, h := range f.Hunks {
			// Lines outside functions that changed on both sides of a hunk
			// (a package-level line was edited) are reported once, with the
			// head side. Blank lines are ignored.
			var added []gitdiff.Range
			if headIdx.known[f.NewPath] {
				added = nonBlank(outside(h.New, headDecls), h.New.Start, h.Added)
			}
			for _, r := range added {
				lines = append(lines, LineChange{File: f.NewPath, Start: r.Start, End: r.End()})
			}
			if len(added) > 0 || !baseIdx.known[f.OldPath] {
				continue
			}
			for _, r := range nonBlank(outside(h.Old, baseDecls), h.Old.Start, h.Removed) {
				lines = append(lines, LineChange{File: f.OldPath, Start: r.Start, End: r.End(), Removed: true})
			}
		}
	}

	funcs := make([]FunctionChange, 0, len(found))
	for _, c := range found {
		funcs = append(funcs, *c)
	}
	slices.SortFunc(funcs, func(a, b FunctionChange) int {
		return cmp.Or(
			cmp.Compare(a.Declaration.File, b.Declaration.File),
			cmp.Compare(a.Declaration.StartLine, b.Declaration.StartLine),
			cmp.Compare(a.Function.ID, b.Function.ID),
		)
	})
	return funcs, lines
}

// decl is one declaration of a function, with the key that identifies the
// function across the base and head commits.
type decl struct {
	key string
	fn  *model.Function
	loc model.Declaration
}

type index struct {
	files map[string][]decl              // by file path, in source order
	keys  map[string]decl                // by key: the first declaration
	known map[string]bool                // files present in the analysis
	decls map[string][]model.Declaration // package-level declarations other than functions, by file path
}

// newIndex indexes the function declarations of repo. headPath maps a file
// path of repo to its path in the head commit; it is used to build the keys
// of init functions.
func newIndex(repo *model.Repository, headPath func(string) string) *index {
	idx := &index{
		files: make(map[string][]decl),
		keys:  make(map[string]decl),
		known: make(map[string]bool),
		decls: make(map[string][]model.Declaration),
	}
	for _, p := range repo.Packages {
		for _, f := range p.Files {
			idx.known[f.Path] = true
			idx.decls[f.Path] = f.Declarations
		}
	}
	inits := make(map[string]int) // init functions seen per file
	for _, fn := range repo.Functions() {
		for _, loc := range fn.Declarations {
			key := fn.ID
			if fn.Name == "init" && fn.Receiver == "" {
				// The analyzer numbers init functions within their file, and
				// its IDs name the file, which may have been renamed. Key
				// them by their file in the head commit and their position
				// in it, so an init in a renamed file is still the same one.
				// The IDs themselves never collide between a deleted init
				// and a head function: the file of a deleted init either
				// still exists in head (and the same key would be found
				// there) or was deleted or renamed away.
				key = fmt.Sprintf("%s.init@%s#%d", fn.Package, headPath(loc.File), inits[loc.File])
				inits[loc.File]++
			}
			d := decl{key: key, fn: fn, loc: loc}
			idx.files[loc.File] = append(idx.files[loc.File], d)
			if _, ok := idx.keys[key]; !ok {
				idx.keys[key] = d
			}
		}
	}
	for _, decls := range idx.files {
		slices.SortFunc(decls, func(a, b decl) int { return cmp.Compare(a.loc.StartLine, b.loc.StartLine) })
	}
	return idx
}

// analyzed reports whether the functions of the file at path are known:
// false only for a Go file missing from the analysis, typically because it
// failed to parse. Its functions must then not be taken as added or deleted.
// An empty path (no such file on this side) is trivially analyzed.
func (idx *index) analyzed(path string) bool {
	return path == "" || !strings.HasSuffix(path, ".go") || idx.known[path]
}

type side func(gitdiff.Hunk) gitdiff.Range

func oldSide(h gitdiff.Hunk) gitdiff.Range { return h.Old }
func newSide(h gitdiff.Hunk) gitdiff.Range { return h.New }

// overlapsAny reports whether a declaration shares a line with one side of
// any of the hunks.
func overlapsAny(loc model.Declaration, hunks []gitdiff.Hunk, side side) bool {
	for _, h := range hunks {
		r := side(h)
		if !r.Empty() && r.Start <= loc.EndLine && loc.StartLine <= r.End() {
			return true
		}
	}
	return false
}

// outside returns the parts of r not covered by any of decls, which must be
// sorted by StartLine.
func outside(r gitdiff.Range, decls []decl) []gitdiff.Range {
	if r.Empty() {
		return nil
	}
	var gaps []gitdiff.Range
	next := r.Start // first line not yet accounted for
	for _, d := range decls {
		if d.loc.EndLine < next {
			continue
		}
		if d.loc.StartLine > r.End() {
			break
		}
		if d.loc.StartLine > next {
			gaps = append(gaps, gitdiff.Range{Start: next, Count: d.loc.StartLine - next})
		}
		next = d.loc.EndLine + 1
	}
	if next <= r.End() {
		gaps = append(gaps, gitdiff.Range{Start: next, Count: r.End() - next + 1})
	}
	return gaps
}

// nonBlank returns the parts of ranges made of non-blank lines. text holds
// the lines of one side of a hunk, the first being line start.
func nonBlank(ranges []gitdiff.Range, start int, text []string) []gitdiff.Range {
	var out []gitdiff.Range
	for _, r := range ranges {
		for line := r.Start; line <= r.End(); line++ {
			if i := line - start; i < len(text) && strings.TrimSpace(text[i]) == "" {
				continue
			}
			if n := len(out); n > 0 && out[n-1].End() == line-1 {
				out[n-1].Count++
			} else {
				out = append(out, gitdiff.Range{Start: line, Count: 1})
			}
		}
	}
	return out
}
