# impact

`impact` analyzes a pull request or any two git commits of a Go repository.
It reports:

- **what changed**: the Go functions and methods that were modified, added
  or deleted;
- **what is affected**: every function that calls them, directly or
  through other functions, with the call path that explains why;
- **where it matters**: affected packages, `net/http` endpoints, goroutine
  workers and tests;
- **how much to worry**: a **risk** score and a separate **confidence**
  score, each explained factor by factor.

It is a single CLI with no server, database or account. It reads the
repository without modifying it, and produces text for terminals, JSON for
tools and Markdown for pull requests.

> **Go only.** `impact` currently analyzes Go code only. The internal model
> is language-neutral, so other languages can be added later.

## Install

Requires Go 1.22 or newer, and git.

```sh
go install github.com/Easlkad/impact/cmd/impact@latest
```

Or from a checkout:

```sh
go build -o impact ./cmd/impact        # impact.exe on Windows
impact version
```

## Quick start

```sh
impact analyze . main HEAD                      # what does my branch affect?
impact analyze . main HEAD --merge-base         # ... from where the branch started, like a PR
impact analyze . main HEAD --format markdown    # a report for a pull request
impact analyze . main HEAD --format json        # for scripts and CI tools
```

## Commands

| Command | Purpose |
|---------|---------|
| `impact scan <path>` | Summarize a Go repository: packages, functions, call graph |
| `impact diff <path> <base> <head>` | List the Go files and functions changed between two refs |
| `impact analyze <path> <base> <head>` | Changes, impact, context, risk and confidence |
| `impact version` | Print the version |

Flags can be placed before or after the positional arguments. Refs can be
branches, tags, commit hashes or expressions such as `HEAD~3`.

### `impact scan`

```
$ impact scan .
Repository scanned: /home/me/impact

Packages: 7
Files: 12
Functions: 48
Methods: 58
Call relationships: 134
External call sites: 233
Unresolved call sites: 39

Sample call relationships (10 of 134):
  cmd/impact.main -> cmd/impact.run
  ...
```

Flags: `-sample N` (call relationships to print, default 10) and `-tests`
(also scan `_test.go` files).

- **Call relationships** are distinct caller → callee pairs between
  functions of the repository.
- **External call sites** are calls into the standard library or
  dependencies.
