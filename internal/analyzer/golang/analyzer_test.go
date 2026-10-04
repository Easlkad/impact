package golang

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Easlkad/impact/internal/model"
)

const goMod = "module example.com/app\n\ngo 1.22\n"

// writeRepo creates a repository from a map of slash-separated paths to file
// contents and returns its root directory.
func writeRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func analyze(t *testing.T, files map[string]string, opts Options) *model.Repository {
	t.Helper()
	repo, err := Analyze(writeRepo(t, files), opts)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	return repo
}

func findFunc(t *testing.T, repo *model.Repository, id string) *model.Function {
	t.Helper()
	for _, fn := range repo.Functions() {
		if fn.ID == id {
			return fn
		}
	}
	t.Fatalf("function %s not found; have %q", id, funcIDs(repo))
	return nil
}

func funcIDs(repo *model.Repository) []string {
	var ids []string
	for _, fn := range repo.Functions() {
		ids = append(ids, fn.ID)
	}
	return ids
}

func packageIDs(repo *model.Repository) []string {
	var ids []string
	for _, p := range repo.Packages {
		ids = append(ids, p.ID)
	}
	return ids
}

// calls returns the call sites of a function as "<kind> <callee>" strings.
func calls(t *testing.T, repo *model.Repository, id string) []string {
	t.Helper()
	var out []string
	for _, c := range findFunc(t, repo, id).Calls {
		out = append(out, c.Kind.String()+" "+c.Callee)
	}
	return out
}

func assertEqual(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s:\n got: %q\nwant: %q", what, got, want)
	}
}

func TestFunctionCalls(t *testing.T) {
	repo := analyze(t, map[string]string{
		"go.mod": goMod,
		"user/user.go": `package user

func CreateUser() {
	ValidateUser()
	SaveUser()
}

func ValidateUser() {}

func SaveUser() {}
`,
	}, Options{})

	if len(repo.Packages) != 1 {
		t.Fatalf("got %d packages, want 1", len(repo.Packages))
	}
	p := repo.Packages[0]
	if p.ID != "example.com/app/user" || p.Name != "user" || p.Dir != "user" {
		t.Errorf("package = {ID: %q, Name: %q, Dir: %q}", p.ID, p.Name, p.Dir)
	}
	if len(p.Files) != 1 || p.Files[0].Path != "user/user.go" {
		t.Fatalf("unexpected files: %+v", p.Files)
	}

	assertEqual(t, "functions", funcIDs(repo), []string{
		"example.com/app/user.CreateUser",
		"example.com/app/user.ValidateUser",
		"example.com/app/user.SaveUser",
	})
	assertEqual(t, "calls of CreateUser", calls(t, repo, "example.com/app/user.CreateUser"), []string{
		"internal example.com/app/user.ValidateUser",
		"internal example.com/app/user.SaveUser",
	})

	fn := findFunc(t, repo, "example.com/app/user.CreateUser")
	if fn.StartLine != 3 || fn.EndLine != 6 || fn.File != "user/user.go" {
		t.Errorf("CreateUser at %s:%d-%d, want user/user.go:3-6", fn.File, fn.StartLine, fn.EndLine)
	}
	if fn.Calls[0].Line != 4 || fn.Calls[1].Line != 5 {
		t.Errorf("call lines = %d, %d; want 4, 5", fn.Calls[0].Line, fn.Calls[1].Line)
	}
}

