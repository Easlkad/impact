package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Easlkad/impact/internal/gittest"
)

func writeRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func runCLI(args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

var sampleRepo = map[string]string{
	"go.mod": "module example.com/app\n\ngo 1.22\n",
	"main.go": `package main

import "example.com/app/user"

func main() { user.CreateUser() }
`,
	"user/user.go": `package user

type Store struct{}

func (s *Store) Save() {}

func CreateUser() {
	ValidateUser()
	(&Store{}).Save()
}

func ValidateUser() {}
`,
}

func TestScan(t *testing.T) {
	root := writeRepo(t, sampleRepo)

	code, out, errOut := runCLI("scan", root)
	if code != 0 {
		t.Fatalf("exit code %d, stderr: %s", code, errOut)
	}
	for _, want := range []string{
		"Repository scanned: ",
		"Packages: 2\n",
		"Files: 2\n",
		"Functions: 3\n",
		"Methods: 1\n",
		"Call relationships: 3\n",
		"External call sites: 0\n",
		"Unresolved call sites: 0\n",
		"Sample call relationships (3 of 3):\n" +
			"  main.main -> user.CreateUser\n" +
			"  user.CreateUser -> user.Store.Save\n" +
			"  user.CreateUser -> user.ValidateUser\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q\noutput:\n%s", want, out)
		}
	}
}

func TestScanFlagsAfterPath(t *testing.T) {
	root := writeRepo(t, sampleRepo)

	code, out, _ := runCLI("scan", root, "-sample", "1")
	if code != 0 || !strings.Contains(out, "Sample call relationships (1 of 3)") {
		t.Errorf("-sample 1: exit code %d, output:\n%s", code, out)
	}

	_, out, _ = runCLI("scan", "-sample", "0", root)
	if strings.Contains(out, "Sample") {
		t.Errorf("-sample 0 still printed a sample:\n%s", out)
	}
}

func TestUsageErrors(t *testing.T) {
	tests := []struct {
		args []string
		code int
	}{
		{nil, 2},
		{[]string{"bogus"}, 2},
		{[]string{"scan"}, 2},
		{[]string{"scan", "a", "b"}, 2},
		{[]string{"scan", "-unknown", "."}, 2},
		{[]string{"scan", filepath.Join(t.TempDir(), "missing")}, 1},
		{[]string{"help"}, 0},
	}
	for _, tt := range tests {
		if code, _, _ := runCLI(tt.args...); code != tt.code {
			t.Errorf("impact %q: exit code %d, want %d", tt.args, code, tt.code)
		}
	}
}

func TestDiff(t *testing.T) {
	g := gittest.New(t)
	g.Write(sampleRepo)
	g.Write(map[string]string{"user/legacy.go": "package user\n\nfunc Legacy() {}\n"})
	g.Commit("base")
	g.Remove("user/legacy.go")
	g.Write(map[string]string{
		"user/user.go": `package user

type Store struct{}

func (s *Store) Save() {}

var defaultName = "guest"

func CreateUser() {
	ValidateUser()
	ValidateUser()
	(&Store{}).Save()
}

func ValidateUser() {}

func Retry() {}
`,
		"README.md": "docs\n",
	})
	g.Commit("head")

	code, out, errOut := runCLI("diff", g.Dir, "HEAD~1", "HEAD")
	if code != 0 {
		t.Fatalf("exit code %d, stderr: %s", code, errOut)
	}
	want := `
Changed files:
  deleted   user/legacy.go
  modified  user/user.go

Changed functions:
  user/user.go
    CreateUser

Added functions:
  user/user.go
    Retry

Deleted functions:
  user/legacy.go
    Legacy

Package-level changes:
  user/user.go:7
`
	if !strings.HasPrefix(out, "Comparing HEAD~1 (") || !strings.HasSuffix(out, want) {
		t.Errorf("output:\n%s\nwant it to end with:\n%s", out, want)
	}
}

