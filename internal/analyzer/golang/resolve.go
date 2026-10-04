package golang

// Call resolution without type checking.
//
// A call is resolved from the shape of the called expression:
//
//	Foo()        a function declared in the same package
//	pkg.Foo()    a function in an imported package
//	x.Method()   a method of the static type of x
//
// The type of x is inferred from how x was declared: a receiver or parameter,
// "var x T", a composite literal (T{} or &T{}), new(T), a type assertion
// x.(T), a call to a function whose first result has a named type, or a
// struct field reached through any of these. Embedded fields are followed
// when looking up fields and methods.
//
// Local scopes are flattened per function: a name keeps the type from its
// latest declaration even after its block ends. Calls that cannot be
// resolved this way (function values, interface methods, results of
// unresolved calls, ...) are recorded as unresolved rather than guessed.

import (
	"go/ast"
	"go/token"
	"go/types"

	"github.com/Easlkad/impact/internal/model"
)

// typeRef names a declared type by the ID of its package and its name. The
// zero value means the type is unknown.
type typeRef struct {
	pkg, name string
}

func (t typeRef) known() bool { return t.name != "" }

func (t typeRef) String() string { return t.pkg + "." + t.name }

// typeInfo is what the resolver needs to know about a declared type.
type typeInfo struct {
	fields   map[string]typeRef // struct fields, including embedded ones
	embedded []typeRef          // embedded types (or the aliased type), whose fields and methods are promoted
	iface    bool               // an interface type
}

// predeclared holds Go's builtin functions and types. Calling one is a
// builtin operation or a conversion, not a call relationship.
var predeclared = map[string]bool{
	"append": true, "cap": true, "clear": true, "close": true, "complex": true,
	"copy": true, "delete": true, "imag": true, "len": true, "make": true,
	"max": true, "min": true, "new": true, "panic": true, "print": true,
	"println": true, "real": true, "recover": true,

	"any": true, "bool": true, "byte": true, "comparable": true,
	"complex64": true, "complex128": true, "error": true, "float32": true,
	"float64": true, "int": true, "int8": true, "int16": true, "int32": true,
	"int64": true, "rune": true, "string": true, "uint": true, "uint8": true,
	"uint16": true, "uint32": true, "uint64": true, "uintptr": true,
}

// typeOf returns the named type a type expression refers to, ignoring
// pointers and type arguments: *store.Repo and List[int] give store.Repo and
// List. Unnamed types (slices, maps, funcs, ...) and builtins are unknown.
func (s *scanner) typeOf(fi *fileInfo, expr ast.Expr) typeRef {
	switch t := expr.(type) {
	case *ast.Ident:
		if !predeclared[t.Name] {
			return typeRef{pkg: fi.pkg.pkg.ID, name: t.Name}
		}
	case *ast.SelectorExpr:
		if x, ok := t.X.(*ast.Ident); ok {
			if path, ok := fi.imports[x.Name]; ok {
				return typeRef{pkg: path, name: t.Sel.Name}
			}
		}
	case *ast.StarExpr:
		return s.typeOf(fi, t.X)
	case *ast.ParenExpr:
		return s.typeOf(fi, t.X)
	case *ast.IndexExpr:
		return s.typeOf(fi, t.X)
	case *ast.IndexListExpr:
		return s.typeOf(fi, t.X)
	}
	return typeRef{}
}

// fieldType returns the type of field name of t, following embedded types.
func (s *scanner) fieldType(t typeRef, name string) typeRef {
	return s.findField(t, name, make(map[typeRef]bool))
}

func (s *scanner) findField(t typeRef, name string, seen map[typeRef]bool) typeRef {
	info := s.types[t]
	if info == nil || seen[t] {
		return typeRef{}
	}
	seen[t] = true
	if f, ok := info.fields[name]; ok {
		return f
	}
	for _, e := range info.embedded {
		if f := s.findField(e, name, seen); f.known() {
			return f
		}
	}
	return typeRef{}
}

