// Package graph indexes the call relationships of a model.Repository so they
// can be queried in both directions: what a function calls, and what calls it.
package graph

import (
	"cmp"
	"slices"

	"github.com/Easlkad/impact/internal/model"
)

// Edge is a call relationship: the function From calls the function To.
type Edge struct {
	From string // caller Function.ID
	To   string // callee Function.ID
}

// Graph is a call graph over the functions of one repository. Only calls
// between functions of the repository (model.CallInternal) become edges, and
// a function calling another several times yields a single edge.
type Graph struct {
	functions map[string]*model.Function
	callees   map[string][]string
	callers   map[string][]string
	edges     []Edge
}

// Build creates the call graph of repo.
func Build(repo *model.Repository) *Graph {
	g := &Graph{
		functions: make(map[string]*model.Function),
		callees:   make(map[string][]string),
		callers:   make(map[string][]string),
	}
	fns := repo.Functions()
	for _, fn := range fns {
		g.functions[fn.ID] = fn
	}

	seen := make(map[Edge]bool)
	for _, fn := range fns {
		for _, call := range fn.Calls {
			if call.Kind != model.CallInternal || g.functions[call.Callee] == nil {
				continue
			}
			e := Edge{From: fn.ID, To: call.Callee}
			if seen[e] {
				continue
			}
			seen[e] = true
			g.edges = append(g.edges, e)
			g.callees[e.From] = append(g.callees[e.From], e.To)
			g.callers[e.To] = append(g.callers[e.To], e.From)
		}
	}

	slices.SortFunc(g.edges, func(a, b Edge) int {
		return cmp.Or(cmp.Compare(a.From, b.From), cmp.Compare(a.To, b.To))
	})
	for _, ids := range g.callees {
		slices.Sort(ids)
	}
	for _, ids := range g.callers {
		slices.Sort(ids)
	}
	return g
}

// Function returns the function with the given ID, or nil.
func (g *Graph) Function(id string) *model.Function { return g.functions[id] }

// Callees returns the IDs of the functions that id calls, sorted.
func (g *Graph) Callees(id string) []string { return slices.Clone(g.callees[id]) }

// Callers returns the IDs of the functions that call id, sorted.
func (g *Graph) Callers(id string) []string { return slices.Clone(g.callers[id]) }

// Edges returns every call relationship, sorted by caller then callee.
func (g *Graph) Edges() []Edge { return slices.Clone(g.edges) }