// failingWriter fails every write, as a closed pipe or a full disk would.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

// TestOutputErrors checks that every command fails when its output cannot
// be written, rather than reporting success with a truncated output.
func TestOutputErrors(t *testing.T) {
	g := gittest.New(t)
	g.Write(sampleRepo)
	g.Commit("base")
	g.Write(map[string]string{"user/user.go": strings.Replace(sampleRepo["user/user.go"], "func ValidateUser() {}", "func ValidateUser() { _ = 1 }", 1)})
	g.Commit("head")

	for _, args := range [][]string{
		{"scan", g.Dir},
		{"diff", g.Dir, "HEAD~1", "HEAD"},
		{"analyze", g.Dir, "HEAD~1", "HEAD"},
		{"version"},
	} {
		var errOut bytes.Buffer
		code := run(args, failingWriter{}, &errOut)
		if code != exitError || !strings.Contains(errOut.String(), "disk full") {
			t.Errorf("%s: exit code %d, stderr %q; want %d and the write error", args[0], code, errOut.String(), exitError)
		}
	}
}

func TestDiffNoGoChanges(t *testing.T) {
	g := gittest.New(t)
	g.Write(sampleRepo)
	g.Commit("base")
	g.Write(map[string]string{"README.md": "docs\n"})
	g.Commit("head")

	code, out, _ := runCLI("diff", g.Dir, "HEAD~1", "HEAD")
	if code != 0 || !strings.Contains(out, "No Go files changed.") {
		t.Errorf("exit code %d, output:\n%s", code, out)
	}
}

func TestDiffErrors(t *testing.T) {
	g := gittest.New(t)
	g.Write(sampleRepo)
	g.Commit("base")

	tests := []struct {
		args []string
		code int
		msg  string
	}{
		{[]string{"diff", g.Dir, "HEAD"}, 2, "Usage: impact diff"},
		{[]string{"diff", g.Dir, "HEAD", "no-such-ref"}, 1, `ref "no-such-ref" does not exist`},
		{[]string{"diff", t.TempDir(), "HEAD", "HEAD"}, 1, "not inside a git work tree"},
	}
	for _, tt := range tests {
		code, _, errOut := runCLI(tt.args...)
		if code != tt.code || !strings.Contains(errOut, tt.msg) {
			t.Errorf("impact %q: exit code %d, stderr %q; want %d and %q", tt.args, code, errOut, tt.code, tt.msg)
		}
	}
}

// shopRepo is a small service: HTTP handlers registered on a ServeMux, a
// goroutine worker, and tests, all depending on the payment package.
var shopRepo = map[string]string{
	"go.mod": "module example.com/shop\n\ngo 1.22\n",
	"payment/service.go": `package payment

func ProcessPayment(amount int) error {
	return validate(amount)
}

func LegacyCharge(amount int) {}

func validate(amount int) error { return nil }
`,
	"payment/retry.go": `package payment

func RetryPayment(amount int) error {
	return ProcessPayment(amount)
}
`,
	"payment/gateway.go": `package payment

const fee = 1

type Gateway struct{}

func (g *Gateway) Charge(amount int) error { return nil }

type Charger interface{ Charge(amount int) error }

func Bill(c Charger) { c.Charge(fee) }
`,
	"payment/service_test.go": `package payment

import "testing"

func TestProcessPayment(t *testing.T) { ProcessPayment(1) }

func BenchmarkRetry(b *testing.B) { RetryPayment(1) }

func fixture() { ProcessPayment(0) }
`,
	"api/payment.go": `package api

import (
	"net/http"

	"example.com/shop/payment"
)

func PaymentHandler(w http.ResponseWriter, r *http.Request) {
	payment.ProcessPayment(10)
}

func ChargeHandler(w http.ResponseWriter, r *http.Request) {
	payment.LegacyCharge(10)
}

func HealthHandler(w http.ResponseWriter, r *http.Request) {}
`,
	"router/router.go": `package router

import (
	"net/http"

	"example.com/shop/api"
)

func RegisterPaymentRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /payments", api.PaymentHandler)
	mux.HandleFunc("GET /health", api.HealthHandler)
}

func RegisterChargeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/charge", api.ChargeHandler)
}
`,
	"worker/worker.go": `package worker

import "example.com/shop/payment"

func Start() { go RunRetries() }

func RunRetries() { payment.RetryPayment(1) }
`,
}

