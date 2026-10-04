package golang

import (
	"go/ast"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Easlkad/impact/internal/model"
)

// testKind returns the kind of a function that "go test" runs, following
// the rules of the go command: a top-level function in a _test.go file named
// TestXxx, BenchmarkXxx or FuzzXxx with a single *testing.T, *testing.B or
// *testing.F parameter, or ExampleXxx without parameters and results. Xxx
// must not start with a lower-case letter. TestMain is not a test.
func (s *scanner) testKind(fi *fileInfo, d *ast.FuncDecl) model.TestKind {
	if !strings.HasSuffix(fi.model.Path, "_test.go") || d.Recv != nil || d.Type.TypeParams != nil || d.Type.Results != nil {
		return model.NotTest
	}
	name, params := d.Name.Name, d.Type.Params.List

	if hasTestPrefix(name, "Example") && len(params) == 0 {
		return model.ExampleTest
	}
	if len(params) != 1 || len(params[0].Names) > 1 {
		return model.NotTest
	}
	star, ok := params[0].Type.(*ast.StarExpr)
	if !ok {
		return model.NotTest
	}
	for _, k := range []struct {
		prefix, param string
		kind          model.TestKind
	}{
		{"Test", "T", model.UnitTest},
		{"Benchmark", "B", model.Benchmark},
		{"Fuzz", "F", model.FuzzTest},
	} {
		if hasTestPrefix(name, k.prefix) && s.typeOf(fi, star.X) == (typeRef{pkg: "testing", name: k.param}) {
			return k.kind
		}
	}
	return model.NotTest
}

// hasTestPrefix reports whether name is prefix followed by nothing or by a
// character that is not a lower-case letter: Test and TestFoo, not Testify.
func hasTestPrefix(name, prefix string) bool {
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	rest := name[len(prefix):]
	if rest == "" {
		return true
	}
	r, _ := utf8.DecodeRuneInString(rest)
	return !unicode.IsLower(r)
}
