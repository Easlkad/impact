package impact

import (
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/Easlkad/impact/internal/changes"
	"github.com/Easlkad/impact/internal/graph"
	"github.com/Easlkad/impact/internal/model"
)

// buildGraph creates a call graph from "caller -> callee" edges. Each
// function lives in the file "<lower-case name>.go".
func buildGraph(edges ...string) *graph.Graph {
	fns := make(map[string]*model.Function)
	get := func(id string) *model.Function {
		if fns[id] == nil {
			fns[id] = &model.Function{ID: id, Name: id, File: strings.ToLower(id) + ".go"}
		}
		return fns[id]
	}
	for _, e := range edges {
		from, to, ok := strings.Cut(e, " -> ")
		if !ok {
			get(e) // a function without calls
			continue
		}
		get(to)
		f := get(from)
		f.Calls = append(f.Calls, model.Call{Callee: to, Kind: model.CallInternal})
	}
	ids := make([]string, 0, len(fns))
	for id := range fns {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	file := &model.File{}
	for _, id := range ids {
		file.Functions = append(file.Functions, fns[id])
	}
	return graph.Build(&model.Repository{Packages: []*model.Package{{Files: []*model.File{file}}}})
}

// change returns a FunctionChange for a function of g.
func change(t *testing.T, g *graph.Graph, kind changes.Kind, id string) changes.FunctionChange {
	t.Helper()
	fn := g.Function(id)
	if fn == nil {
		t.Fatalf("no function %s in graph", id)
	}
	return changes.FunctionChange{Kind: kind, Function: fn}
}

func modified(t *testing.T, g *graph.Graph, ids ...string) []changes.FunctionChange {
	var out []changes.FunctionChange
	for _, id := range ids {
		out = append(out, change(t, g, changes.Modified, id))
	}
	return out
}

// summary formats impacted functions as "<id> d<distance> via <CausedBy>",
// in the result's order.
func summary(r *Result) []string {
	var out []string
	for _, imp := range r.Impacted {
		out = append(out, fmt.Sprintf("%s d%d via %s", imp.ID(), imp.Distance, strings.Join(imp.CausedBy, ",")))
	}
	return out
}

func assertEqual[T any](t *testing.T, what string, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s:\n got: %v\nwant: %v", what, got, want)
	}
}

func TestOneDirectCaller(t *testing.T) {
	g := buildGraph("B -> C")
	r := Analyze(modified(t, g, "C"), g, g)
	assertEqual(t, "impacted", summary(r), []string{"B d1 via C"})
	assertEqual(t, "path", r.Path("B"), []string{"B", "C"})
	if len(r.Changed) != 1 || r.Changed[0].Distance != 0 || r.Changed[0].Change != changes.Modified {
		t.Errorf("changed = %+v", r.Changed)
	}
}

func TestTransitiveCallers(t *testing.T) {
	g := buildGraph("A -> B", "B -> C", "C -> D")
	r := Analyze(modified(t, g, "D"), g, g)
	assertEqual(t, "impacted", summary(r), []string{"C d1 via D", "B d2 via C", "A d3 via B"})
	assertEqual(t, "path", r.Path("A"), []string{"A", "B", "C", "D"})
	assertEqual(t, "direct", ids(r.Direct()), []string{"C"})
	assertEqual(t, "transitive", ids(r.Transitive()), []string{"B", "A"})
}

func TestMultipleCallers(t *testing.T) {
	g := buildGraph("Z -> C", "Y -> C", "X -> C")
	r := Analyze(modified(t, g, "C"), g, g)
	// Sorted by distance, then file.
	assertEqual(t, "impacted", summary(r), []string{"X d1 via C", "Y d1 via C", "Z d1 via C"})
}

func TestDiamond(t *testing.T) {
	g := buildGraph("A -> B", "A -> C", "B -> D", "C -> D")
	r := Analyze(modified(t, g, "D"), g, g)
	// A is reached through both B and C but reported once.
	assertEqual(t, "impacted", summary(r), []string{"B d1 via D", "C d1 via D", "A d2 via B,C"})
	assertEqual(t, "path", r.Path("A"), []string{"A", "B", "D"})
}

func TestCycles(t *testing.T) {
	g := buildGraph("A -> B", "B -> C", "C -> A", "X -> Y", "Y -> X", "Y -> C")
	r := Analyze(modified(t, g, "C"), g, g)
	// C is in a cycle with A and B, and Y in another cycle with X. The
	// search terminates, and C is not reported as impacted by itself.
	assertEqual(t, "impacted", summary(r), []string{"B d1 via C", "Y d1 via C", "A d2 via B", "X d2 via Y"})
	assertEqual(t, "path", r.Path("A"), []string{"A", "B", "C"})
}

func TestMultipleChangedRoots(t *testing.T) {
	g := buildGraph("A -> C1", "B -> C2", "D -> A", "D -> B", "E -> A")
	r := Analyze(modified(t, g, "C1", "C2"), g, g)
	assertEqual(t, "impacted", summary(r), []string{"A d1 via C1", "B d1 via C2", "D d2 via A,B", "E d2 via A"})
	assertEqual(t, "roots of D", r.Lookup("D").Roots, []string{"C1", "C2"})
	assertEqual(t, "roots of E", r.Lookup("E").Roots, []string{"C1"})
}