// TestAnalyze follows a change from a low-level function to an HTTP
// endpoint, a worker and tests.
func TestAnalyze(t *testing.T) {
	g := gittest.New(t)
	g.Write(shopRepo)
	g.Commit("base")
	g.Write(map[string]string{"payment/service.go": strings.Replace(shopRepo["payment/service.go"],
		"return validate(amount)", "return validate(amount * 100)", 1)})
	g.Commit("head")
	statusBefore := g.Git("status", "--porcelain")

	code, out, errOut := runCLI("analyze", g.Dir, "HEAD~1", "HEAD")
	if code != 0 {
		t.Fatalf("exit code %d, stderr: %s", code, errOut)
	}
	want := `
Changed:
  payment/service.go
    ProcessPayment [modified]

Direct impact:
  api/payment.go
    PaymentHandler (calls ProcessPayment)

  payment/retry.go
    RetryPayment (calls ProcessPayment)

  payment/service_test.go
    fixture (calls ProcessPayment)

Transitive impact:
  worker/worker.go
    RunRetries (distance 2)
    Start (distance 3)

Impact paths:
  RunRetries (worker/worker.go)
    -> RetryPayment (payment/retry.go)
    -> ProcessPayment (payment/service.go) [changed]

  Start (worker/worker.go)
    -> RunRetries (worker/worker.go)
    -> RetryPayment (payment/retry.go)
    -> ProcessPayment (payment/service.go) [changed]

Package-level changes (not propagated):
  (none)

Affected packages:
  api
    direct: 1

  payment
    changed: 1
    direct: 2

  worker
    transitive: 2

Affected endpoints:
  POST /payments
    handler: PaymentHandler (api/payment.go)
    impact: direct

Affected workers:
  RunRetries (worker/worker.go)
    kind: goroutine
    impact: transitive (distance 2)

Affected tests:
  TestProcessPayment (payment/service_test.go)
    impact: direct

  BenchmarkRetry (payment/service_test.go) [benchmark]
    impact: transitive (distance 2)

Summary:
  Changed functions: 1
  Direct impact: 3
  Transitive impact: 2
  Packages: 3
  Endpoints: 1
  Workers: 1
  Tests: 2

Assessment:
  Risk: HIGH (57/100)
  Confidence: HIGH (100/100)

Risk factors:
  +4   1 function changed: ProcessPayment
  +5   exported API changed: ProcessPayment
  +15  affects 1 endpoint: POST /payments
  +12  affects 1 worker: RunRetries
  +8   impact reaches 3 packages
  +10  5 functions impacted (3 direct, 2 transitive)
  +8   impact reaches distance 3
  -5   2 affected tests exercising the change (not proof of safety)

Confidence factors:
  (no known blind spots in the affected code)
`
	if !strings.HasPrefix(out, "Comparing HEAD~1 (") || !strings.HasSuffix(out, want) {
		t.Errorf("output:\n%s\nwant it to end with:\n%s", out, want)
	}
	if got := g.Git("status", "--porcelain"); got != statusBefore {
		t.Errorf("git status changed from %q to %q", statusBefore, got)
	}
}

