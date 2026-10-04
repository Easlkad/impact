package changes

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Easlkad/impact/internal/gitdiff"
	"github.com/Easlkad/impact/internal/gittest"
)

const goMod = "module example.com/app\n\ngo 1.22\n"

// userGo is the base version of user/user.go used by most tests.
//
//	 5: const maxNameLength
//	 7: CreateUser (doc comment on line 7, func on 8-13)
//	15: ValidateUser (15-20)
//	22: SaveUser (22-24)
const userGo = `package user

import "fmt"

const maxNameLength = 64

// CreateUser validates and stores a user.
func CreateUser(name string) error {
	if err := ValidateUser(name); err != nil {
		return err
	}
	return SaveUser(name)
}

func ValidateUser(name string) error {
	if len(name) > maxNameLength {
		return fmt.Errorf("name too long")
	}
	return nil
}

func SaveUser(name string) error {
	return nil
}
`

// compareCommits commits base, applies edit, commits again and compares the
// two commits.
func compareCommits(t *testing.T, base map[string]string, edit func(g *gittest.Repo)) *Report {
	t.Helper()
	g := gittest.New(t)
	g.Write(base)
	baseHash := g.Commit("base")
	edit(g)
	g.Commit("head")

	report, err := Compare(g.Dir, baseHash, "HEAD", Options{})
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	return report
}

// editFile replaces old with new in a file of the repository.
func editFile(t *testing.T, g *gittest.Repo, name, old, new string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(g.Dir, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), old) {
		t.Fatalf("%s does not contain %q", name, old)
	}
	g.Write(map[string]string{name: strings.Replace(string(data), old, new, 1)})
}

// functions formats the function changes as "<kind> <file> <name>".
func functions(r *Report) []string {
	var out []string
	for _, c := range r.Functions {
		out = append(out, string(c.Kind)+" "+c.Declaration.File+" "+c.Function.QualifiedName())
	}
	return out
}

func packageLevel(r *Report) []string {
	var out []string
	for _, c := range r.PackageLevel {
		s := c.String()
		if c.Removed {
			s += " removed"
		}
		out = append(out, s)
	}
	return out
}

func files(r *Report) []string {
	var out []string
	for _, f := range r.Files {
		s := string(f.Status) + " " + f.Path()
		if f.Status == gitdiff.Renamed {
			s = string(f.Status) + " " + f.OldPath + " -> " + f.NewPath
		}
		out = append(out, s)
	}
	return out
}

func assertEqual(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s:\n got: %q\nwant: %q", what, got, want)
	}
}

var userRepo = map[string]string{"go.mod": goMod, "user/user.go": userGo}

func TestModificationInsideFunction(t *testing.T) {
	r := compareCommits(t, userRepo, func(g *gittest.Repo) {
		editFile(t, g, "user/user.go", `fmt.Errorf("name too long")`, `fmt.Errorf("name longer than %d", maxNameLength)`)
	})
	assertEqual(t, "files", files(r), []string{"modified user/user.go"})
	assertEqual(t, "functions", functions(r), []string{"modified user/user.go ValidateUser"})
	assertEqual(t, "package level", packageLevel(r), nil)

	fn := r.Functions[0].Function
	if fn.ID != "example.com/app/user.ValidateUser" {
		t.Errorf("function ID = %q", fn.ID)
	}
}

func TestAddedFunction(t *testing.T) {
	r := compareCommits(t, userRepo, func(g *gittest.Repo) {
		editFile(t, g, "user/user.go", "func SaveUser", "func DeleteUser(name string) error {\n\treturn nil\n}\n\nfunc SaveUser")
	})
	assertEqual(t, "functions", functions(r), []string{"added user/user.go DeleteUser"})
	assertEqual(t, "package level", packageLevel(r), nil)
}

func TestDeletedFunction(t *testing.T) {
	r := compareCommits(t, userRepo, func(g *gittest.Repo) {
		editFile(t, g, "user/user.go", "\tif err := ValidateUser(name); err != nil {\n\t\treturn err\n\t}\n", "")
		editFile(t, g, "user/user.go", "func ValidateUser(name string) error {\n\tif len(name) > maxNameLength {\n\t\treturn fmt.Errorf(\"name too long\")\n\t}\n\treturn nil\n}\n\n", "")
	})
	// SaveUser moved up by ten lines but did not change.
	assertEqual(t, "functions", functions(r), []string{
		"modified user/user.go CreateUser",
		"deleted user/user.go ValidateUser",
	})
	assertEqual(t, "package level", packageLevel(r), nil)

	deleted := r.Functions[1]
	if deleted.Declaration.StartLine != 15 || deleted.Declaration.EndLine != 20 {
		t.Errorf("deleted function at lines %d-%d, want its base lines 15-20",
			deleted.Declaration.StartLine, deleted.Declaration.EndLine)
	}
}

