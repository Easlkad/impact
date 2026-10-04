// Command impact analyzes a code repository to find what may be affected by
// a change. It currently scans Go repositories, builds their call graph,
// reports the functions changed between two git refs, and finds the
// functions that call them.
package main

import (
	"fmt"
	"io"
	"os"
)

const usage = `Usage:
  impact <command> [arguments]

Commands:
  scan    Scan a local Go repository and summarize its packages, functions and calls
  diff    Report the Go files and functions changed between two git refs
  analyze Report the changed functions, their impact, its context, and risk
          and confidence scores (text, JSON or Markdown)
  version Print the version

Run "impact <command> -h" for the usage of a command.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes the command line args and returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "scan":
		return runScan(args[1:], stdout, stderr)
	case "diff":
		return runDiff(args[1:], stdout, stderr)
	case "analyze":
		return runAnalyze(args[1:], stdout, stderr)
	case "version", "-version", "--version":
		return runVersion(stdout, stderr)
	case "help", "-h", "-help", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "impact: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

// errWriter writes to w and keeps the first error, after which it writes
// nothing more: a renderer can print freely and check err once at the end.
type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) Write(p []byte) (int, error) {
	if e.err != nil {
		return 0, e.err
	}
	var n int
	n, e.err = e.w.Write(p)
	return n, e.err
}
