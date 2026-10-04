package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Easlkad/impact/internal/analyzer/golang"
	"github.com/Easlkad/impact/internal/changes"
	"github.com/Easlkad/impact/internal/classify"
	"github.com/Easlkad/impact/internal/gitdiff"
	"github.com/Easlkad/impact/internal/graph"
	"github.com/Easlkad/impact/internal/impact"
	"github.com/Easlkad/impact/internal/scoring"
)

// shop is a small service: core.Charge is called by an HTTP handler (via a
// helper), a goroutine worker and a test.
var shop = map[string]string{
	"core/core.go": "package core\n\nfunc Charge() {}\n",
	"core/core_test.go": `package core

import "testing"

func TestCharge(t *testing.T) { Charge() }
`,
	"api/api.go": `package api

import (
	"net/http"

	"example.com/app/core"
)

func Pay(w http.ResponseWriter, r *http.Request) { process() }

func process() { core.Charge() }

func Routes() { http.HandleFunc("POST /pay", Pay) }
`,
	"jobs/jobs.go": `package jobs

import "example.com/app/core"

func Run() { core.Charge() }

func Start() { go Run() }
`,
}

// build runs the analysis pipeline on in-memory sources, with the given
// functions (IDs relative to example.com/app/) modified, and builds the
// report.
func build(t *testing.T, files map[string]string, modified ...string) *Report {
	t.Helper()
	fsys := fstest.MapFS{"go.mod": {Data: []byte("module example.com/app\n\ngo 1.22\n")}}
	for name, src := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(src)}
	}
	repo, err := golang.AnalyzeFS(fsys, "app", golang.Options{IncludeTests: true})
	if err != nil {
		t.Fatal(err)
	}
	g := graph.Build(repo)
	ch := &changes.Report{
		Base: changes.Commit{Ref: "main", Hash: "1111111111111111111111111111111111111111"},
		Head: changes.Commit{Ref: "HEAD", Hash: "2222222222222222222222222222222222222222"},
	}
	if len(modified) > 0 {
		ch.BaseAnalysis, ch.HeadAnalysis = repo, repo
	}
	for _, id := range modified {
		fn := g.Function("example.com/app/" + id)
		if fn == nil {
			t.Fatalf("no function %s", id)
		}
		ch.Functions = append(ch.Functions, changes.FunctionChange{Kind: changes.Modified, Function: fn})
		ch.Files = append(ch.Files, gitdiff.File{OldPath: fn.File, NewPath: fn.File, Status: gitdiff.Modified})
	}
	r := impact.FromReport(ch)
	c := classify.Classify(r, ch.BaseAnalysis, ch.HeadAnalysis)
	return Build(Input{
		Changes:    ch,
		Impact:     r,
		Context:    c,
		Assessment: scoring.Assess(scoring.Input{Changes: ch, Impact: r, Context: c}),
		Version:    "test",
	})
}

