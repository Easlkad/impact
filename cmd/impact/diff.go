package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/Easlkad/impact/internal/changes"
	"github.com/Easlkad/impact/internal/gitdiff"
)

func runDiff(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	mergeBase := fs.Bool("merge-base", false, "compare head with the merge base of the two refs, as a pull request diff does")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: impact diff [flags] <repository-path> <base-ref> <head-ref>")
		fmt.Fprintln(stderr)
		fmt.Fprintln(stderr, "Reports the Go files and functions that changed between two git refs.")
		fmt.Fprintln(stderr)
		fmt.Fprintln(stderr, "Flags:")
		fs.PrintDefaults()
	}

	positional, err := parseArgs(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		return 2
	}
	if len(positional) != 3 {
		fs.Usage()
		return 2
	}

	report, err := changes.Compare(positional[0], positional[1], positional[2], changes.Options{MergeBase: *mergeBase})
	if err != nil {
		fmt.Fprintf(stderr, "impact: %v\n", err)
		return 1
	}
	for _, w := range report.Warnings {
		fmt.Fprintf(stderr, "warning: %s\n", w)
	}
	if err := printReport(stdout, report); err != nil {
		fmt.Fprintf(stderr, "impact: writing the report: %v\n", err)
		return 1
	}
	return 0
}

// printReport prints the changes and returns the first write error.
func printReport(out io.Writer, r *changes.Report) error {
	w := &errWriter{w: out}
	base := short(r.Base.Hash)
	if r.MergeBase {
		base = "merge base " + base
	}
	fmt.Fprintf(w, "Comparing %s (%s) -> %s (%s)\n\n", r.Base.Ref, base, r.Head.Ref, short(r.Head.Hash))
	if len(r.Files) == 0 {
		fmt.Fprintln(w, "No Go files changed.")
		return w.err
	}

	fmt.Fprintln(w, "Changed files:")
	for _, f := range r.Files {
		if f.Status == gitdiff.Renamed || f.Status == gitdiff.Copied {
			fmt.Fprintf(w, "  %-9s %s -> %s\n", f.Status, f.OldPath, f.NewPath)
		} else {
			fmt.Fprintf(w, "  %-9s %s\n", f.Status, f.Path())
		}
	}

	printFunctions(w, "Changed functions", r.Functions, changes.Modified)
	printFunctions(w, "Added functions", r.Functions, changes.Added)
	printFunctions(w, "Deleted functions", r.Functions, changes.Deleted)

	fmt.Fprintln(w, "\nPackage-level changes:")
	printPackageLevel(w, r.PackageLevel)
	return w.err
}

func printPackageLevel(w io.Writer, lines []changes.LineChange) {
	if len(lines) == 0 {
		fmt.Fprintln(w, "  (none)")
	}
	for _, c := range lines {
		if c.Removed {
			fmt.Fprintf(w, "  %s (removed; base line numbers)\n", c)
		} else {
			fmt.Fprintf(w, "  %s\n", c)
		}
	}
}

// printFunctions prints the functions of one kind, grouped by file. The
// changes are sorted by file, so each group is contiguous.
func printFunctions(w io.Writer, title string, fns []changes.FunctionChange, kind changes.Kind) {
	fmt.Fprintf(w, "\n%s:\n", title)
	file, none := "", true
	for _, c := range fns {
		if c.Kind != kind {
			continue
		}
		if c.Declaration.File != file {
			if !none {
				fmt.Fprintln(w)
			}
			file = c.Declaration.File
			fmt.Fprintf(w, "  %s\n", file)
		}
		fmt.Fprintf(w, "    %s\n", c.Function.QualifiedName())
		none = false
	}
	if none {
		fmt.Fprintln(w, "  (none)")
	}
}

func short(hash string) string {
	if len(hash) > 7 {
		return hash[:7]
	}
	return hash
}
