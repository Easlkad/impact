// Package impact finds the functions that may be affected by a change: the
// functions that call a changed function, directly or through other
// functions.
//
// It walks the call graph backwards from the changed functions. Only calls
// between functions of the repository (graph edges) propagate impact;
// external and unresolved calls do not. Package-level changes are not
// propagated.
package impact

import (
	"cmp"
	"math/bits"
	"slices"

	"github.com/Easlkad/impact/internal/changes"
	"github.com/Easlkad/impact/internal/graph"
	"github.com/Easlkad/impact/internal/model"
)

// Impact is a changed function, or a function affected by a change.
type Impact struct {
	// Function comes from the head commit, or from the base commit for a
	// deleted function.
	Function *model.Function
	// Change says how a changed function changed; it is empty for impacted
	// functions.
	Change changes.Kind
	// Distance is 0 for a changed function, 1 for a function calling a
	// changed function, and n for a function n calls away from the nearest
	// change.
	Distance int
	// CausedBy holds the IDs of the functions called by this one that are
	// one step closer to a change (at Distance-1), sorted. Following any of
	// them repeatedly leads to a changed function along a shortest path.
	// Empty for changed functions.
	CausedBy []string
	// Roots holds the IDs of all changed functions this one depends on,
	// directly or transitively, sorted. Empty for changed functions.
	Roots []string
}

// ID returns the ID of the function.
func (i *Impact) ID() string { return i.Function.ID }

// Result is the outcome of an impact analysis.
type Result struct {
	Changed  []*Impact // Distance 0, sorted by file and name
	Impacted []*Impact // Distance 1 and more, sorted by distance, file and name
	// Unmapped holds the IDs of base-commit callers of deleted functions
	// that do not exist in the head commit and are not changed functions
	// themselves, so their impact could not be followed. Sorted.
	Unmapped []string

	byID map[string]*Impact
}

// Lookup returns the changed or impacted function with the given ID, or nil.
func (r *Result) Lookup(id string) *Impact { return r.byID[id] }

// Direct returns the impacted functions at distance 1.
func (r *Result) Direct() []*Impact {
	return slices.DeleteFunc(slices.Clone(r.Impacted), func(i *Impact) bool { return i.Distance != 1 })
}

// Transitive returns the impacted functions at distance 2 or more.
func (r *Result) Transitive() []*Impact {
	return slices.DeleteFunc(slices.Clone(r.Impacted), func(i *Impact) bool { return i.Distance < 2 })
}

// Path returns a shortest chain of calls from the function id to a changed
// function, as function IDs: id, a function id calls, ..., a changed
// function. It returns nil for an unknown id.
func (r *Result) Path(id string) []string {
	var path []string
	for imp := r.byID[id]; imp != nil; {
		path = append(path, imp.ID())
		if len(imp.CausedBy) == 0 {
			break
		}
		imp = r.byID[imp.CausedBy[0]]
	}
	return path
}

// FromReport analyzes the impact of the function changes found by
// changes.Compare, using the call graphs of its two analyses.
func FromReport(report *changes.Report) *Result {
	if report.BaseAnalysis == nil || report.HeadAnalysis == nil {
		return Analyze(nil, nil, nil) // no Go file changed
	}
	return Analyze(report.Functions, graph.Build(report.BaseAnalysis), graph.Build(report.HeadAnalysis))
}

// Analyze finds the functions impacted by the changed functions.
//
// Callers are looked up in the head graph. A deleted function no longer
// exists there, so its callers are taken from the base graph; each is
// followed further only if a function with the same ID exists in the head
// graph. Callers without such a counterpart are listed in Result.Unmapped.
func Analyze(changed []changes.FunctionChange, base, head *graph.Graph) *Result {
	r := &Result{byID: make(map[string]*Impact)}
	deleted := make(map[string]bool)
	for _, c := range changed {
		imp := &Impact{Function: c.Function, Change: c.Kind}
		r.byID[imp.ID()] = imp
		r.Changed = append(r.Changed, imp)
		if c.Kind == changes.Deleted {
			deleted[imp.ID()] = true
		}
	}

	unmapped := make(map[string]bool)
	callers := func(id string) []string {
		if !deleted[id] {
			return head.Callers(id)
		}
		var mapped []string
		for _, caller := range base.Callers(id) {
			switch {
			case head.Function(caller) != nil:
				mapped = append(mapped, caller)
			case r.byID[caller] == nil:
				unmapped[caller] = true
			}
		}
		return mapped
	}

	// Breadth-first search backwards from all changed functions at once.
	// The level at which a function is first reached is its distance to the
	// nearest change; functions already reached are never revisited, which
	// also makes cycles harmless.
	roots := sortedKeys(r.byID)
	callersOf := make(map[string][]string) // every reached function's callers, fetched once
	frontier := roots
	for distance := 1; len(frontier) > 0; distance++ {
		reachedVia := make(map[string][]string) // newly reached caller -> callees in the frontier
		for _, id := range frontier {
			callersOf[id] = callers(id)
			for _, caller := range callersOf[id] {
				if r.byID[caller] == nil {
					reachedVia[caller] = append(reachedVia[caller], id)
				}
			}
		}
		frontier = sortedKeys(reachedVia)
		for _, id := range frontier {
			imp := &Impact{
				Function: head.Function(id),
				Distance: distance,
				CausedBy: sortedUnique(reachedVia[id]),
			}
			r.byID[id] = imp
			r.Impacted = append(r.Impacted, imp)
		}
	}

	r.attributeRoots(roots, callersOf)

	r.Unmapped = sortedKeys(unmapped)
	order := func(a, b *Impact) int {
		return cmp.Or(
			cmp.Compare(a.Distance, b.Distance),
			cmp.Compare(a.Function.File, b.Function.File),
			cmp.Compare(a.Function.QualifiedName(), b.Function.QualifiedName()),
			cmp.Compare(a.ID(), b.ID()),
		)
	}
	slices.SortFunc(r.Changed, order)
	slices.SortFunc(r.Impacted, order)
	return r
}