func TestAnalyzeDeletedFunction(t *testing.T) {
	g := gittest.New(t)
	g.Write(shopRepo)
	g.Commit("base")
	// LegacyCharge is deleted; ChargeHandler still calls it in the head
	// commit, which only the base call graph can reveal.
	g.Write(map[string]string{"payment/service.go": strings.Replace(shopRepo["payment/service.go"],
		"func LegacyCharge(amount int) {}\n\n", "", 1)})
	g.Commit("head")

	code, out, errOut := runCLI("analyze", g.Dir, "HEAD~1", "HEAD", "-paths", "0")
	if code != 0 {
		t.Fatalf("exit code %d, stderr: %s", code, errOut)
	}
	for _, want := range []string{
		"Changed:\n  payment/service.go\n    LegacyCharge [deleted]\n",
		"Direct impact:\n  api/payment.go\n    ChargeHandler (calls LegacyCharge)\n",
		"Affected endpoints:\n  ANY /charge\n    handler: ChargeHandler (api/payment.go)\n    impact: direct\n",
		"Endpoints: 1\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q\noutput:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Impact paths") {
		t.Error("-paths 0 still printed paths")
	}
}
func TestAnalyzeErrors(t *testing.T) {
	g := gittest.New(t)
	g.Write(shopRepo)
	g.Commit("base")

	if code, _, _ := runCLI("analyze", g.Dir, "HEAD"); code != 2 {
		t.Errorf("missing ref: exit code %d, want 2", code)
	}
	if code, _, errOut := runCLI("analyze", g.Dir, "HEAD", "nope"); code != 1 || !strings.Contains(errOut, `"nope"`) {
		t.Errorf("unknown ref: exit code %d, stderr %q", code, errOut)
	}
	code, out, _ := runCLI("analyze", g.Dir, "HEAD", "HEAD")
	if code != 0 || !strings.Contains(out, "No Go files changed.") {
		t.Errorf("same commit: exit code %d, output %q", code, out)
	}
}

// TestAnalyzeAssessment produces both risk and confidence factors: a deleted
// handler dependency and an endpoint raise the risk; an interface call that
// may reach the changed method, the base-commit mapping of the deleted
// function and a package-level change lower the confidence.
// assessedRepo returns a repository whose last commit changes the gateway
// and deletes LegacyCharge: risk MODERATE (42), confidence HIGH (85).
func assessedRepo(t *testing.T) *gittest.Repo {
	t.Helper()
	g := gittest.New(t)
	g.Write(shopRepo)
	g.Commit("base")
	gateway := strings.Replace(shopRepo["payment/gateway.go"], "const fee = 1", "const fee = 2", 1)
	gateway = strings.Replace(gateway, "error { return nil }", "error {\n\tif amount < 0 {\n\t\treturn nil\n\t}\n\treturn nil\n}", 1)
	g.Write(map[string]string{
		"payment/gateway.go": gateway,
		"payment/service.go": strings.Replace(shopRepo["payment/service.go"], "func LegacyCharge(amount int) {}\n\n", "", 1),
	})
	g.Commit("head")
	return g
}

func TestAnalyzeAssessment(t *testing.T) {
	g := assessedRepo(t)
	code, out, errOut := runCLI("analyze", g.Dir, "HEAD~1", "HEAD")
	if code != 0 {
		t.Fatalf("exit code %d, stderr: %s", code, errOut)
	}
	want := `
Assessment:
  Risk: MODERATE (42/100)
  Confidence: HIGH (85/100)

Risk factors:
  +8   2 functions changed: Gateway.Charge, LegacyCharge
  +5   exported API changed: Gateway.Charge, LegacyCharge
  +8   1 function deleted
  +15  affects 1 endpoint: ANY /charge
  +4   impact reaches 2 packages
  +2   1 function impacted (1 direct, 0 transitive)

Confidence factors:
  -5   callers of 1 deleted function were taken from the base commit and matched to head by ID
  -4   1 interface method call may reach affected methods (Charge) through implementations that cannot be linked
  -6   1 package-level change, whose users are not analyzed
`
	if !strings.HasSuffix(out, want) {
		t.Errorf("output:\n%s\nwant it to end with:\n%s", out, want)
	}
}

