// Package model defines the in-memory representation of a scanned repository:
// packages, files, functions and the calls between them.
//
// The types are language-neutral. A language analyzer (see internal/analyzer)
// produces a *Repository, and everything downstream (graph building, output)
// depends only on this package.
package model

// Repository is the result of scanning a source tree.
type Repository struct {
	Root     string     // absolute path of the scanned directory
	Packages []*Package // sorted by ID
	Warnings []string   // non-fatal problems, e.g. files that failed to parse
}

// Package is a group of files sharing one namespace.
type Package struct {
	ID    string  // unique identifier; for Go, the import path
	Name  string  // declared name, e.g. "user"
	Dir   string  // directory relative to the repository root, slash-separated
	Files []*File // sorted by Path
}

// File is a single source file.
type File struct {
	Path      string      // relative to the repository root, slash-separated
	Package   string      // ID of the owning package
	Functions []*Function // in source order
	// Declarations are the package-level declarations other than functions
	// and imports (types, constants, variables), in source order. For Go,
	// one entry covers a whole "const ( ... )" block.
	Declarations []Declaration
}

// Function is a function or method declaration.
type Function struct {
	// ID is unique within the repository: "<package ID>.<Name>" for
	// functions and "<package ID>.<Receiver>.<Name>" for methods.
	ID        string
	Name      string
	Receiver  string // receiver type name for methods, e.g. "Service"; empty for functions
	Exported  bool   // visible outside its package
	Package   string // ID of the owning package
	File      string // path of the declaring file
	StartLine int    // first line of the declaration, including its doc comment
	EndLine   int
	// Declarations lists every declaration of the function. There is more
	// than one only when build-constrained files declare the same function
	// (foo_linux.go, foo_windows.go); the first one is File/StartLine/EndLine.
	Declarations []Declaration
	Calls        []Call   // call sites in the body, including those inside closures
	Routes       []Route  // HTTP routes registered in the body
	Test         TestKind // set when the test runner calls the function
}

// TestKind says how a test runner uses a function.
type TestKind string

const (
	NotTest     TestKind = ""
	UnitTest    TestKind = "test"
	Benchmark   TestKind = "benchmark"
	FuzzTest    TestKind = "fuzz"
	ExampleTest TestKind = "example"
)

// Route is an HTTP route registration. A registration whose pattern or
// handler cannot be understood statically is still recorded, with the
// unknown part left empty, so that the gap is visible.
type Route struct {
	Method  string // "GET", "POST", ...; "ANY" when the route accepts every method; empty if Path is
	Path    string // path pattern, possibly preceded by a host: "/payments/{id}"; empty if unknown
	Handler string // Function.ID of the function serving the route; empty if unknown
	Line    int    // line of the registration
}

// Complete reports whether both the pattern and the handler of the route
// are known.
func (r Route) Complete() bool { return r.Path != "" && r.Handler != "" }

// Declaration is the location of one declaration of a function.
type Declaration struct {
	File      string
	StartLine int
	EndLine   int
}

// IsMethod reports whether f has a receiver.
func (f *Function) IsMethod() bool { return f.Receiver != "" }

// QualifiedName returns the name relative to its package: "CreateUser" or
// "Service.Create".
func (f *Function) QualifiedName() string {
	if f.Receiver == "" {
		return f.Name
	}
	return f.Receiver + "." + f.Name
}

// CallKind says what is known about the target of a call.
type CallKind int

const (
	// CallInternal targets a function or method in the scanned repository.
	// Call.Callee is that Function's ID.
	CallInternal CallKind = iota
	// CallExternal targets code outside the repository (standard library or
	// a dependency). Call.Callee is "<import path>.<Name>" or
	// "<import path>.<Type>.<Method>".
	CallExternal
	// CallUnresolved targets something that cannot be determined statically,
	// such as a function value or an interface method. Call.Callee is a
	// best-effort description of the called expression.
	CallUnresolved
)

func (k CallKind) String() string {
	switch k {
	case CallInternal:
		return "internal"
	case CallExternal:
		return "external"
	case CallUnresolved:
		return "unresolved"
	}
	return "unknown"
}

// CallMode says whether a call runs concurrently with its caller.
type CallMode int

const (
	// CallSync is an ordinary call.
	CallSync CallMode = iota
	// CallAsync starts the callee concurrently, as "go f()" does.
	CallAsync
	// CallInAsync is made inside an anonymous function that is started
	// concurrently, as in "go func() { f() }()".
	CallInAsync
)

// UnresolvedReason says why a call could not be resolved.
type UnresolvedReason int

const (
	// UnknownTarget: the type of the receiver, or what the called
	// expression evaluates to, is unknown.
	UnknownTarget UnresolvedReason = iota
	// FunctionValue: the call goes through a variable, parameter or struct
	// field holding a function.
	FunctionValue
	// InterfaceMethod: the call is a method of an interface type, whose
	// implementation is only chosen at run time.
	InterfaceMethod
)

// Call is a single call site inside a function body.
type Call struct {
	Callee string
	Kind   CallKind
	Mode   CallMode
	Reason UnresolvedReason // only meaningful when Kind is CallUnresolved
	Line   int
}

// Functions returns every function and method in the repository, ordered by
// package, file and position.
func (r *Repository) Functions() []*Function {
	var fns []*Function
	for _, p := range r.Packages {
		for _, f := range p.Files {
			fns = append(fns, f.Functions...)
		}
	}
	return fns
}

// FileCount returns the number of files in the repository.
func (r *Repository) FileCount() int {
	n := 0
	for _, p := range r.Packages {
		n += len(p.Files)
	}
	return n
}