func render(t *testing.T, write func(*bytes.Buffer) error) string {
	t.Helper()
	var buf bytes.Buffer
	if err := write(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func keys(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func TestJSONSchema(t *testing.T) {
	r := build(t, shop, "core.Charge")
	var doc map[string]any
	if err := json.Unmarshal([]byte(render(t, func(b *bytes.Buffer) error { return WriteJSON(b, r) })), &doc); err != nil {
		t.Fatal(err)
	}

	// The top-level fields are the contract with JSON consumers.
	want := []string{
		"base", "changed", "changedFiles", "confidence", "directImpact", "endpoints", "head", "mergeBase",
		"packageLevelChanges", "packages", "risk", "schemaVersion", "summary", "tests", "tool",
		"transitiveImpact", "unmappedCallers", "warnings", "workers",
	}
	if got := keys(doc); !reflect.DeepEqual(got, want) {
		t.Errorf("top-level keys:\n got: %q\nwant: %q", got, want)
	}
	if doc["schemaVersion"] != float64(SchemaVersion) {
		t.Errorf("schemaVersion = %v", doc["schemaVersion"])
	}
	if base := doc["base"].(map[string]any); base["ref"] != "main" || base["commit"] != "1111111111111111111111111111111111111111" {
		t.Errorf("base = %v", base)
	}

	risk := doc["risk"].(map[string]any)
	if got := keys(risk); !reflect.DeepEqual(got, []string{"factors", "level", "value"}) {
		t.Errorf("risk keys = %q", got)
	}
	factor := risk["factors"].([]any)[0].(map[string]any)
	if got := keys(factor); !reflect.DeepEqual(got, []string{"delta", "name", "reason"}) {
		t.Errorf("factor keys = %q", got)
	}

	changed := doc["changed"].([]any)[0].(map[string]any)
	if got := keys(changed); !reflect.DeepEqual(got, []string{"change", "distance", "file", "id", "impact", "line", "name", "package"}) {
		t.Errorf("changed function keys = %q", got)
	}
	transitive := doc["transitiveImpact"].([]any)[0].(map[string]any)
	if got := keys(transitive); !reflect.DeepEqual(got, []string{"causedBy", "distance", "file", "id", "impact", "line", "name", "package", "path", "roots"}) {
		t.Errorf("impacted function keys = %q", got)
	}
	if transitive["id"] != "example.com/app/api.Pay" || transitive["impact"] != "transitive" ||
		!reflect.DeepEqual(transitive["path"], []any{"example.com/app/api.Pay", "example.com/app/api.process", "example.com/app/core.Charge"}) {
		t.Errorf("transitive impact = %v", transitive)
	}

	endpoint := doc["endpoints"].([]any)[0].(map[string]any)
	if endpoint["method"] != "POST" || endpoint["path"] != "/pay" || endpoint["handler"].(map[string]any)["name"] != "Pay" {
		t.Errorf("endpoint = %v", endpoint)
	}
	worker := doc["workers"].([]any)[0].(map[string]any)
	if worker["kind"] != "goroutine" || worker["name"] != "Run" || worker["impact"] != "direct" {
		t.Errorf("worker = %v", worker)
	}
	test := doc["tests"].([]any)[0].(map[string]any)
	if test["kind"] != "test" || test["name"] != "TestCharge" {
		t.Errorf("test = %v", test)
	}
	summary := doc["summary"].(map[string]any)
	if summary["changed"] != 1.0 || summary["directImpact"] != 2.0 || summary["transitiveImpact"] != 2.0 || summary["endpoints"] != 1.0 {
		t.Errorf("summary = %v", summary)
	}
}

// TestJSONStreaming checks that WriteJSON, which writes the report piece
// by piece and computes each path as it goes, gives the same bytes as
// encoding the report at once with every path set.
func TestJSONStreaming(t *testing.T) {
	r := build(t, shop, "core.Charge")
	for _, f := range append(slices.Clone(r.DirectImpact), r.TransitiveImpact...) {
		if f.Path != nil {
			t.Fatalf("Build set the path of %s", f.ID)
		}
	}

	full := *r
	fill := func(fns []Function) []Function {
		out := slices.Clone(fns)
		for i := range out {
			out[i] = r.withPath(out[i])
		}
		return out
	}
	full.Changed, full.DirectImpact, full.TransitiveImpact = fill(r.Changed), fill(r.DirectImpact), fill(r.TransitiveImpact)
	full.Endpoints = slices.Clone(r.Endpoints)
	for i := range full.Endpoints {
		full.Endpoints[i].Handler = r.withPath(full.Endpoints[i].Handler)
	}
	full.Workers = slices.Clone(r.Workers)
	for i := range full.Workers {
		full.Workers[i].Function = r.withPath(full.Workers[i].Function)
	}
	full.Tests = slices.Clone(r.Tests)
	for i := range full.Tests {
		full.Tests[i].Function = r.withPath(full.Tests[i].Function)
	}
	var want bytes.Buffer
	enc := json.NewEncoder(&want)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(&full); err != nil {
		t.Fatal(err)
	}

	got := render(t, func(b *bytes.Buffer) error { return WriteJSON(b, r) })
	if got != want.String() {
		t.Errorf("WriteJSON differs from encoding at once:\n got: %s\nwant: %s", got, want.String())
	}
	for _, path := range []string{`"path": [`, `"handler": {`, `"kind": "goroutine"`, `"kind": "test"`} {
		if !strings.Contains(got, path) {
			t.Errorf("output lacks %s, so the test does not cover it", path)
		}
	}
}

func TestJSONListsAreNeverNull(t *testing.T) {
	r := build(t, shop) // nothing changed
	out := render(t, func(b *bytes.Buffer) error { return WriteJSON(b, r) })
	if strings.Contains(out, "null") {
		t.Errorf("JSON contains null:\n%s", out)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"changed", "directImpact", "transitiveImpact", "packages", "endpoints", "workers", "tests"} {
		if list, ok := doc[k].([]any); !ok || len(list) != 0 {
			t.Errorf("%s = %v, want []", k, doc[k])
		}
	}
	if risk := doc["risk"].(map[string]any); risk["value"] != 0.0 || risk["level"] != "LOW" {
		t.Errorf("risk = %v", risk)
	}
}

func TestJSONDeterministic(t *testing.T) {
	first := render(t, func(b *bytes.Buffer) error { return WriteJSON(b, build(t, shop, "core.Charge")) })
	for i := 0; i < 5; i++ {
		if got := render(t, func(b *bytes.Buffer) error { return WriteJSON(b, build(t, shop, "core.Charge")) }); got != first {
			t.Fatalf("run %d produced different JSON", i)
		}
	}
}

func TestMarkdown(t *testing.T) {
	r := build(t, shop, "core.Charge")
	got := render(t, func(b *bytes.Buffer) error {
		return WriteMarkdown(b, r, MarkdownOptions{MaxItems: 20, MaxPaths: 10})
	})
	want := "<!-- impact-report -->\n" + "## Impact Analysis" + `

**Risk:** HIGH (51/100)

**Confidence:** HIGH (100/100)

Comparing ` + "`main` (`1111111`) → `HEAD` (`2222222`)" + `

### Summary

- 1 changed function
- 2 directly impacted functions
- 2 transitively impacted functions
- 3 affected packages
- 1 affected endpoint
- 1 affected worker
- 1 affected test

### Changed functions

- ` + "`Charge` (modified) — `core/core.go`" + `

### Affected endpoints

- ` + "`POST /pay` — transitive (distance 2), handler `Pay`" + `

### Affected workers

- ` + "`Run` (goroutine) — direct" + `

### Affected tests

- ` + "`TestCharge` — direct" + `

### Important impact paths

- ` + "`Pay` → `process` → `Charge` **[changed]**" + `
- ` + "`Run` → `Charge` **[changed]**" + `
- ` + "`Start` → `Run` → `Charge` **[changed]**" + `

### Risk factors

- ` + "`+4`" + ` 1 function changed: Charge
- ` + "`+5`" + ` exported API changed: Charge
- ` + "`+15`" + ` affects 1 endpoint: POST /pay
- ` + "`+12`" + ` affects 1 worker: Run
- ` + "`+8`" + ` impact reaches 3 packages
- ` + "`+8`" + ` 4 functions impacted (2 direct, 2 transitive)
- ` + "`+4`" + ` impact reaches distance 2
- ` + "`-5`" + ` 1 affected test exercising the change (not proof of safety)

### Confidence factors

No known blind spots in the affected code.

<details>
<summary>Impacted functions (4)</summary>

- ` + "`process` — direct — `api/api.go`" + `
- ` + "`Run` — direct — `jobs/jobs.go`" + `
- ` + "`Pay` — transitive (distance 2) — `api/api.go`" + `
- ` + "`Start` — transitive (distance 2) — `jobs/jobs.go`" + `

</details>

<details>
<summary>Affected packages (3)</summary>

- ` + "`api` — 1 direct, 1 transitive" + `
- ` + "`core` — 1 changed" + `
- ` + "`jobs` — 1 direct, 1 transitive" + `

</details>

<sub>Generated by impact test. Risk and confidence are heuristics: see the factors above.</sub>
`
	if got != want {
		t.Errorf("markdown:\n%s\nwant:\n%s", got, want)
	}
}

func TestMarkdownTruncation(t *testing.T) {
	files := map[string]string{"core/core.go": "package core\n\nfunc Charge() {}\n"}
	src := "package many\n\nimport \"example.com/app/core\"\n\n"
	for i := 0; i < 30; i++ {
		src += fmt.Sprintf("func F%02d() { core.Charge() }\n", i)
	}
	files["many/many.go"] = src
	r := build(t, files, "core.Charge")

	limited := render(t, func(b *bytes.Buffer) error { return WriteMarkdown(b, r, MarkdownOptions{MaxItems: 5, MaxPaths: 10}) })
	if !strings.Contains(limited, "_25 additional impacted functions omitted._") {
		t.Errorf("missing truncation note:\n%s", limited)
	}
	if n := strings.Count(limited, "— direct — `many/many.go`"); n != 5 {
		t.Errorf("listed %d impacted functions, want 5", n)
	}
	if strings.Contains(limited, "F05") {
		t.Error("an omitted function is listed")
	}

	// Lists are complete without a limit, and JSON is always complete.
	unlimited := render(t, func(b *bytes.Buffer) error { return WriteMarkdown(b, r, MarkdownOptions{MaxItems: 0, MaxPaths: 10}) })
	if strings.Contains(unlimited, "omitted") || !strings.Contains(unlimited, "F29") {
		t.Error("unlimited markdown is truncated")
	}
	if len(r.DirectImpact) != 30 {
		t.Errorf("report has %d direct impacts, want 30", len(r.DirectImpact))
	}
}

func TestMarkdownNoGoChanges(t *testing.T) {
	r := build(t, shop)
	got := render(t, func(b *bytes.Buffer) error { return WriteMarkdown(b, r, MarkdownOptions{MaxItems: 20}) })
	if !strings.Contains(got, "No Go files changed between `main` (`1111111`) and `HEAD` (`2222222`).") || strings.Contains(got, "**Risk:**") {
		t.Errorf("markdown:\n%s", got)
	}
}

func TestMarkdownEscaping(t *testing.T) {
	if got := escape("a_b *c* <d> [e] #f |g|"); got != `a\_b \*c\* \<d\> \[e\] \#f \|g\|` {
		t.Errorf("escape = %q", got)
	}
	if got := code("x`y"); got != "`` x`y ``" {
		t.Errorf("code = %q", got)
	}
}

func TestPackageLabel(t *testing.T) {
	for _, tt := range []struct{ dir, name, want string }{
		{".", "main", "main"},
		{"internal/user", "user", "internal/user"},
		{"internal/user", "user_test", "internal/user_test"},
	} {
		if got := PackageLabel(tt.dir, tt.name); got != tt.want {
			t.Errorf("PackageLabel(%q, %q) = %q, want %q", tt.dir, tt.name, got, tt.want)
		}
	}
}

func TestCommitLabels(t *testing.T) {
	sha := "0d191bba3735409cda8c38b8b633636fa2991cb6"
	other := "54a7f81a7ff5e4e120f05fb60652d59f1330c62a"
	for _, tt := range []struct {
		c          Commit
		mergeBase  bool
		text, mark string
	}{
		{Commit{Ref: "main", Commit: sha}, false, "main (0d191bb)", "`main` (`0d191bb`)"},
		{Commit{Ref: sha, Commit: sha}, false, "0d191bb", "`0d191bb`"},
		{Commit{Ref: "main", Commit: sha}, true, "main (merge base 0d191bb)", "the merge base `0d191bb` of `main`"},
		{Commit{Ref: other, Commit: sha}, true, "54a7f81 (merge base 0d191bb)", "the merge base `0d191bb` of `54a7f81`"},
		{Commit{Ref: "deadbeef-branch", Commit: sha}, false, "deadbeef-branch (0d191bb)", "`deadbeef-branch` (`0d191bb`)"},
	} {
		if got := commitLabel(tt.c, tt.mergeBase); got != tt.text {
			t.Errorf("commitLabel(%v, %v) = %q, want %q", tt.c, tt.mergeBase, got, tt.text)
		}
		if got := mdCommit(tt.c, tt.mergeBase); got != tt.mark {
			t.Errorf("mdCommit(%v, %v) = %q, want %q", tt.c, tt.mergeBase, got, tt.mark)
		}
	}
}
