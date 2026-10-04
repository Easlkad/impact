package changes

import (
	"path/filepath"
	"strings"

	"github.com/Easlkad/impact/internal/analyzer/golang"
	"github.com/Easlkad/impact/internal/gitdiff"
	"github.com/Easlkad/impact/internal/model"
)

// Report describes the Go changes between two commits.
type Report struct {
	Root       string // top-level directory of the git repository
	Base, Head Commit
	// MergeBase is set when Base.Hash is the merge base of the two refs
	// rather than the commit the base ref names (see Options.MergeBase).
	MergeBase    bool
	Files        []gitdiff.File   // changed Go files, in git's order
	Functions    []FunctionChange // sorted by file and line
	PackageLevel []LineChange     // changed lines outside functions
	Warnings     []string         // analysis problems in the changed files

	// The analyses of the two commits, for further processing such as
	// building call graphs. Both are nil when no Go file changed.
	BaseAnalysis, HeadAnalysis *model.Repository
}

// Commit is a ref given by the user and the commit it resolved to.
type Commit struct {
	Ref  string
	Hash string
}

// Options adjust what Compare compares.
type Options struct {
	// MergeBase compares the head commit with the merge base of the two
	// refs, the commit the head branch started from, instead of with the
	// base commit itself. This is what a pull request diff shows: changes
	// made on the base branch after the head branch started are left out.
	MergeBase bool
}

// Compare reports the Go changes between the commits baseRef and headRef of
// the git repository containing dir.
//
// Both commits are analyzed from git's object database, so neither has to
// be checked out, and the repository is not modified.
func Compare(dir, baseRef, headRef string, opts Options) (*Report, error) {
	repo, err := gitdiff.Open(dir)
	if err != nil {
		return nil, err
	}
	report := &Report{Root: repo.Root, Base: Commit{Ref: baseRef}, Head: Commit{Ref: headRef}}
	if report.Base.Hash, err = repo.ResolveCommit(baseRef); err != nil {
		return nil, err
	}
	if report.Head.Hash, err = repo.ResolveCommit(headRef); err != nil {
		return nil, err
	}
	if opts.MergeBase {
		if report.Base.Hash, err = repo.MergeBase(report.Base.Hash, report.Head.Hash); err != nil {
			return nil, err
		}
		report.MergeBase = true
	}

	// Go files are diffed as text even when .gitattributes marks them
	// binary: without hunks, their changed functions would go unnoticed.
	diff, err := repo.Diff(report.Base.Hash, report.Head.Hash, isGo)
	if err != nil {
		return nil, err
	}
	var binary []string
	for _, f := range diff {
		if isGo(f.OldPath) || isGo(f.NewPath) {
			report.Files = append(report.Files, f)
			if f.Binary {
				binary = append(binary, "diff: "+f.Path()+": git reported a binary change; its changed lines are unknown")
			}
		}
	}
	if len(report.Files) == 0 {
		return report, nil
	}

	base, err := analyzeCommit(repo, report.Base.Hash)
	if err != nil {
		return nil, err
	}
	head, err := analyzeCommit(repo, report.Head.Hash)
	if err != nil {
		return nil, err
	}
	report.BaseAnalysis, report.HeadAnalysis = base, head
	report.Functions, report.PackageLevel = Detect(report.Files, base, head)
	report.Warnings = append(binary, changedFileWarnings(base, report.Files, "base")...)
	report.Warnings = append(report.Warnings, changedFileWarnings(head, report.Files, "head")...)
	return report, nil
}

func isGo(path string) bool { return strings.HasSuffix(path, ".go") }

// analyzeCommit runs the Go analyzer over the files of a commit, test files
// included: changes to tests are changes too.
func analyzeCommit(repo *gitdiff.Repo, commit string) (*model.Repository, error) {
	opts := golang.Options{IncludeTests: true}
	fsys, err := repo.Snapshot(commit, func(path string) bool { return golang.Wants(path, opts) })
	if err != nil {
		return nil, err
	}
	// Outside a module, packages are named after the repository directory,
	// as "impact scan" of the top-level directory would name them.
	analysis, err := golang.AnalyzeFS(fsys, filepath.Base(repo.Root), opts)
	if err != nil {
		return nil, err
	}
	analysis.Root = repo.Root
	return analysis, nil
}

// changedFileWarnings returns the analysis warnings about the changed files,
// such as parse errors that hide their functions. Warnings about other files
// do not affect the report.
func changedFileWarnings(repo *model.Repository, files []gitdiff.File, label string) []string {
	var out []string
	for _, w := range repo.Warnings {
		for _, f := range files {
			if (f.OldPath != "" && strings.HasPrefix(w, f.OldPath+":")) ||
				(f.NewPath != "" && strings.HasPrefix(w, f.NewPath+":")) {
				out = append(out, label+": "+w)
				break
			}
		}
	}
	return out
}
