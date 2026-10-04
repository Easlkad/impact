package report

import (
	"fmt"
	"io"
	"strings"
)

// Marker is a hidden line at the start of the Markdown report. Automation
// can search for it to update a previous report instead of adding another,
// for example in pull request comments.
const Marker = "<!-- impact-report -->"

// MarkdownOptions controls the Markdown rendering.
type MarkdownOptions struct {
	// MaxItems limits each list of the report; the rest is counted in an
	// "N additional ... omitted." line. 0 means no limit.
	MaxItems int
	// MaxPaths limits the important impact paths; 0 shows none.
	MaxPaths int
}

// WriteMarkdown renders a compact report suited to a pull request comment
// or a CI job summary. Long lists are truncated (see MarkdownOptions), and
// the full lists of impacted functions and packages are in collapsed
// sections.
func WriteMarkdown(w io.Writer, r *Report, opts MarkdownOptions) error {
	p := &printer{w: w}
	p.markdown(r, opts)
	return p.err
}

func (p *printer) markdown(r *Report, opts MarkdownOptions) {
	p.line(Marker)
	p.line("## Impact Analysis")
	p.line("")
	if len(r.ChangedFiles) == 0 {
		p.printf("No Go files changed between %s and %s.\n", mdCommit(r.Base, r.MergeBase), mdCommit(r.Head, false))
		p.mdFooter(r)
		return
	}

	p.printf("**Risk:** %s (%d/100)\n\n", r.Risk.Level, r.Risk.Value)
	p.printf("**Confidence:** %s (%d/100)\n\n", r.Confidence.Level, r.Confidence.Value)
	p.printf("Comparing %s → %s\n", mdCommit(r.Base, r.MergeBase), mdCommit(r.Head, false))

	p.line("\n### Summary\n")
	s := r.Summary
	p.printf("- %s\n", count(s.Changed, "changed function"))
	p.printf("- %s\n", count(s.DirectImpact, "directly impacted function"))
	p.printf("- %s\n", count(s.TransitiveImpact, "transitively impacted function"))
	p.printf("- %s\n", count(s.Packages, "affected package"))
	p.printf("- %s\n", count(s.Endpoints, "affected endpoint"))
	p.printf("- %s\n", count(s.Workers, "affected worker"))
	p.printf("- %s\n", count(s.Tests, "affected test"))
	if s.PackageLevelChanges > 0 {
		p.printf("- %s (not propagated)\n", count(s.PackageLevelChanges, "package-level change"))
	}

	p.line("\n### Changed functions\n")
	p.mdList(len(r.Changed), opts.MaxItems, "changed function", func(i int) string {
		f := r.Changed[i]
		return fmt.Sprintf("%s (%s) — %s", code(f.Name), f.Change, code(f.File))
	})

	if len(r.Endpoints) > 0 {
		p.line("\n### Affected endpoints\n")
		p.mdList(len(r.Endpoints), opts.MaxItems, "endpoint", func(i int) string {
			e := r.Endpoints[i]
			return fmt.Sprintf("%s — %s, handler %s", code(e.Method+" "+e.Path), impactLabel(e.Handler), code(e.Handler.Name))
		})
	}
	if len(r.Workers) > 0 {
		p.line("\n### Affected workers\n")
		p.mdList(len(r.Workers), opts.MaxItems, "worker", func(i int) string {
			w := r.Workers[i]
			return fmt.Sprintf("%s (%s) — %s", code(w.Name), w.Kind, impactLabel(w.Function))
		})
	}
	if len(r.Tests) > 0 {
		p.line("\n### Affected tests\n")
		p.mdList(len(r.Tests), opts.MaxItems, "test", func(i int) string {
			t := r.Tests[i]
			kind := ""
			if t.Kind != "test" {
				kind = " (" + t.Kind + ")"
			}
			return fmt.Sprintf("%s%s — %s", code(t.Name), kind, impactLabel(t.Function))
		})
	}

	if paths := importantPaths(r); len(paths) > 0 && opts.MaxPaths > 0 {
		p.line("\n### Important impact paths\n")
		p.mdList(len(paths), limit(opts.MaxPaths, opts.MaxItems), "impact path", func(i int) string {
			var steps []string
			for _, id := range paths[i] {
				step := r.lookup(id)
				s := code(step.Name)
				if step.Distance == 0 {
					s += " **[changed]**"
				}
				steps = append(steps, s)
			}
			return strings.Join(steps, " → ")
		})
	}

	p.line("\n### Risk factors\n")
	p.mdFactors(r.Risk.Factors, "No risk factors.")
	p.line("\n### Confidence factors\n")
	p.mdFactors(r.Confidence.Factors, "No known blind spots in the affected code.")

	if len(r.Warnings) > 0 {
		p.line("\n### Warnings\n")
		p.mdList(len(r.Warnings), opts.MaxItems, "warning", func(i int) string { return escape(r.Warnings[i]) })
	}

	impacted := append(append([]Function{}, r.DirectImpact...), r.TransitiveImpact...)
	if len(impacted) > 0 {
		p.printf("\n<details>\n<summary>Impacted functions (%d)</summary>\n\n", len(impacted))
		p.mdList(len(impacted), opts.MaxItems, "impacted function", func(i int) string {
			f := impacted[i]
			return fmt.Sprintf("%s — %s — %s", code(f.Name), impactLabel(f), code(f.File))
		})
		p.line("\n</details>")
	}
	if len(r.Packages) > 0 {
		p.printf("\n<details>\n<summary>Affected packages (%d)</summary>\n\n", len(r.Packages))
		p.mdList(len(r.Packages), opts.MaxItems, "package", func(i int) string {
			pkg := r.Packages[i]
			var counts []string
			for _, n := range []struct {
				label string
				count int
			}{{"changed", pkg.Changed}, {"direct", pkg.Direct}, {"transitive", pkg.Transitive}} {
				if n.count > 0 {
					counts = append(counts, fmt.Sprintf("%d %s", n.count, n.label))
				}
			}
			return fmt.Sprintf("%s — %s", code(PackageLabel(pkg.Dir, pkg.Name)), strings.Join(counts, ", "))
		})
		p.line("\n</details>")
	}

	p.mdFooter(r)
}

