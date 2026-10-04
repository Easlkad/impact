package report

import (
	"fmt"
	"io"
	"strings"
)

// TextOptions controls the text rendering.
type TextOptions struct {
	MaxPaths int // impact paths to print; 0 prints none
}

// WriteText renders the report for a terminal.
func WriteText(w io.Writer, r *Report, opts TextOptions) error {
	p := &printer{w: w}
	p.text(r, opts)
	return p.err
}

// printer writes formatted output and keeps the first error.
type printer struct {
	w   io.Writer
	err error
}

func (p *printer) printf(format string, args ...any) {
	if p.err == nil {
		_, p.err = fmt.Fprintf(p.w, format, args...)
	}
}

func (p *printer) line(s string) { p.printf("%s\n", s) }

func (p *printer) text(r *Report, opts TextOptions) {
	p.printf("Comparing %s -> %s\n\n", commitLabel(r.Base, r.MergeBase), commitLabel(r.Head, false))
	if len(r.ChangedFiles) == 0 {
		p.line("No Go files changed.")
		return
	}

	p.line("Changed:")
	p.functions(r.Changed, func(f Function) string { return "[" + f.Change + "]" })

	p.line("\nDirect impact:")
	p.functions(r.DirectImpact, func(f Function) string {
		names := make([]string, len(f.CausedBy))
		for i, id := range f.CausedBy {
			names[i] = r.lookup(id).Name
		}
		return "(calls " + strings.Join(names, ", ") + ")"
	})

	p.line("\nTransitive impact:")
	p.functions(r.TransitiveImpact, func(f Function) string { return fmt.Sprintf("(distance %d)", f.Distance) })

	// Direct impacts explain themselves above; paths explain the others.
	if opts.MaxPaths > 0 && len(r.TransitiveImpact) > 0 {
		p.line("\nImpact paths:")
		for n, f := range r.TransitiveImpact {
			if n > 0 {
				p.line("")
			}
			if n == opts.MaxPaths {
				p.printf("  ... %d more (use -paths to show more)\n", len(r.TransitiveImpact)-n)
				break
			}
			for k, id := range f.Path {
				step := r.lookup(id)
				s := fmt.Sprintf("%s (%s)", step.Name, step.File)
				if step.Distance == 0 {
					s += " [changed]"
				}
				if k == 0 {
					p.printf("  %s\n", s)
				} else {
					p.printf("    -> %s\n", s)
				}
			}
		}
	}

	if len(r.UnmappedCallers) > 0 {
		p.line("\nCallers of deleted functions with no counterpart in head (not followed):")
		for _, id := range r.UnmappedCallers {
			p.printf("  %s\n", id)
		}
	}

	p.line("\nPackage-level changes (not propagated):")
	p.packageLevel(r.PackageLevelChanges)

	p.context(r)

	p.line("\nSummary:")
	p.printf("  Changed functions: %d\n", r.Summary.Changed)
	p.printf("  Direct impact: %d\n", r.Summary.DirectImpact)
	p.printf("  Transitive impact: %d\n", r.Summary.TransitiveImpact)
	p.printf("  Packages: %d\n", r.Summary.Packages)
	p.printf("  Endpoints: %d\n", r.Summary.Endpoints)
	p.printf("  Workers: %d\n", r.Summary.Workers)
	p.printf("  Tests: %d\n", r.Summary.Tests)

	p.line("\nAssessment:")
	p.printf("  Risk: %s (%d/100)\n", r.Risk.Level, r.Risk.Value)
	p.printf("  Confidence: %s (%d/100)\n", r.Confidence.Level, r.Confidence.Value)
	p.line("\nRisk factors:")
	p.factors(r.Risk.Factors, "no risk factors")
	p.line("\nConfidence factors:")
	p.factors(r.Confidence.Factors, "no known blind spots in the affected code")
}