func TestAnalyzeJSON(t *testing.T) {
	g := assessedRepo(t)
	code, out, errOut := runCLI("analyze", g.Dir, "HEAD~1", "HEAD", "--format", "json")
	if code != 0 {
		t.Fatalf("exit code %d, stderr: %s", code, errOut)
	}
	var doc struct {
		SchemaVersion int `json:"schemaVersion"`
		Tool          struct{ Name, Version string }
		Base, Head    struct{ Ref, Commit string }
		Changed       []struct{ ID, Name, Change string }
		Endpoints     []struct{ Method, Path string }
		Risk          struct {
			Value int
			Level string
		}
		Confidence struct {
			Value   int
			Level   string
			Factors []struct {
				Name  string
				Delta int
			}
		}
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if doc.SchemaVersion != 1 || doc.Tool.Name != "impact" || doc.Tool.Version != "dev" || doc.Base.Ref != "HEAD~1" || len(doc.Base.Commit) != 40 {
		t.Errorf("header = %+v", doc)
	}
	if len(doc.Changed) != 2 || doc.Changed[1].Name != "LegacyCharge" || doc.Changed[1].Change != "deleted" {
		t.Errorf("changed = %+v", doc.Changed)
	}
	if len(doc.Endpoints) != 1 || doc.Endpoints[0].Method != "ANY" || doc.Endpoints[0].Path != "/charge" {
		t.Errorf("endpoints = %+v", doc.Endpoints)
	}
	if doc.Risk.Value != 42 || doc.Risk.Level != "MODERATE" || doc.Confidence.Value != 85 || len(doc.Confidence.Factors) != 3 {
		t.Errorf("scores = %+v %+v", doc.Risk, doc.Confidence)
	}

	// The same commits always give the same bytes.
	if _, again, _ := runCLI("analyze", g.Dir, "HEAD~1", "HEAD", "--format", "json"); again != out {
		t.Error("JSON output is not deterministic")
	}
}

func TestAnalyzeMarkdown(t *testing.T) {
	g := assessedRepo(t)
	code, out, errOut := runCLI("analyze", g.Dir, "HEAD~1", "HEAD", "--format", "markdown")
	if code != 0 {
		t.Fatalf("exit code %d, stderr: %s", code, errOut)
	}
	for _, want := range []string{
		"<!-- impact-report -->\n## Impact Analysis\n",
		"**Risk:** MODERATE (42/100)",
		"**Confidence:** HIGH (85/100)",
		"### Affected endpoints\n\n- `ANY /charge` — direct, handler `ChargeHandler`\n",
		"- `-6` 1 package-level change, whose users are not analyzed\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("markdown is missing %q\n%s", want, out)
		}
	}
}

func TestAnalyzeThresholds(t *testing.T) {
	g := assessedRepo(t) // risk 42, confidence 85
	tests := []struct {
		name   string
		flags  []string
		code   int
		stderr string
	}{
		{"no thresholds", nil, 0, ""},
		{"risk below threshold", []string{"--fail-risk", "43"}, 0, ""},
		{"risk at threshold", []string{"--fail-risk", "42"}, 3, "risk MODERATE (42) is at or above the -fail-risk threshold 42"},
		{"confidence at threshold", []string{"--fail-confidence-below", "85"}, 0, ""},
		{"confidence below threshold", []string{"--fail-confidence-below", "86"}, 4, "confidence HIGH (85) is below the -fail-confidence-below threshold 86"},
		{"both", []string{"--fail-risk", "40", "--fail-confidence-below", "90"}, 5, "-fail-risk"},
		{"invalid", []string{"--fail-risk", "101"}, 2, "between 0 and 100"},
	}
	for _, tt := range tests {
		args := append([]string{"analyze", g.Dir, "HEAD~1", "HEAD", "--format", "json"}, tt.flags...)
		code, out, errOut := runCLI(args...)
		if code != tt.code || !strings.Contains(errOut, tt.stderr) {
			t.Errorf("%s: exit code %d, stderr %q; want %d and %q", tt.name, code, errOut, tt.code, tt.stderr)
		}
		// A violated threshold still prints the full report.
		if tt.code == 3 || tt.code == 4 || tt.code == 5 {
			if !json.Valid([]byte(out)) {
				t.Errorf("%s: no report printed", tt.name)
			}
		}
	}
}