func TestMethods(t *testing.T) {
	repo := analyze(t, map[string]string{
		"go.mod": goMod,
		"user/service.go": `package user

type Service struct {
	repo *Repo
}

func NewService() *Service { return &Service{repo: &Repo{}} }

func (s *Service) Create(name string) error {
	if err := s.validate(name); err != nil {
		return err
	}
	return s.repo.Save(name)
}

func (s Service) validate(name string) error { return nil }

type Repo struct{}

func (r *Repo) Save(name string) error { return nil }
`,
	}, Options{})

	create := findFunc(t, repo, "example.com/app/user.Service.Create")
	if !create.IsMethod() || create.Receiver != "Service" || create.QualifiedName() != "Service.Create" {
		t.Errorf("Create: IsMethod=%v Receiver=%q QualifiedName=%q", create.IsMethod(), create.Receiver, create.QualifiedName())
	}
	assertEqual(t, "calls of Service.Create", calls(t, repo, create.ID), []string{
		"internal example.com/app/user.Service.validate",
		"internal example.com/app/user.Repo.Save",
	})
	if findFunc(t, repo, "example.com/app/user.NewService").IsMethod() {
		t.Error("NewService reported as a method")
	}
}

func TestCrossPackageCalls(t *testing.T) {
	repo := analyze(t, map[string]string{
		"go.mod": goMod,
		"main.go": `package main

import (
	"fmt"

	st "example.com/app/store"
	"example.com/app/user"
)

func main() {
	svc := user.NewService(st.Open())
	if err := svc.Create("ada"); err != nil {
		fmt.Println(err)
	}
}
`,
		"store/store.go": `package store

type DB struct{}

func Open() *DB { return &DB{} }

func (db *DB) Insert(v string) error { return nil }
`,
		"user/service.go": `package user

import "example.com/app/store"

type Service struct{ db *store.DB }

func NewService(db *store.DB) *Service { return &Service{db: db} }

func (s *Service) Create(name string) error { return s.db.Insert(name) }
`,
	}, Options{})

	assertEqual(t, "packages", packageIDs(repo), []string{
		"example.com/app",
		"example.com/app/store",
		"example.com/app/user",
	})
	assertEqual(t, "calls of main", calls(t, repo, "example.com/app.main"), []string{
		"internal example.com/app/user.NewService",
		"internal example.com/app/store.Open",
		"internal example.com/app/user.Service.Create",
		"external fmt.Println",
	})
	assertEqual(t, "calls of Service.Create", calls(t, repo, "example.com/app/user.Service.Create"), []string{
		"internal example.com/app/store.DB.Insert",
	})
}

func TestTypeInference(t *testing.T) {
	repo := analyze(t, map[string]string{
		"go.mod": goMod,
		"shop/shop.go": `package shop

type Base struct{}

func (b *Base) Log() {}

type Cart struct {
	*Base
	items []string
}

func (c *Cart) Add(item string) { c.Log() }

func (c *Cart) Total() int { return 0 }

type Basket = Cart

var defaultCart = &Cart{}

func UseVar()            { var c Cart; c.Total() }
func UseLiteral()        { c := &Cart{}; c.Total() }
func UseNew()            { c := new(Cart); c.Total() }
func UseParam(c *Cart)   { c.Total() }
func UsePackageVar()     { defaultCart.Total() }
func UseAssertion(v any) { v.(*Cart).Total() }
func UseAlias(b Basket)  { b.Total() }
func UseClosure(c *Cart) { func() { c.Total() }() }
func UseRange(cs []Cart) { c := Cart{}; for _, c := range cs { c.Total() }; c.Total() }
`,
	}, Options{})

	total := "internal example.com/app/shop.Cart.Total"
	tests := map[string][]string{
		"UseVar":        {total},
		"UseLiteral":    {total},
		"UseNew":        {total},
		"UseParam":      {total},
		"UsePackageVar": {total},
		"UseAssertion":  {total},
		"UseAlias":      {total},
		"UseClosure":    {total},
		// The range variable shadows c with an unknown type. Scopes are
		// flattened per function, so the call after the loop is unresolved too.
		"UseRange": {"unresolved c.Total", "unresolved c.Total"},
	}
	for name, want := range tests {
		assertEqual(t, "calls of "+name, calls(t, repo, "example.com/app/shop."+name), want)
	}

	// Log is promoted from the embedded *Base.
	assertEqual(t, "calls of Cart.Add", calls(t, repo, "example.com/app/shop.Cart.Add"), []string{
		"internal example.com/app/shop.Base.Log",
	})
}

