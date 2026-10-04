// Package scoring turns the facts of an impact analysis into two separate,
// explainable scores:
//
//   - Risk: how important or dangerous the change potentially is.
//   - Confidence: how complete and correct the impact analysis is likely
//     to be. It is about the analysis, not about the quality of the code.
//
// The two are independent: a change can be risky while the analysis has
// blind spots, and the other way round. Both are integers from 0 to 100,
// and every point comes from a Factor that says why. The model is a plain
// weighted sum; all weights are in weights.go.
package scoring

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Easlkad/impact/internal/changes"
	"github.com/Easlkad/impact/internal/classify"
	"github.com/Easlkad/impact/internal/impact"
	"github.com/Easlkad/impact/internal/model"
)

// Input is everything the scores are computed from: the results of the
// earlier analysis steps for one comparison of two commits.
type Input struct {
	Changes *changes.Report  // includes the base and head analyses
	Impact  *impact.Result   // computed from Changes
	Context *classify.Result // computed from Impact
}

// Assessment holds the two scores of a change.
type Assessment struct {
	Risk       Score
	Confidence Score
}

// Score is a value from 0 to 100, its level, and the factors that make it.
type Score struct {
	Value   int
	Level   string
	Factors []Factor // in a fixed order; factors without effect are omitted
}

// Factor is one reason for a score.
type Factor struct {
	Name   string // stable identifier, such as "endpoints"
	Delta  int    // points added (positive) or removed (negative)
	Reason string // human-readable explanation
}

// Assess computes the risk and confidence scores.
func Assess(in Input) Assessment {
	return Assessment{Risk: risk(in), Confidence: confidence(in)}
}

func risk(in Input) Score {
	var f factors
	c := in.Context

	var changedNames, exported []string
	deleted := 0
	for _, imp := range c.Changed {
		if imp.Function.Test != model.NotTest {
			continue // changing a test does not make a change risky
		}
		changedNames = append(changedNames, imp.Function.QualifiedName())
		if imp.Function.Exported {
			exported = append(exported, imp.Function.QualifiedName())
		}
		if imp.Change == changes.Deleted {
			deleted++
		}
	}
	f.add("changed-functions", RiskChangedFunctions.Points(len(changedNames)),
		"%s changed: %s", count(len(changedNames), "function"), list(changedNames))
	f.add("exported-change", RiskExportedChange.Points(len(exported)),
		"exported API changed: %s", list(exported))
	f.add("deleted-functions", RiskDeletedFunctions.Points(deleted),
		"%s deleted", count(deleted, "function"))

	var endpoints []string
	for _, e := range c.Endpoints {
		endpoints = append(endpoints, e.Method+" "+e.Path)
	}
	f.add("endpoints", RiskEndpoints.Points(len(endpoints)),
		"affects %s: %s", count(len(endpoints), "endpoint"), list(endpoints))

	var workers []string
	for _, w := range c.Workers {
		workers = append(workers, w.Function.Function.QualifiedName())
	}
	f.add("workers", RiskWorkers.Points(len(workers)),
		"affects %s: %s", count(len(workers), "worker"), list(workers))

	f.add("packages", RiskExtraPackages.Points(len(c.Packages)-1),
		"impact reaches %d packages", len(c.Packages))

	impacted := len(c.Direct) + len(c.Transitive)
	f.add("impacted-functions", RiskImpactedFunctions.Points(impacted),
		"%s impacted (%d direct, %d transitive)", count(impacted, "function"), len(c.Direct), len(c.Transitive))

	depth := 0
	for _, imp := range c.Transitive {
		depth = max(depth, imp.Distance)
	}
	f.add("depth", RiskExtraDepth.Points(depth-1), "impact reaches distance %d", depth)

	if name, callers := busiestChange(c); callers >= RiskFanInThreshold {
		f.add("fan-in", RiskHighFanIn.Points(1), "%s has %d direct callers", name, callers)
	}

	f.add("tests", -RiskAffectedTests.Points(len(c.Tests)),
		"%s exercising the change (not proof of safety)", count(len(c.Tests), "affected test"))

	return f.score(0, RiskLevels)
}

