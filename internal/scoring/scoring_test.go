package scoring

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Easlkad/impact/internal/analyzer/golang"
	"github.com/Easlkad/impact/internal/changes"
	"github.com/Easlkad/impact/internal/classify"
	"github.com/Easlkad/impact/internal/graph"
	"github.com/Easlkad/impact/internal/impact"
	"github.com/Easlkad/impact/internal/model"
)

const prefix = "example.com/app/"

// scenario describes a comparison of two commits in terms of their sources
// and the changed functions, as Phase 2 would report them.
type scenario struct {
	head     map[string]string // sources of the head commit
	base     map[string]string // sources of the base commit; head when nil
	modified []string          // IDs relative to example.com/app/, in head
	deleted  []string          // IDs relative to example.com/app/, in base
	// packageLevel is the number of package-level changes to report.
	packageLevel int
}

func analyzeSources(t *testing.T, files map[string]string) *model.Repository {
	t.Helper()
	fsys := fstest.MapFS{"go.mod": {Data: []byte("module example.com/app\n\ngo 1.22\n")}}
	for name, src := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(src)}
	}
	repo, err := golang.AnalyzeFS(fsys, "app", golang.Options{IncludeTests: true})
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func (s scenario) assess(t *testing.T) Assessment {
	t.Helper()
	head := analyzeSources(t, s.head)
	base := head
	if s.base != nil {
		base = analyzeSources(t, s.base)
	}
	bg, hg := graph.Build(base), graph.Build(head)

	report := &changes.Report{BaseAnalysis: base, HeadAnalysis: head}
	for _, id := range s.modified {
		fn := hg.Function(prefix + id)
		if fn == nil {
			t.Fatalf("no function %s in head", id)
		}
		report.Functions = append(report.Functions, changes.FunctionChange{Kind: changes.Modified, Function: fn})
	}
	for _, id := range s.deleted {
		fn := bg.Function(prefix + id)
		if fn == nil {
			t.Fatalf("no function %s in base", id)
		}
		report.Functions = append(report.Functions, changes.FunctionChange{Kind: changes.Deleted, Function: fn})
	}
	for i := 0; i < s.packageLevel; i++ {
		report.PackageLevel = append(report.PackageLevel, changes.LineChange{File: "x.go", Start: i + 1, End: i + 1})
	}

	r := impact.Analyze(report.Functions, bg, hg)
	return Assess(Input{Changes: report, Impact: r, Context: classify.Classify(r, base, head)})
}

// with returns a copy of files with more files added.
func with(files map[string]string, more map[string]string) map[string]string {
	out := make(map[string]string, len(files)+len(more))
	for k, v := range files {
		out[k] = v
	}
	for k, v := range more {
		out[k] = v
	}
	return out
}

// factorDeltas formats the factors of a score as "name delta".
func factorDeltas(s Score) []string {
	var out []string
	for _, f := range s.Factors {
		out = append(out, fmt.Sprintf("%s %+d", f.Name, f.Delta))
	}
	return out
}

func factorNamed(s Score, name string) Factor {
	for _, f := range s.Factors {
		if f.Name == name {
			return f
		}
	}
	return Factor{}
}

func assertFactors(t *testing.T, what string, s Score, want ...string) {
	t.Helper()
	if got := factorDeltas(s); !reflect.DeepEqual(got, want) {
		t.Errorf("%s factors:\n got: %q\nwant: %q", what, got, want)
	}
}

// A store package with one private function, used by the risk scenarios.
var store = map[string]string{
	"store/store.go": `package store

func Save() { write() }

func write() {}
`,
}

// --- Risk ---

func TestRiskIsolatedPrivateChangeIsLow(t *testing.T) {
	a := scenario{head: map[string]string{"util/util.go": "package util\n\nfunc helper() {}\n"}, modified: []string{"util.helper"}}.assess(t)
	if a.Risk.Value != 4 || a.Risk.Level != "LOW" {
		t.Errorf("risk = %d %s, want 4 LOW", a.Risk.Value, a.Risk.Level)
	}
	assertFactors(t, "risk", a.Risk, "changed-functions +4")
}

