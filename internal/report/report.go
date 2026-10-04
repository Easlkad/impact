// Package report turns the results of an analysis into an output model, and
// renders it as text, JSON or Markdown.
//
// The Report type is the contract with consumers of the JSON output: it is
// built explicitly from the analysis results instead of exposing the
// internal types, which can then change without breaking that contract.
// Fields may be added in later versions; renaming or removing one requires
// a new SchemaVersion.
//
// The analysis packages (impact, classify, scoring) know nothing about
// output: everything about presentation lives here.
package report

import (
	"strconv"
	"strings"

	"github.com/Easlkad/impact/internal/changes"
	"github.com/Easlkad/impact/internal/classify"
	"github.com/Easlkad/impact/internal/impact"
	"github.com/Easlkad/impact/internal/scoring"
)

// SchemaVersion is the version of the JSON structure of Report.
const SchemaVersion = 1

// Report is the result of "impact analyze", in output form. Lists are never
// nil, so they encode as [] rather than null, and are sorted as described
// on each field.
type Report struct {
	SchemaVersion int    `json:"schemaVersion"`
	Tool          Tool   `json:"tool"`
	Base          Commit `json:"base"`
	Head          Commit `json:"head"`
	// MergeBase is true when Base.Commit is the merge base of the two refs
	// (--merge-base) rather than the commit Base.Ref names.
	MergeBase bool `json:"mergeBase"`

	ChangedFiles []File `json:"changedFiles"` // changed Go files, in git's order

	Changed          []Function `json:"changed"`          // changed functions, tests included; by file and name
	DirectImpact     []Function `json:"directImpact"`     // distance 1, tests excluded; by file and name
	TransitiveImpact []Function `json:"transitiveImpact"` // distance 2+, tests excluded; by distance, file and name

	Packages  []Package  `json:"packages"`  // by package ID
	Endpoints []Endpoint `json:"endpoints"` // by handler distance, path and method
	Workers   []Worker   `json:"workers"`   // by distance, file and name
	Tests     []Test     `json:"tests"`     // by distance, file and name

	PackageLevelChanges []LineRange `json:"packageLevelChanges"` // not propagated
	UnmappedCallers     []string    `json:"unmappedCallers"`     // IDs of base callers of deleted functions missing from head
	Warnings            []string    `json:"warnings"`            // analysis problems in the changed files

	Summary    Summary `json:"summary"`
	Risk       Score   `json:"risk"`
	Confidence Score   `json:"confidence"`

	functions map[string]Function // every changed and impacted function, for the renderers
}

// Tool identifies the program that produced the report.
type Tool struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Commit is a ref as given and the commit used for it.
type Commit struct {
	Ref    string `json:"ref"`
	Commit string `json:"commit"`
}

// File is a changed file.
type File struct {
	Path    string `json:"path"`              // path in head; in base for deleted files
	OldPath string `json:"oldPath,omitempty"` // path in base, for renamed and copied files
	Status  string `json:"status"`            // added, modified, deleted, renamed or copied
}

// Function is a changed or impacted function or method.
type Function struct {
	ID      string `json:"id"`      // unique: "<package>.<name>"
	Name    string `json:"name"`    // "CreateUser" or "Service.Create"
	Package string `json:"package"` // package ID (Go import path)
	File    string `json:"file"`    // in head; in base for deleted functions
	Line    int    `json:"line"`    // first line of the declaration
	// Impact is "changed", "direct" or "transitive".
	Impact   string `json:"impact"`
	Distance int    `json:"distance"`         // 0 changed, 1 direct, 2+ transitive
	Change   string `json:"change,omitempty"` // for changed functions: modified, added or deleted
	// CausedBy holds the IDs of the functions it calls one step closer to a
	// change. Roots holds the IDs of all changed functions it depends on.
	// Path is a shortest chain of calls to a changed function, from this
	// function (first) to the changed one (last). All three are empty for
	// changed functions.
	CausedBy []string `json:"causedBy,omitempty"`
	Roots    []string `json:"roots,omitempty"`
	Path     []string `json:"path,omitempty"`
}