// importantPaths returns the impact paths most worth showing: those of the
// affected endpoints and workers, then those of the transitive impacts, each
// starting function once.
func importantPaths(r *Report) [][]string {
	var paths [][]string
	seen := make(map[string]bool)
	add := func(f Function) {
		if len(f.Path) > 1 && !seen[f.ID] {
			seen[f.ID] = true
			paths = append(paths, f.Path)
		}
	}
	for _, e := range r.Endpoints {
		add(e.Handler)
	}
	for _, w := range r.Workers {
		add(w.Function)
	}
	for _, f := range r.TransitiveImpact {
		add(f)
	}
	return paths
}

// mdList prints n list items, at most max of them (0: all), followed by a
// note on the omitted ones.
func (p *printer) mdList(n, max int, noun string, item func(i int) string) {
	if n == 0 {
		p.line("_None._")
		return
	}
	shown := n
	if max > 0 && max < n {
		shown = max
	}
	for i := 0; i < shown; i++ {
		p.printf("- %s\n", item(i))
	}
	if omitted := n - shown; omitted > 0 {
		p.printf("\n_%s omitted._\n", count(omitted, "additional "+noun))
	}
}

func (p *printer) mdFactors(factors []Factor, none string) {
	if len(factors) == 0 {
		p.line(none)
	}
	for _, f := range factors {
		p.printf("- `%+d` %s\n", f.Delta, escape(f.Reason))
	}
}

func (p *printer) mdFooter(r *Report) {
	p.printf("\n<sub>Generated by impact %s. Risk and confidence are heuristics: see the factors above.</sub>\n", escape(r.Tool.Version))
}

// mdCommit formats a compared commit for Markdown: `main` (`1a2b3c4`),
// just `1a2b3c4` when the ref is that hash, or for a merge base
// "the merge base `1a2b3c4` of `main`".
func mdCommit(c Commit, mergeBase bool) string {
	switch {
	case mergeBase:
		return fmt.Sprintf("the merge base %s of %s", code(shortHash(c.Commit)), code(refLabel(c.Ref)))
	case isHash(c.Ref):
		return code(shortHash(c.Commit))
	}
	return fmt.Sprintf("%s (%s)", code(c.Ref), code(shortHash(c.Commit)))
}

// limit returns the smaller of two limits, where 0 means no limit for max.
func limit(n, max int) int {
	if max > 0 && max < n {
		return max
	}
	return n
}

// code formats s as inline code, choosing a fence that s does not contain.
func code(s string) string {
	if strings.Contains(s, "`") {
		return "`` " + s + " ``"
	}
	return "`" + s + "`"
}

// escape escapes the characters that Markdown would interpret in plain
// text, such as "*", "_" and "<".
func escape(s string) string {
	return markdownEscaper.Replace(s)
}

var markdownEscaper = strings.NewReplacer(
	`\`, `\\`, "`", "\\`", "*", `\*`, "_", `\_`, "[", `\[`, "]", `\]`, "<", `\<`, ">", `\>`, "#", `\#`, "|", `\|`,
)

// count formats "1 changed function" or "3 changed functions".
func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