func TestRiskEndpointIncreasesRisk(t *testing.T) {
	handler := map[string]string{"api/api.go": `package api

import (
	"net/http"

	"example.com/app/store"
)

func Pay(w http.ResponseWriter, r *http.Request) { store.Save() }
`}
	routes := map[string]string{"api/routes.go": `package api

import "net/http"

func Routes() { http.HandleFunc("POST /payments", Pay) }
`}
	without := scenario{head: with(store, handler), modified: []string{"store.write"}}.assess(t)
	withEndpoint := scenario{head: with(with(store, handler), routes), modified: []string{"store.write"}}.assess(t)

	if diff := withEndpoint.Risk.Value - without.Risk.Value; diff != RiskEndpoints.First {
		t.Errorf("endpoint added %d points, want %d", diff, RiskEndpoints.First)
	}
	if f := factorNamed(withEndpoint.Risk, "endpoints"); f.Reason != "affects 1 endpoint: POST /payments" {
		t.Errorf("endpoints factor = %+v", f)
	}
}

func TestRiskWorkerIncreasesRisk(t *testing.T) {
	src := func(start string) map[string]string {
		return with(store, map[string]string{"jobs/jobs.go": `package jobs

import "example.com/app/store"

func Run() { store.Save() }

func Start() { ` + start + ` }
`})
	}
	called := scenario{head: src("Run()"), modified: []string{"store.write"}}.assess(t)
	started := scenario{head: src("go Run()"), modified: []string{"store.write"}}.assess(t)
	if diff := started.Risk.Value - called.Risk.Value; diff != RiskWorkers.First {
		t.Errorf("worker added %d points, want %d", diff, RiskWorkers.First)
	}
}

func TestRiskMultiplePackagesIncreaseRisk(t *testing.T) {
	caller := func(pkg string) map[string]string {
		return map[string]string{pkg + "/" + pkg + ".go": "package " + pkg + "\n\nimport \"example.com/app/store\"\n\nfunc Use() { store.Save() }\n"}
	}
	one := scenario{head: with(store, caller("a")), modified: []string{"store.Save"}}.assess(t)
	three := scenario{head: with(with(with(store, caller("a")), caller("b")), caller("c")), modified: []string{"store.Save"}}.assess(t)

	// Two more callers (2 points each) in two more packages (4 points each).
	if diff := three.Risk.Value - one.Risk.Value; diff != 2*RiskImpactedFunctions.Each+RiskExtraPackages.Points(3)-RiskExtraPackages.Points(1) {
		t.Errorf("two more packages added %d points", diff)
	}
	if !strings.Contains(fmt.Sprint(three.Risk.Factors), "impact reaches 4 packages") {
		t.Errorf("factors = %+v", three.Risk.Factors)
	}
}

func TestRiskDeeperImpactIncreasesRisk(t *testing.T) {
	chain := func(n int) map[string]string {
		src := "package chain\n\nfunc F0() {}\n"
		for i := 1; i <= n; i++ {
			src += fmt.Sprintf("func F%d() { F%d() }\n", i, i-1)
		}
		return map[string]string{"chain/chain.go": src}
	}
	shallow := scenario{head: chain(1), modified: []string{"chain.F0"}}.assess(t)
	deep := scenario{head: chain(4), modified: []string{"chain.F0"}}.assess(t)
	assertFactors(t, "shallow", shallow.Risk, "changed-functions +4", "exported-change +5", "impacted-functions +2")
	assertFactors(t, "deep", deep.Risk, "changed-functions +4", "exported-change +5", "impacted-functions +8", "depth +12")
}

func TestRiskDeletedFunctionIncreasesRisk(t *testing.T) {
	base := map[string]string{"util/util.go": "package util\n\nfunc keep() {}\n\nfunc legacy() {}\n"}
	head := map[string]string{"util/util.go": "package util\n\nfunc keep() {}\n"}
	modified := scenario{head: base, modified: []string{"util.legacy"}}.assess(t)
	deleted := scenario{base: base, head: head, deleted: []string{"util.legacy"}}.assess(t)
	if diff := deleted.Risk.Value - modified.Risk.Value; diff != RiskDeletedFunctions.First {
		t.Errorf("deletion added %d points, want %d", diff, RiskDeletedFunctions.First)
	}
}