// Package counts the changed and impacted functions of a package, tests
// excluded.
type Package struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Dir        string `json:"dir"` // relative to the repository root
	Changed    int    `json:"changed"`
	Direct     int    `json:"direct"`
	Transitive int    `json:"transitive"`
}

// Endpoint is an HTTP route whose handler is changed or impacted.
type Endpoint struct {
	Method  string   `json:"method"` // "ANY" when the route accepts every method
	Path    string   `json:"path"`
	Handler Function `json:"handler"`
}

// Worker is a goroutine entry point that is changed or impacted.
type Worker struct {
	Function
	Kind      string   `json:"kind"`      // goroutine or background
	StartedBy []string `json:"startedBy"` // IDs of the functions starting it
}

// Test is a test function that is changed or impacted.
type Test struct {
	Function
	Kind string `json:"kind"` // test, benchmark, fuzz or example
}

// LineRange is a changed range of lines outside functions.
type LineRange struct {
	File  string `json:"file"`
	Start int    `json:"start"`
	End   int    `json:"end"`
	// Removed means the lines only exist in base, and the numbers refer to
	// the base version of the file.
	Removed bool `json:"removed"`
}

// Summary counts the lists of the report.
type Summary struct {
	ChangedFiles        int `json:"changedFiles"`
	Changed             int `json:"changed"`
	DirectImpact        int `json:"directImpact"`
	TransitiveImpact    int `json:"transitiveImpact"`
	Packages            int `json:"packages"`
	Endpoints           int `json:"endpoints"`
	Workers             int `json:"workers"`
	Tests               int `json:"tests"`
	PackageLevelChanges int `json:"packageLevelChanges"`
}

// Score is a risk or confidence score.
type Score struct {
	Value   int      `json:"value"`
	Level   string   `json:"level"`
	Factors []Factor `json:"factors"`
}

// Factor is one reason for a score.
type Factor struct {
	Name   string `json:"name"`
	Delta  int    `json:"delta"`
	Reason string `json:"reason"`
}

// Input holds the results of the analysis steps for one comparison.
type Input struct {
	Changes    *changes.Report
	Impact     *impact.Result
	Context    *classify.Result
	Assessment scoring.Assessment
	Version    string // version of the tool
}

// Build creates the report of an analysis.
func Build(in Input) *Report {
	ch, c := in.Changes, in.Context
	r := &Report{
		SchemaVersion:       SchemaVersion,
		Tool:                Tool{Name: "impact", Version: in.Version},
		Base:                Commit{Ref: ch.Base.Ref, Commit: ch.Base.Hash},
		Head:                Commit{Ref: ch.Head.Ref, Commit: ch.Head.Hash},
		MergeBase:           ch.MergeBase,
		ChangedFiles:        []File{},
		Changed:             []Function{},
		DirectImpact:        []Function{},
		TransitiveImpact:    []Function{},
		Packages:            []Package{},
		Endpoints:           []Endpoint{},
		Workers:             []Worker{},
		Tests:               []Test{},
		PackageLevelChanges: []LineRange{},
		UnmappedCallers:     append([]string{}, in.Impact.Unmapped...),
		Warnings:            append([]string{}, ch.Warnings...),
		Risk:                score(in.Assessment.Risk),
		Confidence:          score(in.Assessment.Confidence),
		functions:           make(map[string]Function),
	}

	for _, f := range ch.Files {
		file := File{Path: f.Path(), Status: string(f.Status)}
		if f.OldPath != "" && f.NewPath != "" && f.OldPath != f.NewPath {
			file.OldPath = f.OldPath
		}
		r.ChangedFiles = append(r.ChangedFiles, file)
	}

	fn := func(imp *impact.Impact) Function { return r.function(in.Impact, imp) }
	for _, imp := range c.Changed {
		r.Changed = append(r.Changed, fn(imp))
	}
	for _, imp := range c.Direct {
		r.DirectImpact = append(r.DirectImpact, fn(imp))
	}
	for _, imp := range c.Transitive {
		r.TransitiveImpact = append(r.TransitiveImpact, fn(imp))
	}
	for _, p := range c.Packages {
		r.Packages = append(r.Packages, Package{
			ID: p.Package.ID, Name: p.Package.Name, Dir: p.Package.Dir,
			Changed: p.Changed, Direct: p.Direct, Transitive: p.Transitive,
		})
	}
	for _, e := range c.Endpoints {
		r.Endpoints = append(r.Endpoints, Endpoint{Method: e.Method, Path: e.Path, Handler: fn(e.Handler)})
	}
	for _, w := range c.Workers {
		r.Workers = append(r.Workers, Worker{Function: fn(w.Function), Kind: string(w.Kind), StartedBy: append([]string{}, w.StartedBy...)})
	}
	for _, t := range c.Tests {
		r.Tests = append(r.Tests, Test{Function: fn(t.Function), Kind: string(t.Kind)})
	}
	for _, l := range ch.PackageLevel {
		r.PackageLevelChanges = append(r.PackageLevelChanges, LineRange{File: l.File, Start: l.Start, End: l.End, Removed: l.Removed})
	}

	r.Summary = Summary{
		ChangedFiles:        len(r.ChangedFiles),
		Changed:             len(r.Changed),
		DirectImpact:        len(r.DirectImpact),
		TransitiveImpact:    len(r.TransitiveImpact),
		Packages:            len(r.Packages),
		Endpoints:           len(r.Endpoints),
		Workers:             len(r.Workers),
		Tests:               len(r.Tests),
		PackageLevelChanges: len(r.PackageLevelChanges),
	}
	return r
}

