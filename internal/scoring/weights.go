package scoring

// Every number of the scoring model is defined in this file.
//
// A signal is counted (endpoints affected, packages reached, ...) and its
// count is turned into points by a Weight: First points for the first
// occurrence, Each for every further one, and never more than Max. Caps keep
// any single signal from dominating a score.

// Weight turns the count of a signal into points.
type Weight struct {
	First int // points for the first occurrence
	Each  int // points for every further occurrence
	Max   int // upper bound
}

// Points returns the points for n occurrences.
func (w Weight) Points(n int) int {
	if n <= 0 {
		return 0
	}
	return min(w.First+w.Each*(n-1), w.Max)
}

// Risk starts at 0. These weights are added, except RiskAffectedTests,
// which is subtracted.
var (
	RiskChangedFunctions  = Weight{First: 4, Each: 4, Max: 16}  // per changed function (tests excluded)
	RiskExportedChange    = Weight{First: 5, Each: 0, Max: 5}   // if an exported function changed
	RiskDeletedFunctions  = Weight{First: 8, Each: 4, Max: 16}  // per deleted function
	RiskEndpoints         = Weight{First: 15, Each: 5, Max: 30} // per affected HTTP endpoint
	RiskWorkers           = Weight{First: 12, Each: 4, Max: 20} // per affected worker
	RiskExtraPackages     = Weight{First: 4, Each: 4, Max: 16}  // per affected package beyond the first
	RiskImpactedFunctions = Weight{First: 2, Each: 2, Max: 16}  // per impacted function (tests excluded)
	RiskExtraDepth        = Weight{First: 4, Each: 4, Max: 12}  // per level of impact beyond distance 1
	RiskHighFanIn         = Weight{First: 8, Each: 0, Max: 8}   // if a changed function has many direct callers
	RiskAffectedTests     = Weight{First: 5, Each: 0, Max: 5}   // subtracted if affected tests exist
)

// RiskFanInThreshold is the number of direct callers from which a changed
// function counts as heavily used.
const RiskFanInThreshold = 5

// Confidence starts at ConfidenceStart. These weights are subtracted.
const ConfidenceStart = 100

var (
	ConfidenceAnalysisWarnings   = Weight{First: 10, Each: 10, Max: 30} // per analysis warning (unparsed file, ...)
	ConfidenceUnmappedCallers    = Weight{First: 8, Each: 8, Max: 24}   // per caller of a deleted function missing from head
	ConfidenceDeletedMapping     = Weight{First: 5, Each: 0, Max: 5}    // if callers of deleted functions came from the base commit
	ConfidenceInterfaceCalls     = Weight{First: 4, Each: 4, Max: 20}   // per interface call that may reach an affected method
	ConfidenceUnknownTargetCalls = Weight{First: 3, Each: 3, Max: 15}   // per call with an unknown receiver that may reach an affected method
	ConfidenceFunctionValueCalls = Weight{First: 2, Each: 2, Max: 10}   // per call through a function value in an affected package
	ConfidenceIncompleteRoutes   = Weight{First: 5, Each: 5, Max: 15}   // per route registration in affected code that is not fully understood
	ConfidencePackageLevel       = Weight{First: 6, Each: 2, Max: 12}   // per package-level change, which is not propagated
)

// A Level names a range of scores: a score gets the first level whose
// minimum it reaches. Lists are ordered from the highest minimum down.
type Level struct {
	Min  int
	Name string
}

var RiskLevels = []Level{
	{Min: 75, Name: "CRITICAL"},
	{Min: 50, Name: "HIGH"},
	{Min: 25, Name: "MODERATE"},
	{Min: 0, Name: "LOW"},
}

var ConfidenceLevels = []Level{
	{Min: 70, Name: "HIGH"},
	{Min: 40, Name: "MEDIUM"},
	{Min: 0, Name: "LOW"},
}