// lookupMethod returns the Function.ID of method name of t, following
// embedded types.
func (s *scanner) lookupMethod(t typeRef, name string) (string, bool) {
	return s.findMethod(t, name, make(map[typeRef]bool))
}

func (s *scanner) findMethod(t typeRef, name string, seen map[typeRef]bool) (string, bool) {
	if seen[t] {
		return "", false
	}
	seen[t] = true
	id := t.String() + "." + name
	if s.funcs[id] != nil {
		return id, true
	}
	if info := s.types[t]; info != nil {
		for _, e := range info.embedded {
			if id, ok := s.findMethod(e, name, seen); ok {
				return id, true
			}
		}
	}
	return "", false
}

// hasField reports whether t has a field name, following embedded types.
// Unlike fieldType, it also finds fields whose type is unknown, such as
// fields holding functions.
func (s *scanner) hasField(t typeRef, name string) bool {
	return s.anyEmbedded(t, func(info *typeInfo) bool {
		_, ok := info.fields[name]
		return ok
	})
}

// isInterface reports whether t is an interface type, or embeds one.
func (s *scanner) isInterface(t typeRef) bool {
	return s.anyEmbedded(t, func(info *typeInfo) bool { return info.iface })
}

// anyEmbedded reports whether match holds for t or a type it embeds.
func (s *scanner) anyEmbedded(t typeRef, match func(*typeInfo) bool) bool {
	seen := make(map[typeRef]bool)
	var visit func(t typeRef) bool
	visit = func(t typeRef) bool {
		info := s.types[t]
		if info == nil || seen[t] {
			return false
		}
		seen[t] = true
		if match(info) {
			return true
		}
		for _, e := range info.embedded {
			if visit(e) {
				return true
			}
		}
		return false
	}
	return visit(t)
}

// resolver resolves the calls of one function body, or the initializers of
// package-level variables when fn is nil.
type resolver struct {
	s      *scanner
	file   *fileInfo
	pkg    string             // ID of the enclosing package
	fn     *model.Function    // function whose calls are recorded
	locals map[string]typeRef // local names declared so far; a zero typeRef means the type is unknown
	mode   model.CallMode     // mode of the calls being visited
}

func (s *scanner) newResolver(fi *fileInfo, fn *model.Function) *resolver {
	return &resolver{s: s, file: fi, pkg: fi.pkg.pkg.ID, fn: fn, locals: make(map[string]typeRef)}
}

func (r *resolver) define(name string, t typeRef) {
	if name != "_" {
		r.locals[name] = t
	}
}

// declare defines the named parameters, results or receiver in fields.
func (r *resolver) declare(fields *ast.FieldList) {
	if fields == nil {
		return
	}
	for _, f := range fields.List {
		t := r.s.typeOf(r.file, f.Type)
		for _, name := range f.Names {
			r.define(name.Name, t)
		}
	}
}

// visit is the ast.Inspect callback for a function body. Declarations are
// handled by hand so that their right-hand side is visited before the new
// names exist: in "user := user.New()" the call refers to the package user.
func (r *resolver) visit(n ast.Node) bool {
	switch n := n.(type) {
	case *ast.FuncLit:
		// A function literal runs when it is called, which is not
		// necessarily concurrently with the code around it.
		r.funcLit(n, model.CallSync)
		return false

	case *ast.GoStmt:
		r.goStmt(n)
		return false

	case *ast.AssignStmt:
		if n.Tok != token.DEFINE {
			return true
		}
		inferred := make([]typeRef, len(n.Lhs))
		for i := range n.Lhs {
			inferred[i] = r.valueType(n.Rhs, len(n.Lhs), i)
		}
		r.inspectAll(n.Rhs)
		for i, lhs := range n.Lhs {
			if id, ok := lhs.(*ast.Ident); ok {
				r.define(id.Name, inferred[i])
			}
		}
		return false

	case *ast.ValueSpec: // local var and const declarations
		inferred := make([]typeRef, len(n.Names))
		for i := range n.Names {
			inferred[i] = r.specType(n, i)
		}
		r.inspectAll(n.Values)
		for i, id := range n.Names {
			r.define(id.Name, inferred[i])
		}
		return false

	case *ast.RangeStmt:
		if n.Tok != token.DEFINE {
			return true
		}
		ast.Inspect(n.X, r.visit)
		for _, e := range []ast.Expr{n.Key, n.Value} {
			if id, ok := e.(*ast.Ident); ok {
				r.define(id.Name, typeRef{})
			}
		}
		ast.Inspect(n.Body, r.visit)
		return false

	case *ast.CallExpr:
		r.record(n, r.mode)
		r.recordRoute(n)
	}
	return true
}

