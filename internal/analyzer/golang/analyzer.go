// Package golang scans Go source trees and builds a model.Repository.
//
// The analysis is purely syntactic: files are parsed with go/parser and walked
// with go/ast. Nothing is type-checked, built or downloaded, so any checkout
// can be scanned, even one whose dependencies are unavailable. The cost is
// that some calls cannot be resolved; resolve.go describes the rules used.
package golang

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	goscanner "go/scanner" // scanner is the analyzer's own type
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/Easlkad/impact/internal/model"
)

// Options controls what Analyze scans.
type Options struct {
	// IncludeTests also scans _test.go files. Tests in an external test
	// package (package foo_test) get the package ID "<import path>_test".
	IncludeTests bool
}

// Analyze scans the Go source tree in the directory root.
//
// Directories the go tool ignores (testdata, and names starting with "." or
// "_") are skipped, as is vendor. Files that fail to parse are reported in
// Repository.Warnings instead of failing the scan.
func Analyze(root string, opts Options) (*model.Repository, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", root)
	}
	repo, err := AnalyzeFS(os.DirFS(abs), rootImportPath(abs), opts)
	if err != nil {
		return nil, err
	}
	repo.Root = abs
	return repo, nil
}

// AnalyzeFS scans the Go source tree at the root of fsys, like Analyze. It
// lets callers analyze sources that are not on disk, such as the files of a
// git commit.
//
// Import paths come from the go.mod files found in fsys. Packages outside
// all of them use rootPath as the import path of the root directory.
// Repository.Root is left empty.
func AnalyzeFS(fsys fs.FS, rootPath string, opts Options) (*model.Repository, error) {
	s := &scanner{
		fsys:     fsys,
		rootPath: rootPath,
		opts:     opts,
		fset:     token.NewFileSet(),
		modules:  make(map[string]string),
		pkgs:     make(map[string]*pkgInfo),
		funcs:    make(map[string]*model.Function),
		results:  make(map[string]typeRef),
		types:    make(map[typeRef]*typeInfo),
		vars:     make(map[string]typeRef),
	}
	s.seedKnownExternals()
	if err := s.walk(); err != nil {
		return nil, err
	}
	s.collectImports()
	s.collectDecls()
	s.collectPackageVars()
	s.collectCalls()
	return s.repository(), nil
}

// Wants reports whether the analyzer reads the file at the slash-separated
// path name, relative to the root: a Go file outside the skipped directories,
// or a go.mod file. Callers that load files into an fs.FS for AnalyzeFS can
// use it to load only what is needed.
func Wants(name string, opts Options) bool {
	dir, base := path.Split(name)
	for _, elem := range strings.Split(dir, "/") {
		if elem != "" && skipDir(elem) {
			return false
		}
	}
	return base == "go.mod" || wantFile(base, opts)
}

// rootImportPath returns the import path of a directory on disk whose
// module's go.mod is in a parent directory, so that import paths stay right
// when only part of a module is scanned. Outside any module, the directory
// name stands in for the module path.
func rootImportPath(dir string) string {
	for d := filepath.Dir(dir); ; d = filepath.Dir(d) {
		if data, err := os.ReadFile(filepath.Join(d, "go.mod")); err == nil {
			if mod := parseModulePath(data); mod != "" {
				rel, _ := filepath.Rel(d, dir)
				return mod + "/" + filepath.ToSlash(rel)
			}
		}
		if filepath.Dir(d) == d {
			return filepath.Base(dir)
		}
	}
}

// scanner holds the state of one AnalyzeFS call. Resolving a call can require
// declarations from any package, so the work is done in passes:
//
//  1. walk: find go.mod files and parse every .go file
//  2. collectImports: map the import names of each file to import paths
//  3. collectDecls: record functions, methods and types
//  4. collectPackageVars: record the types of package-level variables
//  5. collectCalls: walk function bodies and resolve call expressions
//
// All paths are slash-separated and relative to the root of fsys.
type scanner struct {
	fsys     fs.FS
	rootPath string // import path of the root directory when it is not in a module
	opts     Options
	fset     *token.FileSet
	modules  map[string]string          // directory -> module path, for each directory with a go.mod
	pkgs     map[string]*pkgInfo        // by package ID
	funcs    map[string]*model.Function // by Function.ID
	results  map[string]typeRef         // type of a function's first result, by Function.ID
	types    map[typeRef]*typeInfo      // every declared package-level type
	vars     map[string]typeRef         // "<package ID>.<name>" of a package-level variable -> its type
	warnings []string
}

type pkgInfo struct {
	pkg   *model.Package
	files []*fileInfo
	inits int // init functions seen so far, used to give each a unique ID
}

