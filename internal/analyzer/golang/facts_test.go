package golang

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Easlkad/impact/internal/model"
)

func routes(t *testing.T, repo *model.Repository, id string) []string {
	t.Helper()
	var out []string
	for _, r := range findFunc(t, repo, id).Routes {
		unknown := func(s string) string {
			if s == "" {
				return "?"
			}
			return s
		}
		out = append(out, unknown(r.Method)+" "+unknown(r.Path)+" -> "+unknown(r.Handler))
	}
	return out
}

func TestRoutes(t *testing.T) {
	repo := analyze(t, map[string]string{
		"go.mod": goMod,
		"pay/pay.go": `package pay

import "net/http"

func Charge(w http.ResponseWriter, r *http.Request) {}
`,
		"api/api.go": `package api

import (
	"net/http"

	"example.com/app/pay"
)

type Server struct{ mux *http.ServeMux }

type Health struct{}

func (Health) ServeHTTP(w http.ResponseWriter, r *http.Request) {}

func NewHealth() *Health { return &Health{} }

func Payments(w http.ResponseWriter, r *http.Request) {}

func Users(w http.ResponseWriter, r *http.Request) {}

func (s *Server) orders(w http.ResponseWriter, r *http.Request) {}

func Register(s *Server) {
	http.HandleFunc("/payments", Payments)
	http.Handle("/health", Health{})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /payments/{id}", Payments)
	mux.Handle("GET /users", http.HandlerFunc(Users))
	mux.Handle("/ready", NewHealth())
	s.mux.HandleFunc("GET /orders", s.orders)
	http.DefaultServeMux.HandleFunc("/api/"+"v1", pay.Charge)
	var plain http.ServeMux
	plain.Handle("DELETE /h", &Health{})
}
`,
		"api/alias.go": `package api

import web "net/http"

func RegisterAlias(m *web.ServeMux) {
	m.HandleFunc("PUT /alias", Users)
}
`,
	}, Options{})

	assertEqual(t, "routes of Register", routes(t, repo, "example.com/app/api.Register"), []string{
		"ANY /payments -> example.com/app/api.Payments",
		"ANY /health -> example.com/app/api.Health.ServeHTTP",
		"POST /payments/{id} -> example.com/app/api.Payments",
		"GET /users -> example.com/app/api.Users",
		"ANY /ready -> example.com/app/api.Health.ServeHTTP",
		"GET /orders -> example.com/app/api.Server.orders",
		"ANY /api/v1 -> example.com/app/pay.Charge",
		"DELETE /h -> example.com/app/api.Health.ServeHTTP",
	})
	assertEqual(t, "routes of RegisterAlias", routes(t, repo, "example.com/app/api.RegisterAlias"), []string{
		"PUT /alias -> example.com/app/api.Users",
	})
	if r := findFunc(t, repo, "example.com/app/api.Register").Routes[2]; r.Line != 27 {
		t.Errorf("route line = %d, want 27", r.Line)
	}
}

func TestRoutesThatCannotBeResolved(t *testing.T) {
	repo := analyze(t, map[string]string{
		"go.mod": goMod,
		"api/api.go": `package api

import (
	"net/http"

	fake "example.com/app/http"
)

type Router struct{}

func (Router) HandleFunc(p string, f http.HandlerFunc) {}

func Payments(w http.ResponseWriter, r *http.Request) {}

func withAuth(h http.Handler) http.Handler { return h }

func Register(mux *http.ServeMux, dynamic string, h http.Handler, custom Router, routes map[string]http.HandlerFunc) {
	prefix := "/api"
	mux.HandleFunc(dynamic, Payments)
	mux.HandleFunc(prefix+"/x", Payments)
	mux.HandleFunc("/inline", func(w http.ResponseWriter, r *http.Request) { Payments(w, r) })
	mux.Handle("/iface", h)
	mux.Handle("/wrapped", withAuth(http.HandlerFunc(Payments)))
	mux.HandleFunc("/map", routes["x"])
	mux.HandleFunc("get /lower", Payments)
	mux.HandleFunc("no-slash", Payments)
	custom.HandleFunc("/custom", Payments)
	fake.HandleFunc("/fake", Payments)
}
`,
		"http/http.go": "package http\n\nfunc HandleFunc(p string, f any) {}\n",
	}, Options{})

	// Registrations on a ServeMux are recorded with their unknown parts
	// empty (shown as "?"), and none of them is complete. Calls on other
	// types and packages are not registrations at all.
	assertEqual(t, "routes", routes(t, repo, "example.com/app/api.Register"), []string{
		"? ? -> example.com/app/api.Payments", // dynamic pattern
		"? ? -> example.com/app/api.Payments", // pattern built from a variable
		"ANY /inline -> ?",                    // function literal
		"ANY /iface -> ?",                     // interface value
		"ANY /wrapped -> ?",                   // middleware
		"ANY /map -> ?",                       // map lookup
		"? ? -> example.com/app/api.Payments", // invalid method
		"? ? -> example.com/app/api.Payments", // no "/" in the pattern
	})
	for _, r := range findFunc(t, repo, "example.com/app/api.Register").Routes {
		if r.Complete() {
			t.Errorf("route %+v is complete", r)
		}
	}
}