func TestRiskMultipleChangedFunctionsIncreaseRisk(t *testing.T) {
	src := map[string]string{"util/util.go": "package util\n\nfunc a() {}\nfunc b() {}\nfunc c() {}\n"}
	one := scenario{head: src, modified: []string{"util.a"}}.assess(t)
	three := scenario{head: src, modified: []string{"util.a", "util.b", "util.c"}}.assess(t)
	if one.Risk.Value != 4 || three.Risk.Value != 12 {
		t.Errorf("risk for 1 and 3 changed functions = %d, %d; want 4, 12", one.Risk.Value, three.Risk.Value)
	}
}

func TestRiskAffectedTestsReduceRiskSlightly(t *testing.T) {
	test := map[string]string{"store/store_test.go": `package store

import "testing"

func TestSave(t *testing.T) { Save() }
`}
	without := scenario{head: store, modified: []string{"store.write"}}.assess(t)
	withTest := scenario{head: with(store, test), modified: []string{"store.write"}}.assess(t)
	if diff := withTest.Risk.Value - without.Risk.Value; diff != -RiskAffectedTests.First {
		t.Errorf("tests changed risk by %d, want %d", diff, -RiskAffectedTests.First)
	}
}

// largeImpact is a change of one exported function used by six packages,
// each serving an endpoint and starting a worker.
func largeImpact() map[string]string {
	files := map[string]string{"core/core.go": "package core\n\nfunc Do() {}\n"}
	for i := 0; i < 6; i++ {
		files[fmt.Sprintf("p%d/p.go", i)] = fmt.Sprintf(`package p%d

import (
	"net/http"

	"example.com/app/core"
)

func Handle(w http.ResponseWriter, r *http.Request) { core.Do() }

func Run() { Handle(nil, nil) }

func Start() { go Run() }

func Routes() { http.HandleFunc("/p%d", Handle) }
`, i, i)
	}
	return files
}

func TestRiskClampsTo100(t *testing.T) {
	a := scenario{head: largeImpact(), modified: []string{"core.Do"}}.assess(t)
	sum := 0
	for _, f := range a.Risk.Factors {
		sum += f.Delta
	}
	if sum <= 100 || a.Risk.Value != 100 || a.Risk.Level != "CRITICAL" {
		t.Errorf("risk = %d %s from factors summing to %d; want 100 CRITICAL from more than 100", a.Risk.Value, a.Risk.Level, sum)
	}
}

func TestDeterministic(t *testing.T) {
	s := scenario{head: largeImpact(), modified: []string{"core.Do"}}
	first := s.assess(t)
	for i := 0; i < 5; i++ {
		if got := s.assess(t); !reflect.DeepEqual(got, first) {
			t.Fatalf("assessment %d differs:\n%+v\n%+v", i, got, first)
		}
	}
}

// --- Confidence ---

func TestConfidenceCleanGraphIsHigh(t *testing.T) {
	a := scenario{head: largeImpact(), modified: []string{"core.Do"}}.assess(t)
	if a.Confidence.Value != 100 || a.Confidence.Level != "HIGH" || len(a.Confidence.Factors) != 0 {
		t.Errorf("confidence = %+v, want 100 HIGH without factors", a.Confidence)
	}
}

// repoWithBlindSpots has a changed method Repo.Save that may be called
// through an interface, through receivers of unknown type, and code calling
// function values in the same package.
var repoWithBlindSpots = map[string]string{
	"db/db.go": `package db

type Repo struct{}

func (r *Repo) Save() {}

type Saver interface{ Save() }

func getSaver() any { return nil }

func Flush(s Saver, hooks []func()) {
	s.Save()
	getSaver().(interface{ Save() }).Save()
	for _, h := range hooks {
		h()
	}
}
`,
}

func TestConfidenceUnresolvedCallsReduceConfidence(t *testing.T) {
	a := scenario{head: repoWithBlindSpots, modified: []string{"db.Repo.Save"}}.assess(t)
	assertFactors(t, "confidence", a.Confidence, "interface-calls -4", "unknown-receiver-calls -3", "function-values -2")
	if a.Confidence.Value != 91 {
		t.Errorf("confidence = %d, want 91", a.Confidence.Value)
	}
	if got := a.Confidence.Factors[0].Reason; !strings.Contains(got, "(Save)") {
		t.Errorf("interface factor reason = %q", got)
	}
}

