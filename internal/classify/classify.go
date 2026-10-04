// Package classify places changed and impacted functions in their
// application context: the packages they belong to, the HTTP endpoints they
// serve, the background workers they start, and the tests they are.
//
// It does not compute impact. It reads an impact.Result and the facts the
// analyzer recorded in the model (routes, goroutine launches, test kinds),
// and only reports a context when those facts establish it: nothing is
// inferred from names.
package classify

import (
	"cmp"
	"slices"

	"github.com/Easlkad/impact/internal/changes"
	"github.com/Easlkad/impact/internal/impact"
	"github.com/Easlkad/impact/internal/model"
)

// Result is the application context of an impact analysis.
type Result struct {
	Changed []*impact.Impact // all changed functions, tests included
	// Direct and Transitive are the impacted functions at distance 1 and at
	// distance 2 or more, without tests: those are in Tests.
	Direct     []*impact.Impact
	Transitive []*impact.Impact

	Packages  []PackageImpact // sorted by package ID
	Endpoints []Endpoint      // sorted by handler distance, path and method
	Workers   []Worker        // sorted like impacts: distance, file, name
	Tests     []Test          // sorted like impacts
}

// PackageImpact counts the changed and impacted functions of a package,
// tests excluded.
type PackageImpact struct {
	Package    *model.Package
	Changed    int
	Direct     int
	Transitive int
}

// Endpoint is an HTTP route whose handler is changed or impacted.
type Endpoint struct {
	Method  string // "ANY" when the route accepts every method
	Path    string
	Handler *impact.Impact
}

// WorkerKind says how a worker is started.
type WorkerKind string

const (
	// Goroutine workers are started directly: go f().
	Goroutine WorkerKind = "goroutine"
	// Background workers are called from an anonymous function started as
	// a goroutine: go func() { f() }().
	Background WorkerKind = "background"
)

// Worker is a function started concurrently that is changed or impacted.
type Worker struct {
	Function  *impact.Impact
	Kind      WorkerKind // Goroutine if it is started directly anywhere
	StartedBy []string   // IDs of the functions that start it, sorted
}

// Test is a changed or impacted function run by the test runner.
type Test struct {
	Function *impact.Impact
	Kind     model.TestKind
}

// Classify places the functions of r in context. base and head are the
// analyses r was computed from; either may be nil when there is nothing to
// classify.
//
// Routes and goroutine launches are taken from the head commit. For deleted
// functions, which only exist in the base commit, they are taken from the
// base commit, so a route whose handler was deleted is still reported.
func Classify(r *impact.Result, base, head *model.Repository) *Result {
	c := &Result{Changed: r.Changed}
	for _, imp := range r.Impacted {
		switch {
		case imp.Function.Test != model.NotTest:
		case imp.Distance == 1:
			c.Direct = append(c.Direct, imp)
		default:
			c.Transitive = append(c.Transitive, imp)
		}
	}
	for _, imp := range append(slices.Clone(r.Changed), r.Impacted...) {
		if imp.Function.Test != model.NotTest {
			c.Tests = append(c.Tests, Test{Function: imp, Kind: imp.Function.Test})
		}
	}
	c.Packages = packages(r, base, head)
	c.Endpoints = endpoints(r, base, head)
	c.Workers = workers(r, base, head)
	return c
}

func packages(r *impact.Result, base, head *model.Repository) []PackageImpact {
	known := make(map[string]*model.Package)
	for _, repo := range []*model.Repository{base, head} { // head wins
		if repo != nil {
			for _, p := range repo.Packages {
				known[p.ID] = p
			}
		}
	}
	counts := make(map[string]*PackageImpact)
	for _, imp := range append(slices.Clone(r.Changed), r.Impacted...) {
		fn := imp.Function
		if fn.Test != model.NotTest || known[fn.Package] == nil {
			continue
		}
		pc := counts[fn.Package]
		if pc == nil {
			pc = &PackageImpact{Package: known[fn.Package]}
			counts[fn.Package] = pc
		}
		switch {
		case imp.Distance == 0:
			pc.Changed++
		case imp.Distance == 1:
			pc.Direct++
		default:
			pc.Transitive++
		}
	}
	out := make([]PackageImpact, 0, len(counts))
	for _, pc := range counts {
		out = append(out, *pc)
	}
	slices.SortFunc(out, func(a, b PackageImpact) int { return cmp.Compare(a.Package.ID, b.Package.ID) })
	return out
}

func endpoints(r *impact.Result, base, head *model.Repository) []Endpoint {
	type key struct{ method, path, handler string }
	seen := make(map[key]bool)
	var out []Endpoint
	add := func(repo *model.Repository, onlyDeleted bool) {
		if repo == nil {
			return
		}
		for _, fn := range repo.Functions() {
			for _, route := range fn.Routes {
				if !route.Complete() {
					continue // an endpoint needs both its pattern and its handler
				}
				imp := r.Lookup(route.Handler)
				if imp == nil || (onlyDeleted && imp.Change != changes.Deleted) {
					continue
				}
				k := key{route.Method, route.Path, route.Handler}
				if !seen[k] {
					seen[k] = true
					out = append(out, Endpoint{Method: route.Method, Path: route.Path, Handler: imp})
				}
			}
		}
	}
	add(head, false)
	add(base, true)
	slices.SortFunc(out, func(a, b Endpoint) int {
		return cmp.Or(
			cmp.Compare(a.Handler.Distance, b.Handler.Distance),
			cmp.Compare(a.Path, b.Path),
			cmp.Compare(a.Method, b.Method),
			cmp.Compare(a.Handler.ID(), b.Handler.ID()),
		)
	})
	return out
}

func workers(r *impact.Result, base, head *model.Repository) []Worker {
	found := make(map[string]*Worker)
	add := func(repo *model.Repository, onlyDeleted bool) {
		if repo == nil {
			return
		}
		for _, fn := range repo.Functions() {
			for _, call := range fn.Calls {
				if call.Mode == model.CallSync || call.Kind != model.CallInternal {
					continue
				}
				imp := r.Lookup(call.Callee)
				if imp == nil || (onlyDeleted && imp.Change != changes.Deleted) {
					continue
				}
				w := found[call.Callee]
				if w == nil {
					w = &Worker{Function: imp, Kind: Background}
					found[call.Callee] = w
				}
				if call.Mode == model.CallAsync {
					w.Kind = Goroutine
				}
				if !slices.Contains(w.StartedBy, fn.ID) {
					w.StartedBy = append(w.StartedBy, fn.ID)
				}
			}
		}
	}
	add(head, false)
	add(base, true)

	out := make([]Worker, 0, len(found))
	for _, w := range found {
		slices.Sort(w.StartedBy)
		out = append(out, *w)
	}
	slices.SortFunc(out, func(a, b Worker) int { return compareImpacts(a.Function, b.Function) })
	return out
}

// compareImpacts orders impacts like impact.Result does: by distance, file
// and name.
func compareImpacts(a, b *impact.Impact) int {
	return cmp.Or(
		cmp.Compare(a.Distance, b.Distance),
		cmp.Compare(a.Function.File, b.Function.File),
		cmp.Compare(a.Function.QualifiedName(), b.Function.QualifiedName()),
		cmp.Compare(a.ID(), b.ID()),
	)
}