// TestEmbeddedSelection checks Go's rules for promoted fields and methods:
// the shallowest depth wins, a field shadows a deeper method, an ambiguous
// selector is not resolved, and a type outside the repository may hide a
// shallower method.
func TestEmbeddedSelection(t *testing.T) {
	repo := analyze(t, map[string]string{
		"go.mod": goMod,
		"app.go": `package app

import "sync"

type C struct{}

func (C) M() {}
func (C) N() {}

type A struct{ C }

type B struct{}

func (B) M() {}

type X struct{}

func (X) M() {}

// B.M (depth 1) wins over C.M (depth 2), whatever the field order.
type S struct {
	A
	B
}

// The field N shadows the method C.N promoted through A.
type F struct {
	A
	N func()
}

// M is at depth 1 twice.
type Amb struct {
	B
	X
}

// sync.Mutex is outside the repository and could have M at depth 1.
type Opaque struct {
	sync.Mutex
	A
}

// The depth-1 match is certain: ambiguity would not compile.
type Shallow struct {
	sync.Mutex
	B
}

type AliasB = B

// An alias has the methods of B at the same depth.
type Aliased struct {
	A
	AliasB
}

func Run(s S, f F, amb Amb, o Opaque, sh Shallow, al Aliased) {
	s.M()
	f.N()
	amb.M()
	o.M()
	sh.M()
	al.M()
}
`,
	}, Options{})

	assertEqual(t, "calls", calls(t, repo, "example.com/app.Run"), []string{
		"internal example.com/app.B.M",
		"unresolved example.com/app.F.N",
		"unresolved example.com/app.Amb.M",
		"unresolved example.com/app.Opaque.M",
		"internal example.com/app.B.M",
		"internal example.com/app.B.M",
	})
	for _, c := range findFunc(t, repo, "example.com/app.Run").Calls {
		if c.Callee == "example.com/app.F.N" && c.Reason != model.FunctionValue {
			t.Errorf("f.N() reason = %v, want a function value", c.Reason)
		}
	}
}

func TestShadowedImport(t *testing.T) {
	repo := analyze(t, map[string]string{
		"go.mod": goMod,
		"user/user.go": `package user

type User struct{}

func New() *User { return &User{} }

func (u *User) Save() {}
`,
		"app.go": `package app

import "example.com/app/user"

func Run() {
	user := user.New()
	user.Save()
}
`,
	}, Options{})

	assertEqual(t, "calls of Run", calls(t, repo, "example.com/app.Run"), []string{
		"internal example.com/app/user.New",
		"internal example.com/app/user.User.Save",
	})
}

func TestExternalBuiltinAndUnresolvedCalls(t *testing.T) {
	repo := analyze(t, map[string]string{
		"go.mod": goMod,
		"util/util.go": `package util

import (
	"fmt"
	"strings"
	"time"
)

type Store interface{ Get(key string) string }

type Celsius float64

func Run(s Store, items []string, hook func()) {
	n := len(items)
	items = append(items, "x")
	_ = Celsius(n)
	_ = []byte("raw")
	fmt.Println(strings.ToUpper("x"))
	t := time.Now()
	t.Unix()
	hook()
	s.Get("k")
	func() { helper() }()
}

func helper() {}
`,
	}, Options{})

	assertEqual(t, "calls of Run", calls(t, repo, "example.com/app/util.Run"), []string{
		"external fmt.Println",
		"external strings.ToUpper",
		"external time.Now",
		"unresolved t.Unix", // result types of external functions are unknown
		"unresolved hook",
		"unresolved example.com/app/util.Store.Get",
		"internal example.com/app/util.helper",
	})
}