type fileInfo struct {
	model   *model.File
	syntax  *ast.File
	pkg     *pkgInfo
	imports map[string]string // name used in this file -> import path
	funcs   []funcDecl
}

type funcDecl struct {
	decl *ast.FuncDecl
	fn   *model.Function
}

func (s *scanner) warnf(format string, args ...any) {
	s.warnings = append(s.warnings, fmt.Sprintf(format, args...))
}

func (s *scanner) walk() error {
	return fs.WalkDir(s.fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			if name == "." {
				return err
			}
			s.warnf("%s: %v", name, err)
			return nil
		}
		if d.IsDir() {
			if name != "." && skipDir(d.Name()) {
				return fs.SkipDir
			}
			s.readModule(name)
			return nil
		}
		if wantFile(d.Name(), s.opts) {
			s.parseFile(name)
		}
		return nil
	})
}

func skipDir(name string) bool {
	return name == "vendor" || name == "testdata" ||
		strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}

func wantFile(name string, opts Options) bool {
	if !strings.HasSuffix(name, ".go") || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
		return false
	}
	return opts.IncludeTests || !strings.HasSuffix(name, "_test.go")
}

// readModule records the module path if dir contains a go.mod file.
func (s *scanner) readModule(dir string) {
	name := path.Join(dir, "go.mod")
	data, err := fs.ReadFile(s.fsys, name)
	if err != nil {
		return
	}
	if mod := parseModulePath(data); mod != "" {
		s.modules[dir] = mod
	} else {
		s.warnf("%s: go.mod has no module directive", name)
	}
}

// parseModulePath returns the module path declared in a go.mod file.
func parseModulePath(gomod []byte) string {
	for _, line := range strings.Split(string(gomod), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "module" {
			return strings.Trim(fields[1], "\"`")
		}
	}
	return ""
}

// importPath returns the import path of the package in dir, derived from the
// nearest enclosing go.mod, or from rootPath outside any module.
func (s *scanner) importPath(dir string) string {
	for d := dir; ; d = path.Dir(d) {
		if mod, ok := s.modules[d]; ok {
			return joinPath(mod, d, dir)
		}
		if d == "." {
			return joinPath(s.rootPath, ".", dir)
		}
	}
}

// joinPath appends the path of dir, relative to its ancestor base, to prefix.
func joinPath(prefix, base, dir string) string {
	switch {
	case dir == base:
		return prefix
	case base == ".":
		return prefix + "/" + dir
	}
	return prefix + "/" + strings.TrimPrefix(dir, base+"/")
}

func (s *scanner) parseFile(name string) {
	src, err := fs.ReadFile(s.fsys, name)
	if err != nil {
		s.warnf("%s: %v", name, err)
		return
	}
	f, err := parser.ParseFile(s.fset, name, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		s.warnf("%s (file skipped)", parseError(name, src, err))
		return
	}
	if hasIgnoreTag(f) {
		return
	}

	dir := path.Dir(name)
	id, pkgName := s.importPath(dir), f.Name.Name
	if strings.HasSuffix(pkgName, "_test") && strings.HasSuffix(name, "_test.go") {
		id += "_test"
	}

	p := s.pkgs[id]
	if p == nil {
		p = &pkgInfo{pkg: &model.Package{ID: id, Name: pkgName, Dir: dir}}
		s.pkgs[id] = p
	} else if p.pkg.Name != pkgName {
		s.warnf("%s: package %s conflicts with package %s in the same directory (file skipped)", name, pkgName, p.pkg.Name)
		return
	}
	p.files = append(p.files, &fileInfo{
		model:  &model.File{Path: name, Package: id},
		syntax: f,
		pkg:    p,
	})
}

// parseError formats an error of the parser for the file name with src. The
// parser places errors after //line directives, which may name another file
// and line; the position is recomputed in the file itself, so the warning
// names the file it is about.
func parseError(name string, src []byte, err error) string {
	var list goscanner.ErrorList
	if !errors.As(err, &list) || len(list) == 0 {
		return err.Error()
	}
	e := list[0]
	off := e.Pos.Offset
	if !e.Pos.IsValid() || off < 0 || off > len(src) {
		return err.Error()
	}
	line := 1 + bytes.Count(src[:off], []byte("\n"))
	col := off - bytes.LastIndexByte(src[:off], '\n')
	msg := fmt.Sprintf("%s:%d:%d: %s", name, line, col, e.Msg)
	if len(list) > 1 {
		msg += fmt.Sprintf(" (and %d more errors)", len(list)-1)
	}
	return msg
}