// busiestChange returns the changed function (tests excluded) with the most
// direct callers that are not tests, and that number.
func busiestChange(c *classify.Result) (string, int) {
	callers := make(map[string]int)
	for _, imp := range c.Direct {
		for _, id := range imp.CausedBy {
			callers[id]++
		}
	}
	name, most := "", 0
	for _, imp := range c.Changed { // sorted, so ties resolve the same way every time
		if imp.Function.Test == model.NotTest && callers[imp.ID()] > most {
			name, most = imp.Function.QualifiedName(), callers[imp.ID()]
		}
	}
	return name, most
}

// confidence starts high and only loses points for blind spots that can
// matter for this change: the signals are restricted to the changed and
// impacted code, so the size of the repository alone has no effect.
func confidence(in Input) Score {
	var f factors
	report, r := in.Changes, in.Impact
	head := report.HeadAnalysis

	warnings := 0
	if head != nil {
		warnings = len(head.Warnings)
	}
	for _, w := range report.Warnings {
		// Head warnings are counted above; diff warnings are about Go files
		// whose changed lines git did not report.
		if strings.HasPrefix(w, "base: ") || strings.HasPrefix(w, "diff: ") {
			warnings++
		}
	}
	f.add("analysis-warnings", -ConfidenceAnalysisWarnings.Points(warnings),
		"%s: files that cannot be parsed are missing from the analysis", count(warnings, "analysis warning"))

	f.add("unmapped-callers", -ConfidenceUnmappedCallers.Points(len(r.Unmapped)),
		"%s of deleted functions without a counterpart in head, not followed", count(len(r.Unmapped), "caller"))

	deletedWithCallers := 0
	for _, imp := range r.Changed {
		if imp.Change == changes.Deleted && reachesAny(r, imp.ID()) {
			deletedWithCallers++
		}
	}
	f.add("deleted-mapping", -ConfidenceDeletedMapping.Points(deletedWithCallers),
		"callers of %s were taken from the base commit and matched to head by ID", count(deletedWithCallers, "deleted function"))

	if head != nil {
		hidden := hiddenCallers(r, head)
		f.add("interface-calls", -ConfidenceInterfaceCalls.Points(hidden.interfaceCalls),
			"%s may reach affected methods (%s) through implementations that cannot be linked",
			count(hidden.interfaceCalls, "interface method call"), list(hidden.interfaceNames))
		f.add("unknown-receiver-calls", -ConfidenceUnknownTargetCalls.Points(hidden.unknownCalls),
			"%s with unknown targets may reach affected functions (%s)",
			count(hidden.unknownCalls, "call"), list(hidden.unknownNames))

		values := functionValueCalls(r, head)
		f.add("function-values", -ConfidenceFunctionValueCalls.Points(values),
			"%s through function values in affected packages cannot be followed", count(values, "call"))

		routes := incompleteRoutes(r, head)
		f.add("incomplete-routes", -ConfidenceIncompleteRoutes.Points(routes),
			"%s in affected code could not be fully resolved (dynamic pattern or handler)", count(routes, "route registration"))
	}

	f.add("package-level", -ConfidencePackageLevel.Points(len(report.PackageLevel)),
		"%s, whose users are not analyzed", count(len(report.PackageLevel), "package-level change"))

	return f.score(ConfidenceStart, ConfidenceLevels)
}

// reachesAny reports whether any function is impacted by the changed
// function id.
func reachesAny(r *impact.Result, id string) bool {
	for _, imp := range r.Impacted {
		if slices.Contains(imp.Roots, id) {
			return true
		}
	}
	return false
}

type hiddenCallerCounts struct {
	interfaceCalls, unknownCalls int
	interfaceNames, unknownNames []string
}