// function converts an impact, and remembers it for the renderers.
func (r *Report) function(res *impact.Result, imp *impact.Impact) Function {
	f := Function{
		ID:       imp.ID(),
		Name:     imp.Function.QualifiedName(),
		Package:  imp.Function.Package,
		File:     imp.Function.File,
		Line:     imp.Function.StartLine,
		Impact:   impactName(imp.Distance),
		Distance: imp.Distance,
		Change:   string(imp.Change),
	}
	if imp.Distance > 0 {
		f.CausedBy = append([]string{}, imp.CausedBy...)
		f.Roots = append([]string{}, imp.Roots...)
		f.Path = res.Path(imp.ID())
	}
	r.functions[f.ID] = f
	return f
}

func impactName(distance int) string {
	switch distance {
	case 0:
		return "changed"
	case 1:
		return "direct"
	}
	return "transitive"
}

func score(s scoring.Score) Score {
	out := Score{Value: s.Value, Level: s.Level, Factors: []Factor{}}
	for _, f := range s.Factors {
		out.Factors = append(out.Factors, Factor{Name: f.Name, Delta: f.Delta, Reason: f.Reason})
	}
	return out
}

// lookup returns a changed or impacted function by ID.
func (r *Report) lookup(id string) Function { return r.functions[id] }

// impactLabel describes how a function is affected: "changed (modified)",
// "direct" or "transitive (distance 3)".
func impactLabel(f Function) string {
	switch f.Impact {
	case "changed":
		return "changed (" + f.Change + ")"
	case "transitive":
		return "transitive (distance " + strconv.Itoa(f.Distance) + ")"
	}
	return f.Impact
}

// PackageLabel returns a short display label for a package: its directory
// relative to the repository root ("internal/user"), or its name for the
// package at the root. External test packages get a "_test" suffix.
func PackageLabel(dir, name string) string {
	switch {
	case dir == ".":
		return name
	case strings.HasSuffix(name, "_test"):
		return dir + "_test"
	}
	return dir
}

// shortHash abbreviates a commit hash for display.
func shortHash(hash string) string {
	if len(hash) > 7 {
		return hash[:7]
	}
	return hash
}

// isHash reports whether a ref is a commit hash rather than a name, as when
// CI passes commit SHAs: such a ref is shown abbreviated, not next to itself.
func isHash(ref string) bool {
	if len(ref) < 7 {
		return false
	}
	for _, r := range ref {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

// refLabel returns the ref as shown to people: abbreviated if it is a hash.
func refLabel(ref string) string {
	if isHash(ref) {
		return shortHash(ref)
	}
	return ref
}