// hasIgnoreTag reports whether f is excluded with "//go:build ignore", the
// usual way of keeping helper programs such as generators out of a package.
func hasIgnoreTag(f *ast.File) bool {
	for _, group := range f.Comments {
		if group.Pos() >= f.Package {
			break
		}
		for _, c := range group.List {
			if !constraint.IsGoBuild(c.Text) && !constraint.IsPlusBuild(c.Text) {
				continue
			}
			expr, err := constraint.Parse(c.Text)
			if err != nil {
				continue
			}
			if tag, ok := expr.(*constraint.TagExpr); ok && tag.Tag == "ignore" {
				return true
			}
		}
	}
	return false
}

func (s *scanner) sortedPackages() []*pkgInfo {
	pkgs := make([]*pkgInfo, 0, len(s.pkgs))
	for _, p := range s.pkgs {
		pkgs = append(pkgs, p)
	}
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].pkg.ID < pkgs[j].pkg.ID })
	return pkgs
}

func (s *scanner) collectImports() {
	for _, p := range s.pkgs {
		for _, fi := range p.files {
			fi.imports = make(map[string]string)
			for _, spec := range fi.syntax.Imports {
				path, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					continue
				}
				name := s.packageName(path)
				if spec.Name != nil {
					name = spec.Name.Name
				}
				// Blank imports introduce no name; dot imports are not supported.
				if name == "_" || name == "." {
					continue
				}
				fi.imports[name] = path
			}
		}
	}
}

// packageName returns the name an import is referred to by when it has no
// explicit alias. For packages in the repository the declared name is known;
// for others it is guessed from the import path.
func (s *scanner) packageName(path string) string {
	if p := s.pkgs[path]; p != nil {
		return p.pkg.Name
	}
	return guessPackageName(path)
}

// guessPackageName follows the common conventions: the last path element,
// skipping a major version suffix ("example.com/foo/v2" -> "foo"), dropping
// a gopkg.in version ("gopkg.in/yaml.v3" -> "yaml") and a "go-" prefix
// ("github.com/mattn/go-sqlite3" -> "sqlite3").
func guessPackageName(path string) string {
	parts := strings.Split(path, "/")
	name := parts[len(parts)-1]
	if isMajorVersion(name) && len(parts) > 1 {
		name = parts[len(parts)-2]
	}
	name, _, _ = strings.Cut(name, ".")
	name = strings.TrimPrefix(name, "go-")
	return strings.TrimSuffix(name, "-go")
}

func isMajorVersion(s string) bool {
	if len(s) < 2 || s[0] != 'v' {
		return false
	}
	_, err := strconv.Atoi(s[1:])
	return err == nil
}

func (s *scanner) collectDecls() {
	// Sorted so init functions are numbered the same way on every run.
	for _, p := range s.sortedPackages() {
		for _, fi := range p.files {
			for _, decl := range fi.syntax.Decls {
				switch d := decl.(type) {
				case *ast.FuncDecl:
					s.addFunc(fi, d)
				case *ast.GenDecl:
					if d.Tok != token.IMPORT {
						fi.model.Declarations = append(fi.model.Declarations, s.declaration(fi, d, d.Doc))
					}
					if d.Tok == token.TYPE {
						for _, spec := range d.Specs {
							s.addType(fi, spec.(*ast.TypeSpec))
						}
					}
				}
			}
		}
	}
}

func (s *scanner) addFunc(fi *fileInfo, d *ast.FuncDecl) {
	name := d.Name.Name
	if name == "_" {
		return
	}
	p := fi.pkg
	recv := receiverType(d)
	id := p.pkg.ID + "." + name
	switch {
	case recv != "":
		id = p.pkg.ID + "." + recv + "." + name
	case name == "init":
		// A package may declare any number of init functions.
		id = fmt.Sprintf("%s.init#%d", p.pkg.ID, p.inits)
		p.inits++
	}

	loc := s.declaration(fi, d, d.Doc)

	// The same function may be declared in several files guarded by build
	// constraints (foo_linux.go, foo_windows.go). To callers they are one
	// symbol, so the declarations share the Function of the first one.
	fn := s.funcs[id]
	if fn == nil {
		fn = &model.Function{
			ID:        id,
			Name:      name,
			Receiver:  recv,
			Exported:  token.IsExported(name),
			Package:   p.pkg.ID,
			File:      loc.File,
			StartLine: loc.StartLine,
			EndLine:   loc.EndLine,
			Test:      s.testKind(fi, d),
		}
		s.funcs[id] = fn
		fi.model.Functions = append(fi.model.Functions, fn)
		if res := d.Type.Results; res != nil && len(res.List) > 0 {
			s.results[id] = s.typeOf(fi, res.List[0].Type)
		}
	}
	fn.Declarations = append(fn.Declarations, loc)
	fi.funcs = append(fi.funcs, funcDecl{decl: d, fn: fn})
}