func TestGenerics(t *testing.T) {
	repo := analyze(t, map[string]string{
		"go.mod": goMod,
		"list/list.go": `package list

type List[T any] struct{ items []T }

func New[T any]() *List[T] { return &List[T]{} }

func (l *List[T]) Push(v T) { l.grow() }

func (l *List[T]) grow() {}

func Map[T, U any](in []T, f func(T) U) []U { return nil }

func Use() {
	l := New[int]()
	l.Push(1)
	_ = Map[int, string](nil, nil)
}
`,
	}, Options{})

	assertEqual(t, "calls of Use", calls(t, repo, "example.com/app/list.Use"), []string{
		"internal example.com/app/list.New",
		"internal example.com/app/list.List.Push",
		"internal example.com/app/list.Map",
	})
	assertEqual(t, "calls of List.Push", calls(t, repo, "example.com/app/list.List.Push"), []string{
		"internal example.com/app/list.List.grow",
	})
}

func TestInitFunctionsGetUniqueIDs(t *testing.T) {
	repo := analyze(t, map[string]string{
		"go.mod": goMod,
		"a.go":   "package app\n\nfunc init() { setup() }\n\nfunc setup() {}\n",
		"b.go":   "package app\n\nfunc init() {}\n",
	}, Options{})

	assertEqual(t, "functions", funcIDs(repo), []string{
		"example.com/app.init#0",
		"example.com/app.setup",
		"example.com/app.init#1",
	})
}

func TestPackageLevelDeclarations(t *testing.T) {
	repo := analyze(t, map[string]string{
		"go.mod": goMod,
		"a.go": `package app

import "fmt"

// Limit is documented.
const Limit = 3

var (
	a = 1
	b = fmt.Sprint(a)
)

func F() {}

type T struct{}
`,
	}, Options{})

	got := repo.Packages[0].Files[0].Declarations
	want := []model.Declaration{
		{File: "a.go", StartLine: 5, EndLine: 6},
		{File: "a.go", StartLine: 8, EndLine: 11},
		{File: "a.go", StartLine: 15, EndLine: 15},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("declarations:\n got: %+v\nwant: %+v", got, want)
	}
}

func TestSkippedFilesAndDirectories(t *testing.T) {
	files := map[string]string{
		"go.mod":             goMod,
		"user/user.go":       "package user\n\nfunc CreateUser() {}\n",
		"user/user_test.go":  "package user\n\nfunc helperForTests() { CreateUser() }\n",
		"user/ext_test.go":   "package user_test\n\nimport \"example.com/app/user\"\n\nfunc TestCreate() { user.CreateUser() }\n",
		"user/gen.go":        "//go:build ignore\n\npackage main\n\nfunc main() {}\n",
		"user/_scratch.go":   "package user\n\nfunc Scratch() {}\n",
		"vendor/dep/dep.go":  "package dep\n\nfunc Dep() {}\n",
		"testdata/td.go":     "package td\n\nfunc TD() {}\n",
		".hidden/h.go":       "package h\n\nfunc H() {}\n",
		"_tools/t.go":        "package tools\n\nfunc T() {}\n",
		"docs/readme.txt":    "not go",
		"nested/sub/deep.go": "package sub\n\nfunc Deep() {}\n",
	}

	repo := analyze(t, files, Options{})
	assertEqual(t, "functions without tests", funcIDs(repo), []string{
		"example.com/app/nested/sub.Deep",
		"example.com/app/user.CreateUser",
	})

	repo = analyze(t, files, Options{IncludeTests: true})
	assertEqual(t, "packages with tests", packageIDs(repo), []string{
		"example.com/app/nested/sub",
		"example.com/app/user",
		"example.com/app/user_test",
	})
	assertEqual(t, "calls of TestCreate", calls(t, repo, "example.com/app/user_test.TestCreate"), []string{
		"internal example.com/app/user.CreateUser",
	})
	assertEqual(t, "calls of helperForTests", calls(t, repo, "example.com/app/user.helperForTests"), []string{
		"internal example.com/app/user.CreateUser",
	})
}

