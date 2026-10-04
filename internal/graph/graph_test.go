package graph

import (
	"slices"
	"testing"

	"github.com/Easlkad/impact/internal/model"
)

func TestBuild(t *testing.T) {
	a := &model.Function{ID: "app.A", Calls: []model.Call{
		{Callee: "app.C", Kind: model.CallInternal},
		{Callee: "app.B", Kind: model.CallInternal},
		{Callee: "app.B", Kind: model.CallInternal}, // repeated call: one edge
		{Callee: "fmt.Println", Kind: model.CallExternal},
		{Callee: "hook", Kind: model.CallUnresolved},
		{Callee: "app.Missing", Kind: model.CallInternal}, // not in the repository
	}}
	b := &model.Function{ID: "app.B", Calls: []model.Call{
		{Callee: "app.C", Kind: model.CallInternal},
	}}
	c := &model.Function{ID: "app.C"}
	repo := &model.Repository{Packages: []*model.Package{{
		ID:    "app",
		Files: []*model.File{{Functions: []*model.Function{a, b}}, {Functions: []*model.Function{c}}},
	}}}

	g := Build(repo)

	wantEdges := []Edge{{"app.A", "app.B"}, {"app.A", "app.C"}, {"app.B", "app.C"}}
	if got := g.Edges(); !slices.Equal(got, wantEdges) {
		t.Errorf("Edges() = %v, want %v", got, wantEdges)
	}

	tests := []struct {
		name string
		got  []string
		want []string
	}{
		{"Callees(A)", g.Callees("app.A"), []string{"app.B", "app.C"}},
		{"Callees(C)", g.Callees("app.C"), nil},
		{"Callers(C)", g.Callers("app.C"), []string{"app.A", "app.B"}},
		{"Callers(A)", g.Callers("app.A"), nil},
		{"Callers(unknown)", g.Callers("nope"), nil},
	}
	for _, tt := range tests {
		if !slices.Equal(tt.got, tt.want) {
			t.Errorf("%s = %v, want %v", tt.name, tt.got, tt.want)
		}
	}

	if g.Function("app.B") != b || g.Function("nope") != nil {
		t.Error("Function lookup returned the wrong function")
	}
}

func TestQueriesReturnCopies(t *testing.T) {
	repo := &model.Repository{Packages: []*model.Package{{Files: []*model.File{{Functions: []*model.Function{
		{ID: "A", Calls: []model.Call{{Callee: "B", Kind: model.CallInternal}}},
		{ID: "B"},
	}}}}}}
	g := Build(repo)

	g.Callees("A")[0] = "changed"
	g.Edges()[0].To = "changed"
	if g.Callees("A")[0] != "B" || g.Edges()[0].To != "B" {
		t.Error("modifying a returned slice changed the graph")
	}
}