- **Unresolved call sites** are calls whose target cannot be determined
  without type checking (see [Limitations](#limitations)).

### `impact diff`

```
$ impact diff . main HEAD
Comparing main (4d21282) -> HEAD (149c154)

Changed files:
  added     cmd/impact/diff.go
  modified  cmd/impact/main.go

Changed functions:
  cmd/impact/main.go
    run

Added functions:
  cmd/impact/diff.go
    runDiff

Deleted functions:
  internal/analyzer/golang/analyzer.go
    scanner.findEnclosingModule

Package-level changes:
  cmd/impact/main.go:17
  internal/model/model.go:42 (removed; base line numbers)
```

- **Changed functions** exist in both commits, and changed lines fall inside
  them. A function's range includes its doc comment.
- **Added / deleted functions** exist in only one commit. Deleted functions
  are shown with their file and lines in the base commit.
- **Package-level changes** are changed lines outside every function
  (declarations, imports, comments). Blank lines are ignored. In added or
  deleted files, which are new or gone as a whole, only the type, constant
  and variable declarations are reported.

Only `.go` files are considered, test files included. Flag: `-merge-base`
(see [pull requests](#pull-requests-and---merge-base)).

### `impact analyze`

```sh
impact analyze [flags] <repository-path> <base-ref> <head-ref>
```

| Flag | Default | Description |
|------|---------|-------------|
| `--format` | `text` | `text`, `json` or `markdown` |
| `--merge-base` | off | Compare head with the merge base of the two refs |
| `--paths N` | `10` | Impact paths shown in text and Markdown output |
| `--max-items N` | `20` | Items per list in Markdown output (`0`: no limit). JSON is always complete |
| `--fail-risk N` | off | Exit with code 3 if risk ≥ N |
| `--fail-confidence-below N` | off | Exit with code 4 if confidence < N |

The text output, for a change to `DB.SaveInvoice` in a small billing service.
An HTTP handler and a goroutine worker reach it through
`Service.CreateInvoice`:

```
$ impact analyze . main HEAD
Comparing main (13e3e9d) -> HEAD (e60d86f)

Changed:
  internal/store/store.go
    DB.SaveInvoice [modified]

Direct impact:
  internal/billing/service.go
    Service.CreateInvoice (calls DB.SaveInvoice)

Transitive impact:
  internal/api/handlers.go
    Handlers.CreateInvoice (distance 2)

  internal/worker/worker.go
    Reconciler.Run (distance 2)

  cmd/server/main.go
    main (distance 3)

Impact paths:
  Handlers.CreateInvoice (internal/api/handlers.go)
    -> Service.CreateInvoice (internal/billing/service.go)
    -> DB.SaveInvoice (internal/store/store.go) [changed]
  ...

Package-level changes (not propagated):
  (none)

Affected packages:
  internal/billing
    direct: 1

  internal/store
    changed: 1
  ...

Affected endpoints:
  POST /invoices
    handler: Handlers.CreateInvoice (internal/api/handlers.go)
    impact: transitive (distance 2)

Affected workers:
  Reconciler.Run (internal/worker/worker.go)
    kind: goroutine
    impact: transitive (distance 2)

Affected tests:
  BenchmarkCreateInvoice (internal/billing/service_test.go) [benchmark]
    impact: transitive (distance 2)

  TestCreateInvoice (internal/billing/service_test.go)
    impact: transitive (distance 2)

Summary:
  Changed functions: 1
  Direct impact: 1
  Transitive impact: 3
  Packages: 5
  Endpoints: 1
  Workers: 1
  Tests: 2

Assessment:
  Risk: HIGH (63/100)
  Confidence: HIGH (95/100)

Risk factors:
  +4   1 function changed: DB.SaveInvoice
  +5   exported API changed: DB.SaveInvoice
  +15  affects 1 endpoint: POST /invoices
  +12  affects 1 worker: Reconciler.Run
  +16  impact reaches 5 packages
  +8   4 functions impacted (1 direct, 3 transitive)
  +8   impact reaches distance 3
  -5   2 affected tests exercising the change (not proof of safety)

Confidence factors:
  -5   1 route registration in affected code could not be fully resolved (dynamic pattern or handler)
```

- **Distance** is the number of calls between a function and the nearest
  changed function: 1 for direct callers, 2 or more for transitive ones.
  Each function is reported once, at its shortest distance. A changed
  function is never reported as impacted.
- **Impact paths** show one shortest chain of calls to a changed function.
- **Affected endpoints** are `net/http` routes whose handler is changed or
  impacted (`ANY` when the pattern has no method). **Workers** are functions
  started as goroutines: `go f()` (`goroutine`), or called inside
  `go func() { ... }()` (`background`).
- **Affected tests** (`TestXxx`, `BenchmarkXxx`, `FuzzXxx`, `ExampleXxx`)
  are listed only in their own section, not as direct or transitive impact.
  Helper functions in `_test.go` files stay with the regular impacts.

#### JSON output

`--format json` prints the complete result, for CI tools and scripts. It is
an explicit, versioned output model (`schemaVersion`), independent of the
internal Go types:

- New fields may be added within a schema version.
- Renaming or removing a field increments `schemaVersion`.
- Lists are always present (`[]`, never `null`), sorted as documented in
  [`internal/report/report.go`](internal/report/report.go).
- The same commits always produce the same bytes.

Abridged:

```json
{
  "schemaVersion": 1,
  "tool": { "name": "impact", "version": "dev" },
  "base": { "ref": "main", "commit": "7348648a3514f07a2c9f74b1d071c017e1854b9b" },
  "head": { "ref": "HEAD", "commit": "a2743404f2b60e649cfde6bf729d93b1d02236a5" },
  "mergeBase": false,
  "changedFiles": [{ "path": "internal/store/store.go", "status": "modified" }],
  "changed": [
    {
      "id": "example.com/billing/internal/store.DB.SaveInvoice",
      "name": "DB.SaveInvoice",
      "package": "example.com/billing/internal/store",
      "file": "internal/store/store.go",
      "line": 5,
      "impact": "changed",
      "distance": 0,
      "change": "modified"
    }
  ],
  "directImpact": [ ... ],
  "transitiveImpact": [ ... ],
  "packages": [ ... ],
  "endpoints": [
    {
      "method": "POST",
      "path": "/invoices",
      "handler": {
        "id": "example.com/billing/internal/api.Handlers.CreateInvoice",
        "name": "Handlers.CreateInvoice",
        "impact": "transitive",
        "distance": 2,
        "causedBy": ["example.com/billing/internal/billing.Service.CreateInvoice"],
        "roots": ["example.com/billing/internal/store.DB.SaveInvoice"],
        "path": [
          "example.com/billing/internal/api.Handlers.CreateInvoice",
          "example.com/billing/internal/billing.Service.CreateInvoice",
          "example.com/billing/internal/store.DB.SaveInvoice"
        ],
        ...
      }
    }
  ],
  "workers": [ ... ],
  "tests": [ ... ],
  "packageLevelChanges": [],
  "unmappedCallers": [],
  "warnings": [],
  "summary": { "changedFiles": 1, "changed": 1, "directImpact": 1, "transitiveImpact": 3,
               "packages": 5, "endpoints": 1, "workers": 1, "tests": 2, "packageLevelChanges": 0 },
  "risk": {
    "value": 63,
    "level": "HIGH",
    "factors": [
      { "name": "changed-functions", "delta": 4, "reason": "1 function changed: DB.SaveInvoice" },
      { "name": "endpoints", "delta": 15, "reason": "affects 1 endpoint: POST /invoices" },
      ...
    ]
  },
  "confidence": { "value": 91, "level": "HIGH", "factors": [ ... ] }
}
```

Every function has an `id` (unique, see [identifiers](#identifiers)), a
`name` and an `impact` (`changed`, `direct` or `transitive`). Impacted
functions also have:

- `causedBy`: the functions they call one step closer to a change;
- `roots`: all the changed functions they depend on;
- `path`: a shortest call chain to a change.

Factor `name`s are stable identifiers; `reason`s are for people.

#### Markdown output

`--format markdown` prints a compact report for a pull request comment or a
CI job summary:

- Each list shows at most `--max-items` entries (default 20), followed by a
  line such as "_12 additional impacted functions omitted._".
- The full lists of impacted functions and packages are in collapsed
  `<details>` sections.
- The report starts with a hidden `<!-- impact-report -->` marker, so
  automation can find and update its previous comment.

```markdown
## Impact Analysis

**Risk:** CRITICAL (80/100)

**Confidence:** MEDIUM (55/100)

Comparing the merge base `0d191bb` of `0d191bb` → `54a7f81`

### Summary

- 3 changed functions
- 2 directly impacted functions
- 4 transitively impacted functions
- 5 affected packages
- 2 affected endpoints
- 1 affected worker
- 3 affected tests
- 2 package-level changes (not propagated)

### Affected endpoints

- `POST /invoices` — transitive (distance 2), handler `Handlers.CreateInvoice`
- `POST /refunds` — transitive (distance 2), handler `Handlers.Refund`

### Important impact paths

- `Handlers.CreateInvoice` → `Service.CreateInvoice` → `DB.SaveInvoice` **[changed]**
- `Reconciler.Run` → `Service.CreateInvoice` → `DB.SaveInvoice` **[changed]**

### Risk factors

- `+20` affects 2 endpoints: POST /invoices, POST /refunds
- `+12` affects 1 worker: Reconciler.Run
- ...

### Confidence factors

- `-20` 2 analysis warnings: files that cannot be parsed are missing from the analysis
- `-8` 2 interface method calls may reach affected methods (SaveRefund) through implementations that cannot be linked
- ...
```

#### CI thresholds and exit codes

By default `impact analyze` exits with 0 whenever the analysis succeeds,
whatever the scores. Thresholds are opt-in:

```sh
impact analyze . "$BASE_SHA" "$HEAD_SHA" --merge-base \
  --fail-risk 75 --fail-confidence-below 40
```

| Exit code | Meaning |
|-----------|---------|
| 0 | Analysis succeeded, no threshold violated |
| 1 | Analysis failed (not a git repository, unknown ref, ...) |
| 2 | Invalid command line |
| 3 | Risk is at or above `--fail-risk` |
| 4 | Confidence is below `--fail-confidence-below` |
| 5 | Both thresholds violated |

When a threshold is violated, the full report is still printed (in the
requested format), and the reason goes to stderr. `--fail-risk 75` fails
CRITICAL changes; `--fail-confidence-below 40` fails LOW confidence.

#### Pull requests and `--merge-base`

A pull request shows the changes of its branch since it started from the
base branch. Comparing with the current tip of the base branch is not the
same thing: if `main` moved on after the branch was created, a plain
`impact analyze . main feature` reports `main`'s new commits as reverted by
the feature branch.

`--merge-base` compares the head commit with the merge base of the two refs
(`git merge-base`), which is what GitHub's "Files changed" tab shows. In
GitHub Actions, `github.event.pull_request.base.sha` is the tip of the base
branch when the event fired, so pass the exact SHAs and add `--merge-base`:

```sh
impact analyze . "$BASE_SHA" "$HEAD_SHA" --merge-base
```

### `impact version`

Prints `impact dev` for development builds. Release builds set the version
at link time, and binaries installed with `go install ...@vX.Y.Z` report the
module version:

```sh
go build -ldflags "-X main.version=v1.0.0" -o impact ./cmd/impact
```

## GitHub Actions

`impact` does not call the GitHub API and needs no token. It produces
Markdown and JSON, and the workflow decides what to do with them.

- [`examples/github-action.yml`](examples/github-action.yml) analyzes every
  pull request, writes the Markdown report to the job summary
  (`$GITHUB_STEP_SUMMARY`), uploads both reports as an artifact, and fails
  the job when a threshold is violated. It needs only `contents: read`.
- [`examples/github-action-comment.yml`](examples/github-action-comment.yml)
  also posts the report as a pull request comment, and updates that same
  comment on later pushes, using `actions/github-script` with
  `pull-requests: write`. It is skipped for pull requests from forks, whose
  token is read-only.

The analysis step of the first example:

```yaml
- uses: actions/checkout@v4
  with:
    fetch-depth: 0 # both commits and their merge base must be available

- name: Analyze the pull request
  id: impact
  env:
    BASE_SHA: ${{ github.event.pull_request.base.sha }}
    HEAD_SHA: ${{ github.event.pull_request.head.sha }}
  run: |
    impact analyze . "$BASE_SHA" "$HEAD_SHA" --merge-base --format json > impact.json
    status=0
    impact analyze . "$BASE_SHA" "$HEAD_SHA" --merge-base --format markdown \
      --fail-risk 75 --fail-confidence-below 40 > impact.md || status=$?
    if [ "$status" -eq 1 ] || [ "$status" -eq 2 ]; then exit "$status"; fi
    echo "status=$status" >> "$GITHUB_OUTPUT"
    cat impact.md >> "$GITHUB_STEP_SUMMARY"
```

The exit code is saved instead of failing the step at once, so the report is
published first; a final step fails the job if `status` is not 0. The SHAs
are passed through environment variables rather than inlined in the script,
which is the safe way to use event data in a `run` step.

## Risk ≠ confidence

`analyze` ends with two scores from 0 to 100, which answer different
questions:

- **Risk**: how important or dangerous is this change? It grows with what
  the change reaches: endpoints, workers, packages, the number and depth of
  impacted functions, deletions, exported API.
- **Confidence**: how complete is this analysis? It starts at 100 and drops
  for blind spots that can matter for *this* change: unparsed files, calls
  through interfaces or function values that may hide callers, dynamic
  routes, unpropagated package-level changes. It is about the analysis, not
  about the quality of the code, and the size of the repository alone does
  not lower it.

They are independent, and every combination is meaningful:

| Risk | Confidence | Meaning |
|------|------------|---------|
| HIGH | HIGH | The change reaches important code, and we can see all of it. Review carefully. |
| HIGH | LOW | The detected impact is potentially serious, *and* the analysis has blind spots: the real impact may be larger. |
| LOW | HIGH | A contained change, and the analysis is likely complete. |
| LOW | LOW | Little detected impact, but blind spots: do not read "low risk" as "safe". |

Every point comes from a listed factor, so a score can always be traced back
to facts. The model is a plain weighted sum, with no LLM, history or machine
learning. Each signal is a count turned into points: `First` points for the
first occurrence, `Each` for every further one, capped at `Max`. Scores are
clamped to 0-100. All weights and thresholds are in
[`internal/scoring/weights.go`](internal/scoring/weights.go).

**Risk** starts at 0:

| Signal                                       | First | Each | Max |
|----------------------------------------------|------:|-----:|----:|
| changed functions (tests excluded)           |    +4 |   +4 | +16 |
| an exported function changed                 |    +5 |    0 |  +5 |
| deleted functions                            |    +8 |   +4 | +16 |
| affected HTTP endpoints                      |   +15 |   +5 | +30 |
| affected workers                             |   +12 |   +4 | +20 |
| affected packages beyond the first           |    +4 |   +4 | +16 |
| impacted functions (tests excluded)          |    +2 |   +2 | +16 |
| impact levels beyond distance 1              |    +4 |   +4 | +12 |
| a changed function has 5+ direct callers     |    +8 |    0 |  +8 |
| affected tests exist                         |    -5 |    0 |  -5 |

Tests lower risk only slightly: they show the change is exercised, which is
not proof of safety.

**Confidence** starts at 100:

| Signal                                                                 | First | Each | Max |
|------------------------------------------------------------------------|------:|-----:|----:|
| analysis warnings (files that cannot be parsed)                        |   -10 |  -10 | -30 |
| callers of deleted functions with no counterpart in head               |    -8 |   -8 | -24 |
| callers of deleted functions were mapped from the base commit          |    -5 |    0 |  -5 |
| interface method calls with the name of an affected method             |    -4 |   -4 | -20 |
| calls with unknown receivers with the name of an affected method       |    -3 |   -3 | -15 |
| calls through function values in affected packages                     |    -2 |   -2 | -10 |
| route registrations in affected code that are not fully resolved       |    -5 |   -5 | -15 |
| package-level changes (their users are not analyzed)                   |    -6 |   -2 | -12 |

Levels:

| Risk     | Range  | | Confidence | Range  |
|----------|--------|-|------------|--------|
| LOW      | 0-24   | | LOW        | 0-39   |
| MODERATE | 25-49  | | MEDIUM     | 40-69  |
| HIGH     | 50-74  | | HIGH       | 70-100 |
| CRITICAL | 75-100 | |            |        |

## How it works

`impact` never modifies the repository and never checks anything out: both
commits are read from git's object database (`git ls-tree`,
`git cat-file`), parsed in memory, and compared with
`git diff --unified=0`. Only read-only git commands are run. Uncommitted
changes are ignored.

```
cmd/impact/                CLI: flags, exit codes, choice of renderer
internal/model/            language-neutral data model
internal/analyzer/golang/  Go analyzer: source tree (directory or fs.FS) -> model.Repository
internal/graph/            call graph built from a model.Repository
internal/gitdiff/          git access (read-only) and unified diff parsing
internal/changes/          maps diff hunks to changed functions
internal/impact/           reverse call graph traversal from the changed functions
internal/classify/         places impacts in context: packages, endpoints, workers, tests
internal/scoring/          risk and confidence scores; weights.go holds every weight
internal/report/           output model (JSON schema) and text, JSON, Markdown renderers
internal/gittest/          test helper: throwaway git repositories
```

`impact analyze`:

```
changes.Compare      git diff + analysis of both commits -> changed functions
    ↓
impact.Analyze       reverse traversal of the call graphs -> impacted functions
    ↓
classify.Classify    packages, endpoints, workers, tests
    ↓
scoring.Assess       risk and confidence
    ↓
report.Build         output model
    ↓
report.WriteText / WriteJSON / WriteMarkdown
```

- **model**: `Repository` → `Package` → `File` → `Function` → `Call`, plus
  facts recorded by the analyzer:
  - `Function.Routes`, `Function.Test`, `Function.Exported`;
  - `Call.Mode` (`go` statements) and `Call.Reason` (why a call is
    unresolved).

  Nothing in it is Go-specific.
- **analyzer/golang**: parses files with `go/parser` and walks them with
  `go/ast`, in passes (parse, imports, declarations, package variables,
  calls). Framework knowledge (`net/http`, `testing`) lives here.
- **changes**, **impact**, **classify**, **scoring**: each step is pure
  except `changes.Compare`, which runs git. Each takes the previous results
  and knows nothing about output.
- **report**: the only place that knows about presentation. All three
  formats render the same `report.Report`, so they always agree.

### Identifiers

| Kind            | ID format                         | Example                               |
|-----------------|-----------------------------------|---------------------------------------|
| Package         | import path                       | `example.com/app/user`                |
| Function        | `<package>.<Name>`                | `example.com/app/user.CreateUser`     |
| Method          | `<package>.<Receiver>.<Name>`     | `example.com/app/user.Service.Create` |
| `init` function | `<package>.init#<n>`              | `example.com/app/user.init#0`         |

Import paths come from the nearest `go.mod`. Functions are matched between
the two commits by ID: a function moved to another file of the same package
is changed, not deleted and added.

### How calls are resolved

The analyzer does not type-check, so it never needs to build the code or
download dependencies. It resolves calls from the shape of the expression:

- `Foo()`: a function in the same package;
- `pkg.Foo()`: a function in an imported package (aliases supported);
- `x.Method()`: a method of the type of `x`. The type is inferred from:
  - a receiver or parameter, `var x T`, `T{}` / `&T{}`, `new(T)`, `x.(T)`;
  - the declared result type of a called function;
  - a struct field reached through any of these.

  Embedded types and type aliases are followed.

Builtins and type conversions are not calls. Calls inside closures belong to
the enclosing function.

### Impact traversal

A breadth-first search walks the call graph backwards from all changed
functions at once. The level at which a function is first reached is its
distance, and functions already reached are never visited again, so cycles
terminate. Only calls between functions of the repository propagate impact.

A deleted function no longer exists in head, so its callers come from the
base commit's graph. They are followed further only if a function with the
same ID exists in head. Other callers are listed as unmapped.

### How context is detected

Context comes only from what the code states explicitly. Nothing is
inferred from names, and a context is left out rather than guessed:

- **Routes**: `http.HandleFunc`, `http.Handle`, and the same two methods on
  a value known to be an `*http.ServeMux` (`http.NewServeMux()`,
  `&http.ServeMux{}`, `http.DefaultServeMux`, or a variable, field or
  parameter of that type).
  - The pattern must be a string literal, or literals joined with `+`.
    Go 1.22 patterns (`"POST /payments/{id}"`) give the method.
  - The handler must resolve to a function of the repository: a function or
    method value, `http.HandlerFunc(f)`, or for `Handle` a value whose type
    has a `ServeHTTP` method.
  - A registration that cannot be resolved is never reported as an
    endpoint. When it concerns affected code, it lowers confidence instead.
- **Workers**: `go` statements. In `go s.worker().Run()`, only `Run` is the
  worker; `worker()` and the arguments are evaluated by the caller.
- **Tests**: the go command's rules: a `_test.go` file, the right name
  prefix, a single `*testing.T`, `*testing.B` or `*testing.F` parameter
  (none for examples), and no results. `TestMain` is not a test.

## Development

```sh
go test ./...   # the gitdiff, changes and CLI tests use temporary git repositories
go vet ./...
gofmt -l .
```

[`.github/workflows/ci.yml`](.github/workflows/ci.yml) runs formatting, vet,
the tests and a build on Go 1.22 and on the latest Go. The tests need git,
but no network access.

## Limitations

General:

- Only Go is supported.
- Build constraints are ignored except `//go:build ignore`. A function
  declared in several platform-specific files is one function.
- `vendor`, `testdata`, and directories starting with `.` or `_` are skipped.
- The whole repository is analyzed at both commits, even when only a few
  files changed.

Call graph:

- No type checking, so these calls are unresolved and do not propagate
  impact:
  - calls through interfaces or function values;
  - methods on values from slices, maps, channels or `range`;
  - methods on results of external functions.

  The confidence score accounts for the unresolved calls that can matter.
- Scopes are flattened per function, which can mis-resolve shadowed names.
- Dot imports are not supported. Calls in package-level variable
  initializers are not recorded.

Changes:

- Results follow git's line diff. When equal lines can be aligned in several
  ways, git's choice decides which function a change lands in.
- Any change inside a function counts, including comments and whitespace.
  Converting a file's line endings (CRLF to LF) changes every function in
  it.
- Package-level changes (types, constants, variables) are reported as line
  ranges and not propagated to their users.
- Without `--merge-base`, the two commits are compared directly.

Context:

- Only `net/http` routes are detected: no Gin, Echo, Chi, gRPC or other
  frameworks.
- These routes are missed:
  - inline function literal handlers;
  - handlers wrapped in middleware returning `http.Handler`;
  - patterns from named constants;
  - custom router types.
- Workers are only `go` statements with a resolvable target. Goroutines
  started through function values, `errgroup` or worker pools are missed,
  and tickers or cron jobs are not recognized.

Scoring:

- The weights are reasoned defaults, not calibrated against real incidents.
  The scores rank and explain changes; they do not predict failures.
- Risk knows what a change reaches, not what it does: a behaviour change and
  a log message change in the same function score the same.
- Hidden callers are matched by method name only, so confidence can drop
  for an interface that could never hold the changed type.

Output:

- Markdown lists are truncated to `--max-items`. JSON is always complete,
  and very large JSON reports are not paginated.
- GitHub comments are limited to 65,536 characters. With the default limits
  the Markdown report stays far below that.

## Status

The MVP is complete:

1. **Repository analysis**: packages, files, functions, call graph.
2. **Git diff analysis**: changed, added and deleted functions between two
   refs.
3. **Impact engine**: direct and transitive callers, distances and paths.
4. **Context detection**: packages, `net/http` endpoints, workers, tests.
5. **Risk and confidence scoring**: two independent, explainable scores.
6. **PR integration**: JSON and Markdown output, CI thresholds, GitHub
   Actions examples.