func TestParseErrorsAreWarnings(t *testing.T) {
	repo := analyze(t, map[string]string{
		"go.mod":    goMod,
		"good.go":   "package app\n\nfunc Good() {}\n",
		"broken.go": "package app\n\nfunc Broken( {\n",
	}, Options{})

	assertEqual(t, "functions", funcIDs(repo), []string{"example.com/app.Good"})
	if len(repo.Warnings) != 1 || !strings.Contains(repo.Warnings[0], "broken.go") {
		t.Errorf("warnings = %q, want one mentioning broken.go", repo.Warnings)
	}
}

// TestLineDirectives checks that lines are those of the files in the
// repository, as git diffs count them, whatever //line directives say.
func TestLineDirectives(t *testing.T) {
	repo := analyze(t, map[string]string{
		"go.mod": goMod,
		"gen.go": `package app

import "net/http"

//line parser.y:1000

func Handle() {
	http.HandleFunc("/x", Serve)
	helper()
}

func Serve(w http.ResponseWriter, r *http.Request) {}

func helper() {}
`,
		"broken.go": "package app\n\n//line other.y:500\nfunc Broken( {\n",
	}, Options{})

	fn := findFunc(t, repo, "example.com/app.Handle")
	if fn.StartLine != 7 || fn.EndLine != 10 {
		t.Errorf("Handle lines = %d-%d, want 7-10", fn.StartLine, fn.EndLine)
	}
	if len(fn.Routes) != 1 || fn.Routes[0].Line != 8 {
		t.Errorf("routes = %+v, want one on line 8", fn.Routes)
	}
	var helperLine int
	for _, c := range fn.Calls {
		if c.Callee == "example.com/app.helper" {
			helperLine = c.Line
		}
	}
	if helperLine != 9 {
		t.Errorf("call to helper on line %d, want 9", helperLine)
	}

	if len(repo.Warnings) != 1 || !strings.HasPrefix(repo.Warnings[0], "broken.go:4:") {
		t.Errorf("warnings = %q, want one at broken.go:4", repo.Warnings)
	}
}

func TestScanSubdirectoryOfModule(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"go.mod":       goMod,
		"user/user.go": "package user\n\nfunc CreateUser() {}\n",
	})
	repo, err := Analyze(filepath.Join(root, "user"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "packages", packageIDs(repo), []string{"example.com/app/user"})
	if repo.Packages[0].Dir != "." {
		t.Errorf("Dir = %q, want %q", repo.Packages[0].Dir, ".")
	}
}

func TestWithoutGoMod(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"pkg/p.go": "package pkg\n\nfunc P() {}\n",
	})
	repo, err := Analyze(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "packages", packageIDs(repo), []string{filepath.Base(root) + "/pkg"})
}

func TestAnalyzeErrors(t *testing.T) {
	root := writeRepo(t, map[string]string{"file.go": "package x\n"})
	if _, err := Analyze(filepath.Join(root, "missing"), Options{}); err == nil {
		t.Error("expected an error for a missing directory")
	}
	if _, err := Analyze(filepath.Join(root, "file.go"), Options{}); err == nil {
		t.Error("expected an error for a file")
	}
}

