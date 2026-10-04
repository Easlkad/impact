package classify

import (
	"fmt"
	"reflect"
	"testing"
	"testing/fstest"

	"github.com/Easlkad/impact/internal/analyzer/golang"
	"github.com/Easlkad/impact/internal/changes"
	"github.com/Easlkad/impact/internal/graph"
	"github.com/Easlkad/impact/internal/impact"
	"github.com/Easlkad/impact/internal/model"
)

const goMod = "module example.com/app\n\ngo 1.22\n"

func analyzeFiles(t *testing.T, files map[string]string) *model.Repository {
	t.Helper()
	fsys := fstest.MapFS{"go.mod": {Data: []byte(goMod)}}
	for name, src := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(src)}
	}
	repo, err := golang.AnalyzeFS(fsys, "app", golang.Options{IncludeTests: true})
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

// classifyModified analyzes files, marks the functions with the given IDs
// (relative to example.com/app/) as modified, and classifies the impact.
func classifyModified(t *testing.T, files map[string]string, modified ...string) *Result {
	t.Helper()
	repo := analyzeFiles(t, files)
	g := graph.Build(repo)
	var changed []changes.FunctionChange
	for _, id := range modified {
		fn := g.Function("example.com/app/" + id)
		if fn == nil {
			t.Fatalf("no function %s", id)
		}
		changed = append(changed, changes.FunctionChange{Kind: changes.Modified, Function: fn})
	}
	return Classify(impact.Analyze(changed, g, g), repo, repo)
}

func label(imp *impact.Impact) string {
	switch imp.Distance {
	case 0:
		return "changed"
	case 1:
		return "direct"
	}
	return fmt.Sprintf("distance %d", imp.Distance)
}

func assertEqual(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s:\n got: %q\nwant: %q", what, got, want)
	}
}

func TestPackages(t *testing.T) {
	c := classifyModified(t, map[string]string{
		"a/a.go": `package a

func Changed() {}
func SamePackageCaller() { Changed() }
func AnotherCaller() { Changed() }
`,
		"a/a_test.go": `package a

import "testing"

func TestChanged(t *testing.T) { Changed() }
`,
		"b/b.go": `package b

import "example.com/app/a"

func Direct() { a.Changed() }
func Transitive() { a.SamePackageCaller() }
func Unrelated() {}
`,
		"c/c.go": "package c\n\nfunc Untouched() {}\n",
	}, "a.Changed")

	var got []string
	for _, p := range c.Packages {
		got = append(got, fmt.Sprintf("%s changed=%d direct=%d transitive=%d", p.Package.Dir, p.Changed, p.Direct, p.Transitive))
	}
	// TestChanged is counted as a test, not in package a; c is unaffected.
	assertEqual(t, "packages", got, []string{
		"a changed=1 direct=2 transitive=0",
		"b changed=0 direct=1 transitive=1",
	})
}

func endpointLines(c *Result) []string {
	var out []string
	for _, e := range c.Endpoints {
		out = append(out, fmt.Sprintf("%s %s -> %s (%s)", e.Method, e.Path, e.Handler.Function.QualifiedName(), label(e.Handler)))
	}
	return out
}

func TestEndpoints(t *testing.T) {
	c := classifyModified(t, map[string]string{
		"store/store.go": "package store\n\nfunc Save() {}\n",
		"api/api.go": `package api

import (
	"net/http"

	"example.com/app/store"
)

type Status struct{}

func (Status) ServeHTTP(w http.ResponseWriter, r *http.Request) { helper() }

func Payments(w http.ResponseWriter, r *http.Request) { store.Save() }
func Users(w http.ResponseWriter, r *http.Request)    { helper() }
func Orders(w http.ResponseWriter, r *http.Request)   {}
func Health(w http.ResponseWriter, r *http.Request)   {}
func helper()                                         { store.Save() }

func Routes(dynamic string) {
	http.HandleFunc("/payments", Payments)
	http.Handle("/status", Status{})
	http.NewServeMux().HandleFunc("GET /users", Users)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /orders", Orders)
	mux.HandleFunc("/health", Health)
	mux.HandleFunc(dynamic, Payments)
}
`,
	}, "store.Save", "api.Orders")

	// Health is unaffected; the dynamic route is not detected at all.
	assertEqual(t, "endpoints", endpointLines(c), []string{
		"POST /orders -> Orders (changed)",
		"ANY /payments -> Payments (direct)",
		"ANY /status -> Status.ServeHTTP (distance 2)",
		"GET /users -> Users (distance 2)",
	})
}