// declaration returns the lines of a declaration, its doc comment included.
func (s *scanner) declaration(fi *fileInfo, n ast.Node, doc *ast.CommentGroup) model.Declaration {
	start := n.Pos()
	if doc != nil {
		start = doc.Pos()
	}
	return model.Declaration{
		File:      fi.model.Path,
		StartLine: s.line(start),
		EndLine:   s.line(n.End()),
	}
}

// line returns the line of pos in its file as stored in the repository.
// //line directives, common in generated code, are ignored: lines are
// matched against git diffs and reported against the repository's files.
func (s *scanner) line(pos token.Pos) int { return s.fset.PositionFor(pos, false).Line }

// receiverType returns the receiver type name of a method ("Service" for
// (s *Service), "List" for (l List[T])), or "" for a function.
func receiverType(d *ast.FuncDecl) string {
	if d.Recv == nil || len(d.Recv.List) == 0 {
		return ""
	}
	expr := d.Recv.List[0].Type
	for {
		switch t := expr.(type) {
		case *ast.StarExpr:
			expr = t.X
		case *ast.ParenExpr:
			expr = t.X
		case *ast.IndexExpr:
			expr = t.X
		case *ast.IndexListExpr:
			expr = t.X
		case *ast.Ident:
			return t.Name
		default:
			return ""
		}
	}
}

func (s *scanner) addType(fi *fileInfo, spec *ast.TypeSpec) {
	info := &typeInfo{fields: make(map[string]typeRef), methods: make(map[string]bool)}
	if it, ok := spec.Type.(*ast.InterfaceType); ok {
		info.iface = true
		for _, m := range it.Methods.List {
			for _, n := range m.Names {
				info.methods[n.Name] = true
			}
			// Embedded interfaces contribute their methods. typeOf leaves
			// out the elements of type sets, such as ~int | string.
			if len(m.Names) == 0 {
				if t := s.typeOf(fi, m.Type); t.known() {
					info.same = append(info.same, t)
				}
			}
		}
	}
	if spec.Assign.IsValid() {
		// An alias has the fields and methods of the aliased type. When
		// that type is unknown (type A = struct{ ... }), the unknown
		// typeRef keeps the alias's members unknown.
		info.same = append(info.same, s.typeOf(fi, spec.Type))
	}
	if st, ok := spec.Type.(*ast.StructType); ok {
		for _, field := range st.Fields.List {
			t := s.typeOf(fi, field.Type)
			if len(field.Names) == 0 {
				// An embedded field is named after its type.
				if t.known() {
					info.embedded = append(info.embedded, t)
					info.fields[t.name] = t
				}
				continue
			}
			for _, n := range field.Names {
				info.fields[n.Name] = t
			}
		}
	}
	s.types[typeRef{pkg: fi.pkg.pkg.ID, name: spec.Name.Name}] = info
}

func (s *scanner) collectPackageVars() {
	for _, p := range s.sortedPackages() {
		for _, fi := range p.files {
			r := s.newResolver(fi, nil)
			for _, decl := range fi.syntax.Decls {
				d, ok := decl.(*ast.GenDecl)
				if !ok || d.Tok != token.VAR {
					continue
				}
				for _, spec := range d.Specs {
					vs := spec.(*ast.ValueSpec)
					for i, name := range vs.Names {
						if t := r.specType(vs, i); t.known() {
							s.vars[p.pkg.ID+"."+name.Name] = t
						}
					}
				}
			}
		}
	}
}

func (s *scanner) collectCalls() {
	for _, p := range s.pkgs {
		for _, fi := range p.files {
			for _, fd := range fi.funcs {
				if fd.decl.Body == nil {
					continue // declared without a body, e.g. implemented in assembly
				}
				r := s.newResolver(fi, fd.fn)
				r.declare(fd.decl.Recv)
				r.declare(fd.decl.Type.Params)
				r.declare(fd.decl.Type.Results)
				ast.Inspect(fd.decl.Body, r.visit)
			}
		}
	}
}

func (s *scanner) repository() *model.Repository {
	repo := &model.Repository{Warnings: s.warnings}
	for _, p := range s.sortedPackages() {
		for _, fi := range p.files {
			p.pkg.Files = append(p.pkg.Files, fi.model)
		}
		repo.Packages = append(repo.Packages, p.pkg)
	}
	return repo
}