func TestParseModulePath(t *testing.T) {
	tests := map[string]string{
		"module example.com/app\n\ngo 1.22\n":         "example.com/app",
		"// comment\nmodule \"example.com/quoted\"\n": "example.com/quoted",
		"module example.com/c // trailing comment\n":  "example.com/c",
		"go 1.22\n": "",
	}
	for in, want := range tests {
		if got := parseModulePath([]byte(in)); got != want {
			t.Errorf("parseModulePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGuessPackageName(t *testing.T) {
	tests := map[string]string{
		"fmt":                          "fmt",
		"net/http":                     "http",
		"github.com/user/repo/v2":      "repo",
		"gopkg.in/yaml.v3":             "yaml",
		"github.com/mattn/go-sqlite3":  "sqlite3",
		"github.com/google/uuid":       "uuid",
		"github.com/example/client-go": "client",
	}
	for path, want := range tests {
		if got := guessPackageName(path); got != want {
			t.Errorf("guessPackageName(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestAnalyzeFS(t *testing.T) {
	fsys := fstest.MapFS{
		"go.mod":       {Data: []byte(goMod)},
		"user/user.go": {Data: []byte("package user\n\nfunc CreateUser() { SaveUser() }\n\nfunc SaveUser() {}\n")},
		"tools/gen.go": {Data: []byte("package tools\n\nfunc Gen() {}\n")},
		"tools/go.mod": {Data: []byte("module example.com/tools\n")},
	}
	repo, err := AnalyzeFS(fsys, "unused", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if repo.Root != "" {
		t.Errorf("Root = %q, want empty", repo.Root)
	}
	// tools has its own go.mod, so it is a separate module.
	assertEqual(t, "packages", packageIDs(repo), []string{"example.com/app/user", "example.com/tools"})
	assertEqual(t, "calls of CreateUser", calls(t, repo, "example.com/app/user.CreateUser"), []string{
		"internal example.com/app/user.SaveUser",
	})

	// Without a go.mod, rootPath names the root directory.
	repo, err = AnalyzeFS(fstest.MapFS{"a/a.go": {Data: []byte("package a\n")}}, "myrepo", Options{})
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "packages without go.mod", packageIDs(repo), []string{"myrepo/a"})
}

func TestWants(t *testing.T) {
	tests := []struct {
		path         string
		includeTests bool
		want         bool
	}{
		{"main.go", false, true},
		{"internal/user/user.go", false, true},
		{"go.mod", false, true},
		{"tools/go.mod", false, true},
		{"vendor/go.mod", false, false},
		{"user/user_test.go", false, false},
		{"user/user_test.go", true, true},
		{"vendor/dep/dep.go", false, false},
		{"user/testdata/x.go", false, false},
		{".github/x.go", false, false},
		{"_tools/x.go", false, false},
		{"user/_x.go", false, false},
		{"README.md", false, false},
	}
	for _, tt := range tests {
		if got := Wants(tt.path, Options{IncludeTests: tt.includeTests}); got != tt.want {
			t.Errorf("Wants(%q, tests=%v) = %v, want %v", tt.path, tt.includeTests, got, tt.want)
		}
	}
}

func TestDeclarationRanges(t *testing.T) {
	repo := analyze(t, map[string]string{
		"go.mod": goMod,
		"a.go":   "package app\n\n// Documented has a doc comment,\n// which is part of its range.\nfunc Documented() {\n}\n",
		// Build-constrained variants of one function share a Function.
		"open_unix.go":    "//go:build unix\n\npackage app\n\nfunc open() {}\n",
		"open_windows.go": "//go:build windows\n\npackage app\n\n\nfunc open() {\n}\n",
	}, Options{})

	doc := findFunc(t, repo, "example.com/app.Documented")
	if doc.StartLine != 3 || doc.EndLine != 6 {
		t.Errorf("Documented at lines %d-%d, want 3-6", doc.StartLine, doc.EndLine)
	}

	open := findFunc(t, repo, "example.com/app.open")
	want := []model.Declaration{
		{File: "open_unix.go", StartLine: 5, EndLine: 5},
		{File: "open_windows.go", StartLine: 6, EndLine: 7},
	}
	if !reflect.DeepEqual(open.Declarations, want) {
		t.Errorf("open.Declarations = %+v, want %+v", open.Declarations, want)
	}
	if open.File != "open_unix.go" || open.StartLine != 5 {
		t.Errorf("open is at %s:%d, want its first declaration", open.File, open.StartLine)
	}
	if n := len(repo.Functions()); n != 2 {
		t.Errorf("got %d functions, want 2 (variants are merged)", n)
	}
}