// record adds a call to the function, if it is a call relationship.
func (r *resolver) record(n *ast.CallExpr, mode model.CallMode) {
	if call, ok := r.resolveCall(n); ok {
		call.Line = r.s.line(n.Lparen)
		call.Mode = mode
		r.fn.Calls = append(r.fn.Calls, call)
	}
}

// funcLit visits a function literal, recording its calls with mode.
func (r *resolver) funcLit(lit *ast.FuncLit, mode model.CallMode) {
	r.declare(lit.Type.Params)
	r.declare(lit.Type.Results)
	outer := r.mode
	r.mode = mode
	ast.Inspect(lit.Body, r.visit)
	r.mode = outer
}

// goStmt visits "go f(args)" or "go func() { ... }(args)". Only the call
// itself, or the body of the literal, runs in the new goroutine: the
// function expression and the arguments are evaluated by the caller.
func (r *resolver) goStmt(g *ast.GoStmt) {
	r.inspectAll(g.Call.Args)
	if lit, ok := ast.Unparen(g.Call.Fun).(*ast.FuncLit); ok {
		r.funcLit(lit, model.CallInAsync)
		return
	}
	r.record(g.Call, model.CallAsync)
	ast.Inspect(g.Call.Fun, r.visit)
}

func (r *resolver) inspectAll(exprs []ast.Expr) {
	for _, e := range exprs {
		ast.Inspect(e, r.visit)
	}
}

// specType returns the type of the i-th name declared by a var or const spec.
func (r *resolver) specType(vs *ast.ValueSpec, i int) typeRef {
	if vs.Type != nil {
		return r.s.typeOf(r.file, vs.Type)
	}
	return r.valueType(vs.Values, len(vs.Names), i)
}

// valueType returns the type of the i-th of n names assigned from values, as
// in "a, b := x, y" or "a, err := f()".
func (r *resolver) valueType(values []ast.Expr, n, i int) typeRef {
	switch {
	case len(values) == n:
		return r.exprType(values[i])
	case len(values) == 1 && i == 0:
		return r.exprType(values[0])
	}
	return typeRef{}
}

// exprType infers the named type of an expression, ignoring pointers.
func (r *resolver) exprType(e ast.Expr) typeRef {
	switch e := e.(type) {
	case *ast.Ident:
		if t, ok := r.locals[e.Name]; ok {
			return t
		}
		return r.s.vars[r.pkg+"."+e.Name]
	case *ast.SelectorExpr:
		if path, ok := r.importedPackage(e.X); ok {
			return r.s.vars[path+"."+e.Sel.Name]
		}
		return r.s.fieldType(r.exprType(e.X), e.Sel.Name)
	case *ast.ParenExpr:
		return r.exprType(e.X)
	case *ast.StarExpr:
		return r.exprType(e.X)
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			return r.exprType(e.X)
		}
	case *ast.CompositeLit:
		if e.Type != nil {
			return r.s.typeOf(r.file, e.Type)
		}
	case *ast.TypeAssertExpr:
		if e.Type != nil {
			return r.s.typeOf(r.file, e.Type)
		}
	case *ast.CallExpr:
		if id, ok := e.Fun.(*ast.Ident); ok && id.Name == "new" && len(e.Args) == 1 && !r.isLocal("new") {
			return r.s.typeOf(r.file, e.Args[0])
		}
		// results holds the repository's functions and a few well-known
		// external ones, such as http.NewServeMux.
		if call, ok := r.resolveCall(e); ok {
			return r.s.results[call.Callee]
		}
	}
	return typeRef{}
}

