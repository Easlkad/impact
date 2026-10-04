package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/Easlkad/impact/internal/changes"
	"github.com/Easlkad/impact/internal/classify"
	"github.com/Easlkad/impact/internal/impact"
	"github.com/Easlkad/impact/internal/report"
	"github.com/Easlkad/impact/internal/scoring"
)

// Exit codes of the CLI. Codes 3 to 5 are only used by "impact analyze"
// when a --fail-* threshold is set and violated; the report is printed
// in full before exiting.
const (
	exitOK                  = 0
	exitError               = 1 // the analysis failed
	exitUsage               = 2 // invalid command line
	exitRiskThreshold       = 3 // risk >= --fail-risk
	exitConfidenceThreshold = 4 // confidence < --fail-confidence-below
	exitBothThresholds      = 5 // both of the above
)

const analyzeUsage = `Usage: impact analyze [flags] <repository-path> <base-ref> <head-ref>

Reports the functions changed between two git refs, the functions that call
them directly or transitively, the affected packages, HTTP endpoints, workers
and tests, and a risk and a confidence score.

Exit codes:
  0  analysis succeeded, no threshold violated
  1  analysis failed
  2  invalid command line
  3  risk is at or above --fail-risk
  4  confidence is below --fail-confidence-below
  5  both thresholds violated

Flags:
`

func runAnalyze(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("analyze", flag.ContinueOnError)
	fs.SetOutput(stderr)
	format := fs.String("format", "text", "output format: text, json or markdown")
	maxPaths := fs.Int("paths", 10, "number of impact paths to show in text and markdown output (0 for none)")
	maxItems := fs.Int("max-items", 20, "maximum items per list in markdown output (0 for no limit); JSON is always complete")
	failRisk := fs.Int("fail-risk", -1, "exit with code 3 if risk is at least this value, 0-100 (default: never)")
	failConfidence := fs.Int("fail-confidence-below", -1, "exit with code 4 if confidence is below this value, 0-100 (default: never)")
	mergeBase := fs.Bool("merge-base", false, "compare head with the merge base of the two refs, as a pull request diff does")
	fs.Usage = func() {
		fmt.Fprint(stderr, analyzeUsage)
		fs.PrintDefaults()
	}

	positional, err := parseArgs(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		return exitOK
	}
	if err != nil {
		return exitUsage
	}
	if len(positional) != 3 {
		fs.Usage()
		return exitUsage
	}
	if *format != "text" && *format != "json" && *format != "markdown" {
		fmt.Fprintf(stderr, "impact: unknown format %q (want text, json or markdown)\n", *format)
		return exitUsage
	}
	// -1, the default, means the threshold is not set.
	if *failRisk < -1 || *failRisk > 100 || *failConfidence < -1 || *failConfidence > 100 {
		fmt.Fprintln(stderr, "impact: -fail-risk and -fail-confidence-below must be between 0 and 100")
		return exitUsage
	}
	if *maxItems < 0 || *maxPaths < 0 {
		fmt.Fprintln(stderr, "impact: -max-items and -paths must not be negative")
		return exitUsage
	}

	ch, err := changes.Compare(positional[0], positional[1], positional[2], changes.Options{MergeBase: *mergeBase})
	if err != nil {
		fmt.Fprintf(stderr, "impact: %v\n", err)
		return exitError
	}
	for _, w := range ch.Warnings {
		fmt.Fprintf(stderr, "warning: %s\n", w)
	}
	r := impact.FromReport(ch)
	c := classify.Classify(r, ch.BaseAnalysis, ch.HeadAnalysis)
	out := report.Build(report.Input{
		Changes:    ch,
		Impact:     r,
		Context:    c,
		Assessment: scoring.Assess(scoring.Input{Changes: ch, Impact: r, Context: c}),
		Version:    toolVersion(),
	})

	switch *format {
	case "json":
		err = report.WriteJSON(stdout, out)
	case "markdown":
		err = report.WriteMarkdown(stdout, out, report.MarkdownOptions{MaxItems: *maxItems, MaxPaths: *maxPaths})
	default:
		err = report.WriteText(stdout, out, report.TextOptions{MaxPaths: *maxPaths})
	}
	if err != nil {
		fmt.Fprintf(stderr, "impact: writing the report: %v\n", err)
		return exitError
	}
	return checkThresholds(out, *failRisk, *failConfidence, stderr)
}

// checkThresholds returns the exit code for the CI thresholds, reporting
// each violation on stderr. A negative threshold is not set.
func checkThresholds(r *report.Report, failRisk, failConfidenceBelow int, stderr io.Writer) int {
	riskFailed := failRisk >= 0 && r.Risk.Value >= failRisk
	confidenceFailed := failConfidenceBelow >= 0 && r.Confidence.Value < failConfidenceBelow
	if riskFailed {
		fmt.Fprintf(stderr, "impact: risk %s (%d) is at or above the -fail-risk threshold %d\n", r.Risk.Level, r.Risk.Value, failRisk)
	}
	if confidenceFailed {
		fmt.Fprintf(stderr, "impact: confidence %s (%d) is below the -fail-confidence-below threshold %d\n", r.Confidence.Level, r.Confidence.Value, failConfidenceBelow)
	}
	switch {
	case riskFailed && confidenceFailed:
		return exitBothThresholds
	case riskFailed:
		return exitRiskThreshold
	case confidenceFailed:
		return exitConfidenceThreshold
	}
	return exitOK
}