func TestConfidenceUnmappedDeletedCallersReduceConfidence(t *testing.T) {
	base := map[string]string{"util/util.go": `package util

func Legacy() {}

func Lost() { Legacy() }
`}
	// In head, Legacy is deleted and Lost disappeared without being
	// reported (as if its declaration could not be matched).
	head := map[string]string{"util/util.go": "package util\n"}
	a := scenario{base: base, head: head, deleted: []string{"util.Legacy"}}.assess(t)
	assertFactors(t, "confidence", a.Confidence, "unmapped-callers -8")
}

func TestConfidenceDeletedMappingReducesConfidence(t *testing.T) {
	base := map[string]string{"util/util.go": "package util\n\nfunc Legacy() {}\n\nfunc Caller() { Legacy() }\n"}
	head := map[string]string{"util/util.go": "package util\n\nfunc Caller() { Legacy() }\n"}
	a := scenario{base: base, head: head, deleted: []string{"util.Legacy"}}.assess(t)
	assertFactors(t, "confidence", a.Confidence, "deleted-mapping -5")
}

func TestConfidenceParseWarningReducesConfidence(t *testing.T) {
	head := with(store, map[string]string{"broken/broken.go": "package broken\n\nfunc Broken( {\n"})
	a := scenario{head: head, modified: []string{"store.write"}}.assess(t)
	assertFactors(t, "confidence", a.Confidence, "analysis-warnings -10")
}

func TestConfidenceIncompleteRouteReducesConfidence(t *testing.T) {
	head := with(store, map[string]string{"api/api.go": `package api

import (
	"net/http"
	"os"

	"example.com/app/store"
)

func Pay(w http.ResponseWriter, r *http.Request) { store.Save() }

func Routes() {
	http.HandleFunc(os.Getenv("PATH_PAY"), Pay)
	http.HandleFunc("/inline", func(w http.ResponseWriter, r *http.Request) {})
}
`})
	// Only the dynamic pattern concerns affected code: Routes itself is not
	// affected, so its inline handler does not count.
	a := scenario{head: head, modified: []string{"store.write"}}.assess(t)
	assertFactors(t, "confidence", a.Confidence, "incomplete-routes -5")
}

func TestConfidenceSignalsStack(t *testing.T) {
	head := with(repoWithBlindSpots, map[string]string{"broken/broken.go": "package broken\n\nfunc (\n"})
	a := scenario{head: head, modified: []string{"db.Repo.Save"}, packageLevel: 2}.assess(t)
	assertFactors(t, "confidence", a.Confidence,
		"analysis-warnings -10", "interface-calls -4", "unknown-receiver-calls -3", "function-values -2", "package-level -8")
	if a.Confidence.Value != 100-10-4-3-2-8 || a.Confidence.Level != "HIGH" {
		t.Errorf("confidence = %d %s, want 73 HIGH", a.Confidence.Value, a.Confidence.Level)
	}
}

func TestScoreClamps(t *testing.T) {
	low := factors{{Delta: -60}, {Delta: -70}}.score(ConfidenceStart, ConfidenceLevels)
	if low.Value != 0 || low.Level != "LOW" {
		t.Errorf("confidence = %d %s, want 0 LOW", low.Value, low.Level)
	}
	high := factors{{Delta: 80}, {Delta: 50}}.score(0, RiskLevels)
	if high.Value != 100 || high.Level != "CRITICAL" {
		t.Errorf("risk = %d %s, want 100 CRITICAL", high.Value, high.Level)
	}
}