func TestChangeBetweenFunctions(t *testing.T) {
	r := compareCommits(t, userRepo, func(g *gittest.Repo) {
		editFile(t, g, "user/user.go", "func ValidateUser", "var defaultName = \"guest\"\n\nfunc ValidateUser")
	})
	assertEqual(t, "functions", functions(r), nil)
	assertEqual(t, "package level", packageLevel(r), []string{"user/user.go:15"})
}

func TestPackageLevelChanges(t *testing.T) {
	base := map[string]string{"go.mod": goMod, "user/user.go": userGo + "\nvar legacy = true\n"}
	r := compareCommits(t, base, func(g *gittest.Repo) {
		editFile(t, g, "user/user.go", "maxNameLength = 64", "maxNameLength = 128")
		editFile(t, g, "user/user.go", "\nvar legacy = true\n", "")
	})
	// ValidateUser uses the constant, but its own lines did not change.
	assertEqual(t, "functions", functions(r), nil)
	assertEqual(t, "package level", packageLevel(r), []string{
		"user/user.go:5",
		"user/user.go:26 removed",
	})
}

func TestMultipleHunksInOneFile(t *testing.T) {
	r := compareCommits(t, userRepo, func(g *gittest.Repo) {
		editFile(t, g, "user/user.go", "\treturn SaveUser(name)", "\treturn SaveUser(strings.TrimSpace(name))")
		editFile(t, g, "user/user.go", "func SaveUser(name string) error {\n\treturn nil", "func SaveUser(name string) error {\n\tfmt.Println(\"saving\", name)\n\treturn nil")
	})
	if n := len(r.Files[0].Hunks); n != 2 {
		t.Errorf("got %d hunks, want 2", n)
	}
	assertEqual(t, "functions", functions(r), []string{
		"modified user/user.go CreateUser",
		"modified user/user.go SaveUser",
	})
}

func TestDocCommentBelongsToFunction(t *testing.T) {
	r := compareCommits(t, userRepo, func(g *gittest.Repo) {
		editFile(t, g, "user/user.go", "// CreateUser validates and stores a user.", "// CreateUser validates a user and stores it.")
	})
	assertEqual(t, "functions", functions(r), []string{"modified user/user.go CreateUser"})
	assertEqual(t, "package level", packageLevel(r), nil)
}

func TestRenamedFile(t *testing.T) {
	r := compareCommits(t, userRepo, func(g *gittest.Repo) {
		g.Git("mv", "user/user.go", "user/accounts.go")
		editFile(t, g, "user/accounts.go", "func SaveUser(name string) error {\n\treturn nil", "func SaveUser(name string) error {\n\treturn errNotImplemented")
	})
	assertEqual(t, "files", files(r), []string{"renamed user/user.go -> user/accounts.go"})
	assertEqual(t, "functions", functions(r), []string{"modified user/accounts.go SaveUser"})
	assertEqual(t, "package level", packageLevel(r), nil)
}

func TestRenameToAnotherPackage(t *testing.T) {
	r := compareCommits(t, userRepo, func(g *gittest.Repo) {
		g.Remove("user/user.go")
		g.Write(map[string]string{"account/user.go": strings.Replace(userGo, "package user", "package account", 1)})
	})
	// Moving to another package changes every function's identity.
	assertEqual(t, "functions", functions(r), []string{
		"added account/user.go CreateUser",
		"added account/user.go ValidateUser",
		"added account/user.go SaveUser",
		"deleted user/user.go CreateUser",
		"deleted user/user.go ValidateUser",
		"deleted user/user.go SaveUser",
	})
}

func TestAddedAndDeletedFiles(t *testing.T) {
	base := map[string]string{
		"go.mod":         goMod,
		"user/user.go":   userGo,
		"user/legacy.go": "package user\n\n// Legacy is gone in head.\nfunc Legacy() {}\n",
	}
	r := compareCommits(t, base, func(g *gittest.Repo) {
		g.Remove("user/legacy.go")
		g.Write(map[string]string{"user/admin.go": "package user\n\ntype Admin struct{}\n\nfunc (a *Admin) Promote(name string) {}\n"})
	})
	assertEqual(t, "files", files(r), []string{"added user/admin.go", "deleted user/legacy.go"})
	assertEqual(t, "functions", functions(r), []string{
		"added user/admin.go Admin.Promote",
		"deleted user/legacy.go Legacy",
	})
	// The lines of added and deleted files are not package-level changes.
	assertEqual(t, "package level", packageLevel(r), nil)
}

