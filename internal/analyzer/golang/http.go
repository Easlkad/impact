package golang

// Detection of net/http route registrations.
//
// Only registrations that can be understood without running the code are
// recorded, and nothing is guessed:
//
//	http.HandleFunc(pattern, f)   http.Handle(pattern, h)
//	mux.HandleFunc(pattern, f)    mux.Handle(pattern, h)
//
// mux must be known to be an *http.ServeMux (http.NewServeMux(),
// &http.ServeMux{}, http.DefaultServeMux, or a variable, field or parameter
// declared with that type). The pattern is known when it is a string
// literal, or a concatenation of string literals. The handler is known when
// it resolves to a function of the repository: a function or method value for
// HandleFunc, possibly wrapped in http.HandlerFunc(...), or for Handle a value
// whose type has a ServeHTTP method. Function literals, middleware calls and
// interface values are not resolved. A registration whose pattern or handler
// is unknown is recorded incomplete (see model.Route).

import (
	"go/ast"
	"go/token"
	"strconv"
	"strings"

	"github.com/Easlkad/impact/internal/model"
)

var serveMux = typeRef{pkg: "net/http", name: "ServeMux"}

// seedKnownExternals records the types that let the resolver recognize a
// *http.ServeMux, although net/http is not part of the scanned repository.
func (s *scanner) seedKnownExternals() {
	s.results["net/http.NewServeMux"] = serveMux
	s.vars["net/http.DefaultServeMux"] = serveMux
}

// recordRoute records call as a route of the function if it is a route
// registration that can be resolved statically.
func (r *resolver) recordRoute(call *ast.CallExpr) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || len(call.Args) != 2 || (sel.Sel.Name != "HandleFunc" && sel.Sel.Name != "Handle") {
		return
	}
	if path, ok := r.importedPackage(sel.X); ok {
		if path != "net/http" {
			return
		}
	} else if r.exprType(sel.X) != serveMux {
		return
	}

	// The call is certainly a registration. What it registers may still be
	// unknown; the route is then recorded incomplete, with the unknown
	// parts empty, so the gap is visible to later analysis.
	var method, path string
	if pattern, ok := stringValue(call.Args[0]); ok {
		method, path, _ = parsePattern(pattern)
	}
	handler, _ := r.handler(call.Args[1], sel.Sel.Name == "HandleFunc")
	r.fn.Routes = append(r.fn.Routes, model.Route{
		Method:  method,
		Path:    path,
		Handler: handler,
		Line:    r.s.fset.Position(call.Lparen).Line,
	})
}

// stringValue returns the value of a string literal, or of a concatenation
// of string literals.
func stringValue(e ast.Expr) (string, bool) {
	switch e := ast.Unparen(e).(type) {
	case *ast.BasicLit:
		if e.Kind == token.STRING {
			s, err := strconv.Unquote(e.Value)
			return s, err == nil
		}
	case *ast.BinaryExpr:
		if e.Op == token.ADD {
			x, okX := stringValue(e.X)
			y, okY := stringValue(e.Y)
			return x + y, okX && okY
		}
	}
	return "", false
}

// parsePattern splits a ServeMux pattern "[METHOD ][HOST]/[PATH]" into its
// method ("ANY" if absent) and the rest. Patterns that ServeMux would reject
// are refused.
func parsePattern(pattern string) (method, path string, ok bool) {
	method, path = "ANY", pattern
	if i := strings.IndexAny(pattern, " \t"); i >= 0 {
		method, path = pattern[:i], strings.TrimLeft(pattern[i+1:], " \t")
		if method == "" || strings.ToUpper(method) != method {
			return "", "", false
		}
	}
	if !strings.Contains(path, "/") || strings.ContainsAny(path, " \t") {
		return "", "", false
	}
	return method, path, true
}

// handler returns the Function.ID of the function serving requests for the
// handler argument of HandleFunc (isFunc) or Handle.
func (r *resolver) handler(e ast.Expr, isFunc bool) (string, bool) {
	e = ast.Unparen(e)
	// http.HandlerFunc(f) converts a function into a Handler.
	if call, ok := e.(*ast.CallExpr); ok && len(call.Args) == 1 {
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "HandlerFunc" {
			if path, ok := r.importedPackage(sel.X); ok && path == "net/http" {
				return r.funcValue(call.Args[0])
			}
		}
	}
	if isFunc {
		return r.funcValue(e)
	}
	// Any other Handler serves requests with its ServeHTTP method.
	return r.s.lookupMethod(r.exprType(e), "ServeHTTP")
}

// funcValue returns the Function.ID of a function or method value, such as
// PaymentHandler, api.PaymentHandler or s.handlePayments. A value resolves
// exactly like a call of it would, so it is resolved as one.
func (r *resolver) funcValue(e ast.Expr) (string, bool) {
	call, ok := r.resolveCall(&ast.CallExpr{Fun: e})
	if !ok || call.Kind != model.CallInternal {
		return "", false
	}
	return call.Callee, true
}