func TestConfidenceClampsTo0(t *testing.T) {
	// Every blind spot at once: confidence cannot go below 0.
	files := with(repoWithBlindSpots, map[string]string{
		"b1/b.go": "package b1\n\nfunc (\n",
		"b2/b.go": "package b2\n\nfunc (\n",
		"b3/b.go": "package b3\n\nfunc (\n",
	})
	files["db/more.go"] = `package db

import (
	"net/http"
	"os"
)

func getAny() any { return nil }

func More(s Saver, fs []func()) {
	s.Save(); s.Save(); s.Save(); s.Save(); s.Save()
	getAny().(interface{ Save() }).Save(); getAny().(interface{ Save() }).Save()
	getAny().(interface{ Save() }).Save(); getAny().(interface{ Save() }).Save()
	fs[0](); fs[1](); fs[2](); fs[3](); fs[4]()
}

func Handle(w http.ResponseWriter, r *http.Request) { (&Repo{}).Save() }

func Routes() {
	http.HandleFunc(os.Getenv("A"), Handle)
	http.HandleFunc(os.Getenv("B"), Handle)
	http.HandleFunc(os.Getenv("C"), Handle)
}
`
	a := scenario{head: files, modified: []string{"db.Repo.Save"}, packageLevel: 5}.assess(t)
	sum := 0
	for _, f := range a.Confidence.Factors {
		sum += f.Delta
	}
	if sum >= -100 {
		t.Fatalf("penalties sum to %d, the scenario must exceed 100: %v", sum, factorDeltas(a.Confidence))
	}
	if a.Confidence.Value != 0 || a.Confidence.Level != "LOW" {
		t.Errorf("confidence = %d %s, want 0 LOW; factors %v", a.Confidence.Value, a.Confidence.Level, factorDeltas(a.Confidence))
	}
}

func TestRepositorySizeAloneDoesNotChangeScores(t *testing.T) {
	small := scenario{head: repoWithBlindSpots, modified: []string{"db.Repo.Save"}}.assess(t)

	// Fifty unrelated packages, with resolved calls and with unresolved
	// calls of their own (to other method names, through function values).
	big := with(repoWithBlindSpots, nil)
	for i := 0; i < 50; i++ {
		big[fmt.Sprintf("other%d/o.go", i)] = fmt.Sprintf(`package other%d

type Loader interface{ Load() }

func A() { B() }
func B() {}
func C(l Loader, f func()) { l.Load(); f() }
`, i)
	}
	large := scenario{head: big, modified: []string{"db.Repo.Save"}}.assess(t)
	if !reflect.DeepEqual(large, small) {
		t.Errorf("a larger repository changed the assessment:\nsmall: %+v\nlarge: %+v", small, large)
	}
}

func TestIndependentScores(t *testing.T) {
	// High risk with low confidence is a valid outcome.
	files := with(largeImpact(), map[string]string{
		"b1/b.go": "package b1\n\nfunc (\n",
		"b2/b.go": "package b2\n\nfunc (\n",
		"b3/b.go": "package b3\n\nfunc (\n",
	})
	a := scenario{head: files, modified: []string{"core.Do"}, packageLevel: 6}.assess(t)
	if a.Risk.Level != "CRITICAL" || a.Confidence.Level != "MEDIUM" {
		t.Errorf("risk %s, confidence %s (%d); want CRITICAL and MEDIUM", a.Risk.Level, a.Confidence.Level, a.Confidence.Value)
	}
}

func TestWeightPoints(t *testing.T) {
	w := Weight{First: 15, Each: 5, Max: 30}
	for n, want := range map[int]int{-1: 0, 0: 0, 1: 15, 2: 20, 4: 30, 10: 30} {
		if got := w.Points(n); got != want {
			t.Errorf("Points(%d) = %d, want %d", n, got, want)
		}
	}
}

func TestLevels(t *testing.T) {
	for _, tt := range []struct {
		value  int
		levels []Level
		want   string
	}{
		{0, RiskLevels, "LOW"}, {24, RiskLevels, "LOW"}, {25, RiskLevels, "MODERATE"}, {49, RiskLevels, "MODERATE"},
		{50, RiskLevels, "HIGH"}, {74, RiskLevels, "HIGH"}, {75, RiskLevels, "CRITICAL"}, {100, RiskLevels, "CRITICAL"},
		{39, ConfidenceLevels, "LOW"}, {40, ConfidenceLevels, "MEDIUM"}, {69, ConfidenceLevels, "MEDIUM"}, {70, ConfidenceLevels, "HIGH"},
	} {
		if got := (factors{{Delta: tt.value}}).score(0, tt.levels).Level; got != tt.want {
			t.Errorf("level of %d = %s, want %s", tt.value, got, tt.want)
		}
	}
}