func TestAnalyzeUsageErrors(t *testing.T) {
	g := assessedRepo(t)
	for _, args := range [][]string{
		{"analyze", g.Dir, "HEAD~1", "HEAD", "--format", "yaml"},
		{"analyze", g.Dir, "HEAD~1", "HEAD", "--max-items", "-1"},
	} {
		if code, _, _ := runCLI(args...); code != 2 {
			t.Errorf("impact %q: exit code %d, want 2", args, code)
		}
	}
}

func TestAnalyzeMergeBase(t *testing.T) {
	g := gittest.New(t)
	g.Write(map[string]string{
		"go.mod":       "module example.com/app\n\ngo 1.22\n",
		"user/user.go": "package user\n\nfunc Create() {}\n\nfunc Delete() {}\n",
	})
	base := g.Commit("base")
	// The feature branch changes Create; it is created on the side, without
	// checking it out.
	feature := g.CommitOn(base, "feature", map[string]string{
		"user/user.go": "package user\n\nfunc Create() { validate() }\n\nfunc Delete() {}\n\nfunc validate() {}\n",
	})
	// Meanwhile the base branch changes Delete.
	g.Write(map[string]string{"user/user.go": "package user\n\nfunc Create() {}\n\nfunc Delete() { audit() }\n\nfunc audit() {}\n"})
	g.Commit("main moves on")

	changed := func(args ...string) []string {
		t.Helper()
		code, out, errOut := runCLI(append([]string{"analyze", g.Dir, "HEAD", feature, "--format", "json"}, args...)...)
		if code != 0 {
			t.Fatalf("exit code %d, stderr: %s", code, errOut)
		}
		var doc struct {
			MergeBase bool
			Base      struct{ Commit string }
			Changed   []struct{ Name, Change string }
		}
		if err := json.Unmarshal([]byte(out), &doc); err != nil {
			t.Fatal(err)
		}
		if doc.MergeBase != (len(args) > 0) || (doc.MergeBase && doc.Base.Commit != base) {
			t.Errorf("mergeBase = %v, base commit = %s", doc.MergeBase, doc.Base.Commit)
		}
		var names []string
		for _, c := range doc.Changed {
			names = append(names, c.Name+" "+c.Change)
		}
		return names
	}

	// Comparing with the tip of main also shows main's own change, reversed.
	if got := changed(); !slices.Equal(got, []string{"Create modified", "Delete modified", "audit deleted", "validate added"}) {
		t.Errorf("without --merge-base: %q", got)
	}
	// From the merge base, only the feature's changes remain.
	if got := changed("--merge-base"); !slices.Equal(got, []string{"Create modified", "validate added"}) {
		t.Errorf("with --merge-base: %q", got)
	}

	code, out, _ := runCLI("diff", g.Dir, "HEAD", feature, "--merge-base")
	if code != 0 || !strings.Contains(out, "(merge base "+base[:7]+")") || strings.Contains(out, "    Delete\n") {
		t.Errorf("diff --merge-base: exit code %d\n%s", code, out)
	}
}

func TestVersion(t *testing.T) {
	for _, arg := range []string{"version", "--version"} {
		code, out, _ := runCLI(arg)
		if code != 0 || out != "impact dev\n" {
			t.Errorf("impact %s: exit code %d, output %q", arg, code, out)
		}
	}
}