// commitLabel describes a compared commit: "main (1a2b3c4)", just
// "1a2b3c4" when the ref is that hash, or for a merge base
// "main (merge base 1a2b3c4)".
func commitLabel(c Commit, mergeBase bool) string {
	switch {
	case mergeBase:
		return fmt.Sprintf("%s (merge base %s)", refLabel(c.Ref), shortHash(c.Commit))
	case isHash(c.Ref):
		return shortHash(c.Commit)
	}
	return fmt.Sprintf("%s (%s)", c.Ref, shortHash(c.Commit))
}

// functions prints functions under a heading per file, each followed by a
// note. Lists sorted by distance before file can repeat a file heading.
func (p *printer) functions(fns []Function, note func(Function) string) {
	if len(fns) == 0 {
		p.line("  (none)")
		return
	}
	file := ""
	for i, f := range fns {
		if f.File != file {
			if i > 0 {
				p.line("")
			}
			file = f.File
			p.printf("  %s\n", file)
		}
		p.printf("    %s %s\n", f.Name, note(f))
	}
}

func (p *printer) packageLevel(lines []LineRange) {
	if len(lines) == 0 {
		p.line("  (none)")
	}
	for _, l := range lines {
		if l.Removed {
			p.printf("  %s (removed; base line numbers)\n", l)
		} else {
			p.printf("  %s\n", l)
		}
	}
}

// String formats the range as "file:12" or "file:12-15".
func (l LineRange) String() string {
	if l.End != l.Start {
		return fmt.Sprintf("%s:%d-%d", l.File, l.Start, l.End)
	}
	return fmt.Sprintf("%s:%d", l.File, l.Start)
}

// context prints the affected packages, endpoints, workers and tests.
func (p *printer) context(r *Report) {
	p.line("\nAffected packages:")
	if len(r.Packages) == 0 {
		p.line("  (none)")
	}
	for i, pkg := range r.Packages {
		if i > 0 {
			p.line("")
		}
		p.printf("  %s\n", PackageLabel(pkg.Dir, pkg.Name))
		for _, n := range []struct {
			label string
			count int
		}{{"changed", pkg.Changed}, {"direct", pkg.Direct}, {"transitive", pkg.Transitive}} {
			if n.count > 0 {
				p.printf("    %s: %d\n", n.label, n.count)
			}
		}
	}

	p.line("\nAffected endpoints:")
	if len(r.Endpoints) == 0 {
		p.line("  (none)")
	}
	for i, e := range r.Endpoints {
		if i > 0 {
			p.line("")
		}
		p.printf("  %s %s\n", e.Method, e.Path)
		p.printf("    handler: %s (%s)\n", e.Handler.Name, e.Handler.File)
		p.printf("    impact: %s\n", impactLabel(e.Handler))
	}

	p.line("\nAffected workers:")
	if len(r.Workers) == 0 {
		p.line("  (none)")
	}
	for i, w := range r.Workers {
		if i > 0 {
			p.line("")
		}
		p.printf("  %s (%s)\n", w.Name, w.File)
		p.printf("    kind: %s\n", w.Kind)
		p.printf("    impact: %s\n", impactLabel(w.Function))
	}

	p.line("\nAffected tests:")
	if len(r.Tests) == 0 {
		p.line("  (none)")
	}
	for i, t := range r.Tests {
		if i > 0 {
			p.line("")
		}
		s := fmt.Sprintf("  %s (%s)", t.Name, t.File)
		if t.Kind != "test" {
			s += fmt.Sprintf(" [%s]", t.Kind)
		}
		p.line(s)
		p.printf("    impact: %s\n", impactLabel(t.Function))
	}
}

func (p *printer) factors(factors []Factor, none string) {
	if len(factors) == 0 {
		p.printf("  (%s)\n", none)
	}
	for _, f := range factors {
		p.printf("  %-4s %s\n", fmt.Sprintf("%+d", f.Delta), f.Reason)
	}
}