// modes formats the calls of a function as "<mode> <callee without package>".
func modes(t *testing.T, repo *model.Repository, id string) []string {
	t.Helper()
	names := map[model.CallMode]string{model.CallSync: "sync", model.CallAsync: "async", model.CallInAsync: "in-async"}
	var out []string
	for _, c := range findFunc(t, repo, id).Calls {
		out = append(out, fmt.Sprintf("%s %s", names[c.Mode], strings.TrimPrefix(c.Callee, "example.com/app/work.")))
	}
	return out
}

func TestGoStatements(t *testing.T) {
	repo := analyze(t, map[string]string{
		"go.mod": goMod,
		"work/work.go": `package work

type Service struct{}

func (s *Service) Run()            {}
func (s *Service) worker() *Service { return s }

func RunWorker()          {}
func RunWith(n int)       {}
func helper(n int) int    { return n }
func process()            {}

func Start(s *Service) {
	go RunWorker()
	go s.Run()
	go s.worker().Run()
	go RunWith(helper(1))
	go func() {
		process()
		func() { helper(2) }()
	}()
	helper(3)
	defer process()
}
`,
	}, Options{})

	assertEqual(t, "calls of Start", modes(t, repo, "example.com/app/work.Start"), []string{
		"async RunWorker",
		"async Service.Run",
		"async Service.Run",   // go s.worker().Run(): Run runs in the goroutine...
		"sync Service.worker", // ...but worker() is evaluated by the caller
		"sync helper",         // arguments are evaluated by the caller
		"async RunWith",
		"in-async process", // called in the body of a goroutine literal
		"sync helper",      // nested literal: not necessarily concurrent
		"sync helper",
		"sync process",
	})
}

func TestTestKinds(t *testing.T) {
	repo := analyze(t, map[string]string{
		"go.mod": goMod,
		"x/x.go": `package x

import "testing"

func TestLookalike(t *testing.T) {}
`,
		"x/x_test.go": `package x

import (
	"testing"
	tt "testing"
)

func TestSomething(t *testing.T)         {}
func Test(t *testing.T)                  {}
func TestAliased(t *tt.T)                {}
func BenchmarkSort(b *testing.B)         {}
func FuzzParse(f *testing.F)             {}
func ExampleHello()                      {}
func TestMain(m *testing.M)              {}
func Testify(t *testing.T)               {}
func TestWrongParam(t *testing.B)        {}
func TestValue(t testing.T)              {}
func TestResult(t *testing.T) error      { return nil }
func BenchmarkNoParam()                  {}
func ExampleWithParam(t *testing.T)      {}
func newFixture(t *testing.T)            {}

type S struct{}

func (S) TestMethod(t *testing.T) {}
`,
	}, Options{IncludeTests: true})

	var got []string
	for _, fn := range repo.Functions() {
		if fn.Test != model.NotTest {
			got = append(got, string(fn.Test)+" "+fn.Name)
		}
	}
	assertEqual(t, "tests", got, []string{
		"test TestSomething",
		"test Test",
		"test TestAliased",
		"benchmark BenchmarkSort",
		"fuzz FuzzParse",
		"example ExampleHello",
	})
}

func TestUnresolvedReasons(t *testing.T) {
	repo := analyze(t, map[string]string{
		"go.mod": goMod,
		"util/util.go": `package util

type Store interface{ Get() string }

type ReadStore interface{ Store }

type Config struct {
	OnSave func()
	Store
}

func getFn() func() { return nil }

func Run(s Store, rs ReadStore, c Config, hook func(), fns []func(), x any) {
	hook()
	c.OnSave()
	fns[0]()
	s.Get()
	rs.Get()
	c.Get()
	getFn()()
	x.(interface{ M() }).M()
}
`,
	}, Options{})

	names := map[model.UnresolvedReason]string{
		model.UnknownTarget:   "unknown",
		model.FunctionValue:   "function-value",
		model.InterfaceMethod: "interface",
	}
	var got []string
	for _, c := range findFunc(t, repo, "example.com/app/util.Run").Calls {
		if c.Kind == model.CallUnresolved {
			got = append(got, names[c.Reason]+" "+c.Callee)
		}
	}
	assertEqual(t, "unresolved calls", got, []string{
		"function-value hook",
		"function-value example.com/app/util.Config.OnSave",
		"function-value fns",
		"interface example.com/app/util.Store.Get",
		"interface example.com/app/util.ReadStore.Get", // embedded interface
		"interface example.com/app/util.Config.Get",    // promoted from an embedded interface
		"unknown getFn()",
		"unknown x.(interface{M()}).M",
	})
}