func TestFunctionMovedWithinPackage(t *testing.T) {
	r := compareCommits(t, userRepo, func(g *gittest.Repo) {
		editFile(t, g, "user/user.go", "\nfunc SaveUser(name string) error {\n\treturn nil\n}\n", "")
		g.Write(map[string]string{"user/store.go": "package user\n\nfunc SaveUser(name string) error {\n\treturn nil\n}\n"})
	})
	assertEqual(t, "functions", functions(r), []string{"modified user/store.go SaveUser"})
	assertEqual(t, "package level", packageLevel(r), nil)
}

func TestInitFunctions(t *testing.T) {
	base := map[string]string{
		"go.mod": goMod,
		"b.go":   "package app\n\nfunc init() { setup() }\n\nfunc setup() {}\n",
	}
	r := compareCommits(t, base, func(g *gittest.Repo) {
		// a.go sorts first, so its init takes over the analyzer ID init#0.
		g.Write(map[string]string{"a.go": "package app\n\nfunc init() {}\n"})
	})
	assertEqual(t, "functions", functions(r), []string{"added a.go init"})
}

func TestTestFilesAreIncluded(t *testing.T) {
	base := map[string]string{
		"go.mod":            goMod,
		"user/user.go":      userGo,
		"user/user_test.go": "package user\n\nimport \"testing\"\n\nfunc TestSave(t *testing.T) {\n\tSaveUser(\"a\")\n}\n",
	}
	r := compareCommits(t, base, func(g *gittest.Repo) {
		editFile(t, g, "user/user_test.go", `SaveUser("a")`, `SaveUser("b")`)
	})
	assertEqual(t, "functions", functions(r), []string{"modified user/user_test.go TestSave"})
}

func TestNonGoFilesAreIgnored(t *testing.T) {
	base := map[string]string{"go.mod": goMod, "user/user.go": userGo, "README.md": "# app\n"}
	r := compareCommits(t, base, func(g *gittest.Repo) {
		g.Write(map[string]string{"README.md": "# app\n\nMore docs.\n", "go.mod": goMod + "\nrequire example.com/dep v1.0.0\n"})
	})
	if len(r.Files) != 0 || len(r.Functions) != 0 || len(r.PackageLevel) != 0 {
		t.Errorf("expected an empty report, got %+v", r)
	}
}

func TestParseErrorInChangedFile(t *testing.T) {
	r := compareCommits(t, userRepo, func(g *gittest.Repo) {
		editFile(t, g, "user/user.go", "func SaveUser(name string) error {", "func SaveUser(name string) error {{")
	})
	// The head version cannot be analyzed. Its functions must not be
	// reported as deleted; the warning explains the gap.
	assertEqual(t, "functions", functions(r), nil)
	if len(r.Warnings) != 1 || !strings.HasPrefix(r.Warnings[0], "head: user/user.go:") {
		t.Errorf("warnings = %q", r.Warnings)
	}
}

func TestWorkingTreeIsNotUsed(t *testing.T) {
	g := gittest.New(t)
	g.Write(userRepo)
	base := g.Commit("base")
	editFile(t, g, "user/user.go", "\treturn nil\n}\n", "\treturn errNotImplemented\n}\n")
	head := g.Commit("head")
	// Uncommitted edits, including to a function the commits do not touch.
	editFile(t, g, "user/user.go", "maxNameLength {", "maxNameLength+1 {")
	statusBefore := g.Git("status", "--porcelain")

	// The repository path may also be a subdirectory.
	r, err := Compare(filepath.Join(g.Dir, "user"), base, head, Options{})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "functions", functions(r), []string{"modified user/user.go ValidateUser"})

	if got := g.Git("rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved to %s", got)
	}
	if got := g.Git("status", "--porcelain"); got != statusBefore {
		t.Errorf("git status changed from %q to %q", statusBefore, got)
	}
	data, _ := os.ReadFile(filepath.Join(g.Dir, "user", "user.go"))
	if !strings.Contains(string(data), "maxNameLength+1") {
		t.Error("the uncommitted edit was lost")
	}
}

func TestCompareErrors(t *testing.T) {
	g := gittest.New(t)
	g.Write(userRepo)
	g.Commit("base")

	if _, err := Compare(g.Dir, "HEAD", "no-such-ref", Options{}); err == nil || !strings.Contains(err.Error(), "no-such-ref") {
		t.Errorf("unknown head ref: err = %v", err)
	}
	if _, err := Compare(g.Dir, "no-such-ref", "HEAD", Options{}); err == nil {
		t.Error("unknown base ref: no error")
	}
	if _, err := Compare(t.TempDir(), "HEAD", "HEAD", Options{}); err == nil {
		t.Error("directory outside git: no error")
	}
}