func TestEndpointOfDeletedHandler(t *testing.T) {
	baseSrc := `package api

import "net/http"

func Legacy(w http.ResponseWriter, r *http.Request) {}

func Routes() {
	http.HandleFunc("DELETE /legacy", Legacy)
}
`
	headSrc := "package api\n\nfunc Routes() {}\n"
	base := analyzeFiles(t, map[string]string{"api/api.go": baseSrc})
	head := analyzeFiles(t, map[string]string{"api/api.go": headSrc})
	bg, hg := graph.Build(base), graph.Build(head)
	changed := []changes.FunctionChange{
		{Kind: changes.Deleted, Function: bg.Function("example.com/app/api.Legacy")},
		{Kind: changes.Modified, Function: hg.Function("example.com/app/api.Routes")},
	}
	c := Classify(impact.Analyze(changed, bg, hg), base, head)
	assertEqual(t, "endpoints", endpointLines(c), []string{"DELETE /legacy -> Legacy (changed)"})
	if c.Endpoints[0].Handler.Change != changes.Deleted {
		t.Errorf("handler change = %q, want deleted", c.Endpoints[0].Handler.Change)
	}
}

func TestWorkers(t *testing.T) {
	c := classifyModified(t, map[string]string{
		"jobs/jobs.go": `package jobs

type Service struct{}

func (s *Service) Run() { Core() }

func Core()      {}
func RunWorker() { Core() }
func drain()     { Core() }
func Process()   { Core() }
func Idle()      {}

func Start(svc *Service) {
	go RunWorker()
	go svc.Run()
	go func() {
		drain()
	}()
	go Idle()
	Process()
}
`,
	}, "jobs.Core")

	var got []string
	for _, w := range c.Workers {
		got = append(got, fmt.Sprintf("%s %s (%s) started by %v", w.Function.Function.QualifiedName(), w.Kind, label(w.Function), w.StartedBy))
	}
	// Process is only called, and Idle is not affected.
	assertEqual(t, "workers", got, []string{
		"RunWorker goroutine (direct) started by [example.com/app/jobs.Start]",
		"Service.Run goroutine (direct) started by [example.com/app/jobs.Start]",
		"drain background (direct) started by [example.com/app/jobs.Start]",
	})
}

func TestTests(t *testing.T) {
	c := classifyModified(t, map[string]string{
		"calc/calc.go": "package calc\n\nfunc Add(a, b int) int { return a + b }\n",
		"calc/calc_test.go": `package calc

import "testing"

func TestAdd(t *testing.T)          { Add(1, 2) }
func BenchmarkAdd(b *testing.B)     { Add(1, 2) }
func FuzzAdd(f *testing.F)          { Add(1, 2) }
func ExampleAdd()                   { Add(1, 2) }
func TestChanged(t *testing.T)      {}
func TestIndirect(t *testing.T)     { check(t) }
func check(t *testing.T)            { Add(0, 0) }
`,
	}, "calc.Add", "calc.TestChanged")

	var tests []string
	for _, tt := range c.Tests {
		tests = append(tests, fmt.Sprintf("%s %s (%s)", tt.Kind, tt.Function.Function.Name, label(tt.Function)))
	}
	assertEqual(t, "tests", tests, []string{
		"test TestChanged (changed)",
		"benchmark BenchmarkAdd (direct)",
		"example ExampleAdd (direct)",
		"fuzz FuzzAdd (direct)",
		"test TestAdd (direct)",
		"test TestIndirect (distance 2)",
	})

	// check lives in a _test.go file but is not a test: it stays with the
	// regular impacts. Tests are kept out of Direct and Transitive.
	var direct []string
	for _, imp := range c.Direct {
		direct = append(direct, imp.Function.Name)
	}
	assertEqual(t, "direct", direct, []string{"check"})
	if len(c.Transitive) != 0 {
		t.Errorf("transitive = %d functions, want none", len(c.Transitive))
	}
	if len(c.Changed) != 2 {
		t.Errorf("changed = %d functions, want 2 (tests included)", len(c.Changed))
	}
}

func TestEmpty(t *testing.T) {
	c := Classify(impact.Analyze(nil, nil, nil), nil, nil)
	if len(c.Packages)+len(c.Endpoints)+len(c.Workers)+len(c.Tests) != 0 {
		t.Errorf("expected an empty result, got %+v", c)
	}
}