// hiddenCallers counts the unresolved calls in head that may call an
// affected function: method calls whose method name is the name of a
// changed or impacted method, and unqualified calls (from a dot import,
// for instance) whose name is the name of a changed or impacted function.
// Each is a caller the call graph may be missing.
//
// Deleted functions are left out of the unqualified names: a head call
// by the name of a deleted function of the same package is one of its
// former callers, which are taken from the base commit.
func hiddenCallers(r *impact.Result, head *model.Repository) hiddenCallerCounts {
	methods := make(map[string]bool) // method names
	funcs := make(map[string]bool)   // names of functions other than methods
	for _, imp := range append(slices.Clone(r.Changed), r.Impacted...) {
		switch {
		case imp.Function.IsMethod():
			methods[imp.Function.Name] = true
		case imp.Change != changes.Deleted:
			funcs[imp.Function.Name] = true
		}
	}
	var h hiddenCallerCounts
	for _, fn := range head.Functions() {
		for _, call := range fn.Calls {
			if call.Kind != model.CallUnresolved || call.Reason == model.FunctionValue {
				continue
			}
			i := strings.LastIndex(call.Callee, ".")
			name := call.Callee[i+1:]
			if i < 0 && !funcs[name] || i >= 0 && !methods[name] {
				continue
			}
			if call.Reason == model.InterfaceMethod {
				h.interfaceCalls++
				h.interfaceNames = appendUnique(h.interfaceNames, name)
			} else {
				h.unknownCalls++
				h.unknownNames = appendUnique(h.unknownNames, name)
			}
		}
	}
	slices.Sort(h.interfaceNames)
	slices.Sort(h.unknownNames)
	return h
}

// functionValueCalls counts the calls through function values in the
// packages of changed and impacted functions: any of them may call
// affected code without the call graph knowing.
func functionValueCalls(r *impact.Result, head *model.Repository) int {
	affected := make(map[string]bool) // package IDs
	for _, imp := range append(slices.Clone(r.Changed), r.Impacted...) {
		affected[imp.Function.Package] = true
	}
	n := 0
	for _, fn := range head.Functions() {
		if !affected[fn.Package] {
			continue
		}
		for _, call := range fn.Calls {
			if call.Kind == model.CallUnresolved && call.Reason == model.FunctionValue {
				n++
			}
		}
	}
	return n
}

// incompleteRoutes counts the route registrations in head that were not
// fully understood and may concern affected code: a known handler that is
// affected but registered with a dynamic pattern, or an unknown handler
// registered by an affected function (inline handlers' calls are attributed
// to the function registering them).
func incompleteRoutes(r *impact.Result, head *model.Repository) int {
	n := 0
	for _, fn := range head.Functions() {
		for _, route := range fn.Routes {
			switch {
			case route.Complete():
			case route.Handler != "" && r.Lookup(route.Handler) != nil:
				n++
			case route.Handler == "" && r.Lookup(fn.ID) != nil:
				n++
			}
		}
	}
	return n
}

// factors accumulates the factors of one score.
type factors []Factor

// add records a factor, unless its delta is zero.
func (f *factors) add(name string, delta int, format string, args ...any) {
	if delta != 0 {
		*f = append(*f, Factor{Name: name, Delta: delta, Reason: fmt.Sprintf(format, args...)})
	}
}

// score adds the deltas to start, clamps the sum to 0-100 and names its level.
func (f factors) score(start int, levels []Level) Score {
	v := start
	for _, factor := range f {
		v += factor.Delta
	}
	v = min(max(v, 0), 100)
	s := Score{Value: v, Factors: f}
	for _, l := range levels {
		if v >= l.Min {
			s.Level = l.Name
			break
		}
	}
	return s
}

// count formats "1 function" or "3 functions".
func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// listLimit is the number of names a factor reason shows before "and N more".
const listLimit = 3

// list joins names, showing at most listLimit of them.
func list(names []string) string {
	if len(names) <= listLimit {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:listLimit], ", "), len(names)-listLimit)
}

func appendUnique(names []string, name string) []string {
	if slices.Contains(names, name) {
		return names
	}
	return append(names, name)
}
