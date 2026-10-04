package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/Easlkad/impact/internal/analyzer/golang"
	"github.com/Easlkad/impact/internal/graph"
	"github.com/Easlkad/impact/internal/model"
	"github.com/Easlkad/impact/internal/report"
)

func runScan(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	fs.SetOutput(stderr)
	sample := fs.Int("sample", 10, "number of call relationships to print (0 to print none)")
	tests := fs.Bool("tests", false, "also scan _test.go files")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: impact scan [flags] <repository-path>")
		fmt.Fprintln(stderr)
		fmt.Fprintln(stderr, "Flags:")
		fs.PrintDefaults()
	}

	paths, err := parseArgs(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		return 2
	}
	if len(paths) != 1 {
		fs.Usage()
		return 2
	}

	repo, err := golang.Analyze(paths[0], golang.Options{IncludeTests: *tests})
	if err != nil {
		fmt.Fprintf(stderr, "impact: %v\n", err)
		return 1
	}
	for _, w := range repo.Warnings {
		fmt.Fprintf(stderr, "warning: %s\n", w)
	}
	if err := printSummary(stdout, repo, graph.Build(repo), *sample); err != nil {
		fmt.Fprintf(stderr, "impact: writing the summary: %v\n", err)
		return 1
	}
	return 0
}

// parseArgs parses flags placed before or after the positional arguments, so
// both "impact scan -sample 5 ." and "impact scan . -sample 5" work.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return positional, nil
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}

// printSummary prints the summary of a scan and returns the first write
// error.
func printSummary(out io.Writer, repo *model.Repository, g *graph.Graph, sample int) error {
	w := &errWriter{w: out}
	var functions, methods, external, unresolved int
	for _, fn := range repo.Functions() {
		if fn.IsMethod() {
			methods++
		} else {
			functions++
		}
		for _, c := range fn.Calls {
			switch c.Kind {
			case model.CallExternal:
				external++
			case model.CallUnresolved:
				unresolved++
			}
		}
	}
	edges := g.Edges()

	fmt.Fprintf(w, "Repository scanned: %s\n\n", repo.Root)
	fmt.Fprintf(w, "Packages: %d\n", len(repo.Packages))
	fmt.Fprintf(w, "Files: %d\n", repo.FileCount())
	fmt.Fprintf(w, "Functions: %d\n", functions)
	fmt.Fprintf(w, "Methods: %d\n", methods)
	fmt.Fprintf(w, "Call relationships: %d\n", len(edges))
	fmt.Fprintf(w, "External call sites: %d\n", external)
	fmt.Fprintf(w, "Unresolved call sites: %d\n", unresolved)

	n := min(sample, len(edges))
	if n <= 0 {
		return w.err
	}
	labels := packageLabels(repo)
	name := func(id string) string {
		fn := g.Function(id)
		return labels[fn.Package] + "." + fn.QualifiedName()
	}
	fmt.Fprintf(w, "\nSample call relationships (%d of %d):\n", n, len(edges))
	for _, e := range edges[:n] {
		fmt.Fprintf(w, "  %s -> %s\n", name(e.From), name(e.To))
	}
	return w.err
}

// packageLabels returns the packageLabel of each package, by package ID.
func packageLabels(repo *model.Repository) map[string]string {
	labels := make(map[string]string, len(repo.Packages))
	for _, p := range repo.Packages {
		labels[p.ID] = packageLabel(p)
	}
	return labels
}

// packageLabel returns a short display label for a package, such as
// "internal/user".
func packageLabel(p *model.Package) string { return report.PackageLabel(p.Dir, p.Name) }