func (r *resolver) isLocal(name string) bool {
	_, ok := r.locals[name]
	return ok
}

// importedPackage returns the import path when x names an imported package
// that is not shadowed by a local name.
func (r *resolver) importedPackage(x ast.Expr) (string, bool) {
	id, ok := x.(*ast.Ident)
	if !ok || r.isLocal(id.Name) {
		return "", false
	}
	path, ok := r.file.imports[id.Name]
	return path, ok
}

// resolveCall determines the target of a call. It reports false for calls
// that are not call relationships: builtins, conversions, and immediately
// invoked function literals (whose bodies are walked as part of the
// enclosing function).
func (r *resolver) resolveCall(call *ast.CallExpr) (model.Call, bool) {
	switch fun := unwrapFun(call.Fun).(type) {
	case *ast.Ident:
		return r.resolveIdent(fun.Name)
	case *ast.SelectorExpr:
		return r.resolveSelector(fun)
	case *ast.FuncLit, *ast.ArrayType, *ast.MapType, *ast.ChanType, *ast.FuncType, *ast.InterfaceType, *ast.StarExpr:
		return model.Call{}, false
	}
	return unresolved(types.ExprString(call.Fun), model.UnknownTarget) // the result of a call, an index expression, ...
}

// unwrapFun strips parentheses and generic instantiations (Map[int, string])
// from the function expression of a call.
func unwrapFun(e ast.Expr) ast.Expr {
	for {
		switch t := e.(type) {
		case *ast.ParenExpr:
			e = t.X
		case *ast.IndexExpr:
			e = t.X
		case *ast.IndexListExpr:
			e = t.X
		default:
			return e
		}
	}
}

func (r *resolver) resolveIdent(name string) (model.Call, bool) {
	if r.isLocal(name) {
		return unresolved(name, model.FunctionValue) // a local variable or parameter
	}
	id := r.pkg + "." + name
	if r.s.funcs[id] != nil {
		return internal(id)
	}
	if predeclared[name] || r.s.types[typeRef{pkg: r.pkg, name: name}] != nil {
		return model.Call{}, false
	}
	return unresolved(name, model.UnknownTarget)
}

func (r *resolver) resolveSelector(sel *ast.SelectorExpr) (model.Call, bool) {
	name := sel.Sel.Name
	if path, ok := r.importedPackage(sel.X); ok {
		id := path + "." + name
		switch {
		case r.s.funcs[id] != nil:
			return internal(id)
		case r.s.pkgs[path] == nil:
			return external(id)
		case r.s.types[typeRef{pkg: path, name: name}] != nil:
			return model.Call{}, false // conversion to an imported type
		}
		return unresolved(id, model.UnknownTarget)
	}

	t := r.exprType(sel.X)
	if !t.known() {
		return unresolved(types.ExprString(sel), model.UnknownTarget)
	}
	if id, ok := r.s.lookupMethod(t, name); ok {
		return internal(id)
	}
	if r.s.pkgs[t.pkg] == nil {
		return external(t.String() + "." + name)
	}
	// A type of the repository without that method: a field holding a
	// function, an interface, or a method promoted from an embedded type
	// defined elsewhere.
	reason := model.UnknownTarget
	switch {
	case r.s.hasField(t, name):
		reason = model.FunctionValue
	case r.s.isInterface(t):
		reason = model.InterfaceMethod
	}
	return unresolved(t.String()+"."+name, reason)
}

func internal(id string) (model.Call, bool) {
	return model.Call{Callee: id, Kind: model.CallInternal}, true
}

func external(id string) (model.Call, bool) {
	return model.Call{Callee: id, Kind: model.CallExternal}, true
}

func unresolved(desc string, reason model.UnresolvedReason) (model.Call, bool) {
	return model.Call{Callee: desc, Kind: model.CallUnresolved, Reason: reason}, true
}