func TestChangedFunctionsCallingEachOther(t *testing.T) {
	g := buildGraph("P -> A", "A -> B", "B -> A")
	r := Analyze(modified(t, g, "A", "B"), g, g)
	// A and B stay changed (distance 0) although each calls the other.
	assertEqual(t, "changed", ids(r.Changed), []string{"A", "B"})
	assertEqual(t, "impacted", summary(r), []string{"P d1 via A"})
	// P calls A, which calls the changed B: both are roots of P.
	assertEqual(t, "roots of P", r.Lookup("P").Roots, []string{"A", "B"})
}

func TestAddedFunctionWithoutCallers(t *testing.T) {
	g := buildGraph("N -> Old", "Other -> Old")
	r := Analyze([]changes.FunctionChange{change(t, g, changes.Added, "N")}, g, g)
	assertEqual(t, "impacted", summary(r), nil)
	assertEqual(t, "changed", ids(r.Changed), []string{"N"})
}

func TestDeletedFunction(t *testing.T) {
	base := buildGraph("P -> D", "Q -> P", "Gone -> D", "Lost -> D", "Unrelated")
	// In head, D is gone, Gone was deleted too, and Lost disappeared
	// without being reported as changed (say its file stopped parsing).
	head := buildGraph("P", "Q -> P", "Unrelated")
	changed := []changes.FunctionChange{
		change(t, base, changes.Deleted, "D"),
		change(t, base, changes.Deleted, "Gone"),
	}
	r := Analyze(changed, base, head)

	assertEqual(t, "changed", ids(r.Changed), []string{"D", "Gone"})
	assertEqual(t, "impacted", summary(r), []string{"P d1 via D", "Q d2 via P"})
	if r.Lookup("P").Function != head.Function("P") {
		t.Error("P should be the head version of the function")
	}
	assertEqual(t, "unmapped", r.Unmapped, []string{"Lost"})
	assertEqual(t, "path", r.Path("Q"), []string{"Q", "P", "D"})
}

func TestUnrelatedFunctions(t *testing.T) {
	g := buildGraph("A -> C", "U -> V", "V -> W", "C -> W")
	r := Analyze(modified(t, g, "C"), g, g)
	// U, V and W do not call C. W is called by C, which does not matter.
	assertEqual(t, "impacted", summary(r), []string{"A d1 via C"})
	for _, id := range []string{"U", "V", "W"} {
		if r.Lookup(id) != nil {
			t.Errorf("%s reported", id)
		}
	}
}

func TestDuplicatePathsToSameCaller(t *testing.T) {
	g := buildGraph("A -> B", "A -> C", "B -> C", "A -> C")
	r := Analyze(modified(t, g, "C"), g, g)
	// A calls C directly (twice) and through B: distance 1, reported once.
	assertEqual(t, "impacted", summary(r), []string{"A d1 via C", "B d1 via C"})
}

func TestOnlyInternalCallsPropagate(t *testing.T) {
	fns := []*model.Function{
		{ID: "C", Name: "C", File: "c.go"},
		{ID: "Ext", Name: "Ext", File: "ext.go", Calls: []model.Call{{Callee: "C", Kind: model.CallExternal}}},
		{ID: "Unres", Name: "Unres", File: "unres.go", Calls: []model.Call{{Callee: "C", Kind: model.CallUnresolved}}},
	}
	g := graph.Build(&model.Repository{Packages: []*model.Package{{Files: []*model.File{{Functions: fns}}}}})
	r := Analyze(modified(t, g, "C"), g, g)
	assertEqual(t, "impacted", summary(r), nil)
}

func TestDeterministic(t *testing.T) {
	edges := []string{"A -> B", "A -> C", "B -> D", "C -> D", "E -> D", "F -> E", "F -> B", "G -> F", "D -> G"}
	g := buildGraph(edges...)
	first := Analyze(modified(t, g, "D", "C"), g, g)
	for i := 0; i < 20; i++ {
		slices.Reverse(edges)
		g := buildGraph(edges...)
		r := Analyze(modified(t, g, "C", "D"), g, g)
		assertEqual(t, "summary", summary(r), summary(first))
		for _, imp := range r.Impacted {
			assertEqual(t, "roots of "+imp.ID(), imp.Roots, first.Lookup(imp.ID()).Roots)
		}
	}
}

func TestEmptyInput(t *testing.T) {
	r := Analyze(nil, nil, nil)
	if len(r.Changed)+len(r.Impacted)+len(r.Unmapped) != 0 || r.Path("X") != nil {
		t.Errorf("expected an empty result, got %+v", r)
	}
	if r := FromReport(&changes.Report{}); len(r.Changed) != 0 {
		t.Errorf("FromReport without analyses = %+v", r)
	}
}

func ids(imps []*Impact) []string {
	var out []string
	for _, imp := range imps {
		out = append(out, imp.ID())
	}
	return out
}