// attributeRoots sets the Roots of every impacted function: all the changed
// functions it depends on, not only the nearest ones. roots are the IDs of
// the changed functions, sorted, and callers holds the callers of every
// reached function, as the breadth-first search followed them.
//
// One search per changed function would cost O(roots × (V + E)). Instead,
// the strongly connected components of the reached call graph are found
// once, and the sets of roots, as bitsets, flow from callees to callers in
// topological order: every function of a cycle depends on the same roots.
func (r *Result) attributeRoots(roots []string, callers map[string][]string) {
	ids := sortedKeys(r.byID)
	index := make(map[string]int, len(ids))
	for i, id := range ids {
		index[id] = i
	}
	// adj[i] lists the callers of ids[i]: edges go from callee to caller,
	// the direction in which a change propagates.
	adj := make([][]int, len(ids))
	for i, id := range ids {
		for _, c := range callers[id] {
			if j, ok := index[c]; ok {
				adj[i] = append(adj[i], j)
			}
		}
	}

	comp, ncomp := components(adj)
	members := make([][]int, ncomp)
	for v, c := range comp {
		members[c] = append(members[c], v)
	}

	words := (len(roots) + 63) / 64
	sets := make([]uint64, ncomp*words) // the roots each component depends on
	set := func(c int) []uint64 { return sets[c*words : (c+1)*words] }
	for bit, id := range roots {
		c := comp[index[id]]
		set(c)[bit/64] |= 1 << (bit % 64)
	}
	// components numbers a component after all those it reaches (its
	// callers): from the highest number down, a component has received
	// the roots of all its callees before passing them on.
	for c := ncomp - 1; c >= 0; c-- {
		from := set(c)
		for _, v := range members[c] {
			for _, w := range adj[v] {
				if d := comp[w]; d != c {
					to := set(d)
					for k := range to {
						to[k] |= from[k]
					}
				}
			}
		}
	}

	for i, id := range ids {
		imp := r.byID[id]
		if imp.Distance == 0 {
			continue
		}
		// Bits in increasing order give the roots sorted, as roots is.
		for k, word := range set(comp[i]) {
			for word != 0 {
				imp.Roots = append(imp.Roots, roots[k*64+bits.TrailingZeros64(word)])
				word &= word - 1 // clear the lowest set bit
			}
		}
	}
}

// components finds the strongly connected components of a graph given by
// its adjacency lists, with Tarjan's algorithm. It returns the component of
// each vertex and the number of components. Components are numbered in the
// order they are completed: a component's number is higher than those of
// all the components it reaches.
func components(adj [][]int) (comp []int, n int) {
	order := make([]int, len(adj)) // discovery order, -1 before
	low := make([]int, len(adj))
	onStack := make([]bool, len(adj))
	comp = make([]int, len(adj))
	var stack []int
	next := 0
	var visit func(v int)
	visit = func(v int) {
		order[v], low[v] = next, next
		next++
		stack = append(stack, v)
		onStack[v] = true
		for _, w := range adj[v] {
			switch {
			case order[w] < 0:
				visit(w)
				low[v] = min(low[v], low[w])
			case onStack[w]:
				low[v] = min(low[v], order[w])
			}
		}
		if low[v] == order[v] {
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[w] = false
				comp[w] = n
				if w == v {
					break
				}
			}
			n++
		}
	}
	for v := range order {
		order[v] = -1
	}
	for v := range adj {
		if order[v] < 0 {
			visit(v)
		}
	}
	return comp, n
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func sortedUnique(ids []string) []string {
	ids = slices.Clone(ids)
	slices.Sort(ids)
	return slices.Compact(ids)
}
