// Package coldwirevet reports coldwire misuse the type system cannot catch:
//
//   - Get or Try on a Deferred provider in code that runs while a graph is
//     constructed (functions returning a graph, package-level var
//     initializers, init functions, Eagerly/TryEagerly builders), including
//     through calls to functions that do so, across packages;
//   - Get or Try inside a builder, where From would propagate failures;
//   - a builder's scope used inside a function literal that outlives the
//     builder, or stored (assigned, returned, put in a value or a channel):
//     the scope is invalid once the builder returns;
//   - Get or Try, or a call to a function doing so, in a builder or in a
//     goroutine it starts: waiting outside From is invisible to cycle
//     detection and can deadlock;
//   - context.Background or context.TODO in a builder, instead of the
//     scope's Context, which Close cancels;
//   - From(nil), which bypasses the scope check.
package coldwirevet

import (
	"fmt"
	"go/ast"
	"go/types"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/types/typeutil"
)

const pkgPath = "github.com/bailleul-dev/coldwire"

var Analyzer = &analysis.Analyzer{
	Name:      "coldwirevet",
	Doc:       "reports Deferred coldwire providers forced during eager initialization",
	URL:       "https://github.com/bailleul-dev/coldwire",
	Run:       run,
	FactTypes: []analysis.Fact{new(forcesDeferred)},
}

// forcesDeferred marks a function that, when called, resolves a Deferred
// provider (directly or through its callees).
type forcesDeferred struct{ Via string }

func (*forcesDeferred) AFact()           {}
func (f *forcesDeferred) String() string { return "forcesDeferred(" + f.Via + ")" }

// site is something that happens when a function or initializer runs.
type site struct {
	call   *ast.CallExpr
	forced string      // non-empty: Get/Try on this Deferred provider
	callee *types.Func // non-nil: a static call to this function
}

func run(pass *analysis.Pass) (any, error) {
	// 1. Per function declaration, the sites executed when it is called.
	funcSites := map[*types.Func][]site{}
	for _, f := range pass.Files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			if fn, ok := pass.TypesInfo.Defs[fd.Name].(*types.Func); ok {
				funcSites[fn] = immediateSites(pass, fd.Body, true)
			}
		}
	}

	// 2. Fixpoint: which functions of this package force a Deferred provider.
	forces := map[*types.Func]string{}
	for changed := true; changed; {
		changed = false
		for fn, sites := range funcSites {
			if _, done := forces[fn]; done {
				continue
			}
			if via := firstForce(pass, sites, forces); via != "" {
				forces[fn] = via
				changed = true
			}
		}
	}
	for fn, via := range forces {
		pass.ExportObjectFact(fn, &forcesDeferred{Via: via})
	}

	// 3. Report in eager contexts.
	for _, f := range pass.Files {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok {
						for _, v := range vs.Values {
							reportEager(pass, immediateSites(pass, v, false), forces, "package initialization")
						}
					}
				}
			case *ast.FuncDecl:
				switch {
				case d.Body == nil:
				case d.Recv == nil && d.Name.Name == "init":
					reportEager(pass, immediateSites(pass, d.Body, false), forces, "init")
				case returnsGraph(pass, d):
					reportEager(pass, immediateSites(pass, d.Body, false), forces, "graph construction in "+d.Name.Name)
				}
			}
		}
		// Eager builders anywhere, and misuse inside any builder.
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if lit, name := builderArg(pass, call); lit != nil {
				if name == "Eagerly" || name == "TryEagerly" {
					reportEager(pass, immediateSites(pass, lit.Body, false), forces, name+" builder")
				}
				reportScopeEscape(pass, lit, name)
				reportScopeStored(pass, lit, name)
				eager := name == "Eagerly" || name == "TryEagerly"
				for _, s := range immediateSites(pass, lit.Body, false) {
					switch {
					case eager && (s.forced != "" || s.callee != nil):
						// Already reported above as forcing a Deferred provider.
					case s.callee == nil:
						pass.Reportf(s.call.Pos(), "%s inside a %s builder: use From(s) so failures propagate and cycles are detected", methodName(s.call), name)
					default:
						if via := forcesVia(pass, s.callee, forces); via != "" {
							pass.Reportf(s.call.Pos(), "call to %s resolves a provider (%s) with Get inside a %s builder: pass the value in, read with From(s)", s.callee.Name(), via, name)
						}
					}
				}
				for _, c := range backgroundContexts(pass, lit.Body) {
					pass.Reportf(c.Pos(), "%s in a %s builder: use %s.Context(), which Close cancels", types.ExprString(c.Fun), name, scopeName(lit))
				}
			}
			if isProviderMethod(pass, call, "From") && len(call.Args) == 1 {
				if tv, ok := pass.TypesInfo.Types[call.Args[0]]; ok && tv.IsNil() {
					pass.Reportf(call.Pos(), "From(nil) bypasses the scope check; pass the builder's scope")
				}
			}
			return true
		})
	}
	return nil, nil
}

// reportScopeEscape reports uses of the builder's scope parameter inside a
// nested function literal that is not invoked on the spot: such a literal
// runs after the builder returned, when the scope is no longer valid.
func reportScopeEscape(pass *analysis.Pass, lit *ast.FuncLit, builder string) {
	params := lit.Type.Params.List
	if len(params) == 0 || len(params[0].Names) == 0 {
		return
	}
	scope := pass.TypesInfo.Defs[params[0].Names[0]]
	if scope == nil {
		return
	}
	invoked := map[*ast.FuncLit]bool{}
	var visit func(n ast.Node, nested bool) bool
	visit = func(n ast.Node, nested bool) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			if f, ok := ast.Unparen(n.Fun).(*ast.FuncLit); ok {
				invoked[f] = true
			}
			// wg.Go(func() { … }) and errgroup's g.Go: parallel reads the
			// builder presumably joins before returning. The run-time check
			// catches it if it does not.
			if sel, ok := ast.Unparen(n.Fun).(*ast.SelectorExpr); ok && sel.Sel.Name == "Go" {
				for _, arg := range n.Args {
					if f, ok := ast.Unparen(arg).(*ast.FuncLit); ok {
						invoked[f] = true
					}
				}
			}
		case *ast.SelectorExpr:
			// s.Context() stays valid after the builder returns.
			if id, ok := n.X.(*ast.Ident); ok && n.Sel.Name == "Context" && pass.TypesInfo.Uses[id] == scope {
				return false
			}
		case *ast.FuncLit:
			if n != lit && !invoked[n] && !nested {
				ast.Inspect(n.Body, func(m ast.Node) bool { return visit(m, true) })
				return false
			}
		case *ast.Ident:
			if nested && pass.TypesInfo.Uses[n] == scope {
				pass.Reportf(n.Pos(), "scope %s of the %s builder is used in a function that runs after the builder returned; read dependencies before returning", n.Name, builder)
			}
		}
		return true
	}
	ast.Inspect(lit.Body, func(n ast.Node) bool { return visit(n, false) })
}

// reportScopeStored reports the builder's scope being stored where it can
// outlive the builder: assigned, returned, put in a composite value, sent on a
// channel, or appended. Passing it to a function call is allowed.
func reportScopeStored(pass *analysis.Pass, lit *ast.FuncLit, builder string) {
	scope := scopeParam(pass, lit)
	if scope == nil {
		return
	}
	is := func(e ast.Expr) bool {
		id, ok := ast.Unparen(e).(*ast.Ident)
		return ok && pass.TypesInfo.Uses[id] == scope
	}
	report := func(e ast.Expr) {
		pass.Reportf(e.Pos(), "scope %s of the %s builder is stored: it is only valid while the builder runs", scope.Name(), builder)
	}
	ast.Inspect(lit.Body, func(n ast.Node) bool {
		var exprs []ast.Expr
		switch n := n.(type) {
		case *ast.AssignStmt:
			exprs = n.Rhs
		case *ast.ValueSpec:
			exprs = n.Values
		case *ast.ReturnStmt:
			exprs = n.Results
		case *ast.SendStmt:
			exprs = []ast.Expr{n.Value}
		case *ast.KeyValueExpr:
			exprs = []ast.Expr{n.Value}
		case *ast.CompositeLit:
			exprs = n.Elts
		case *ast.CallExpr:
			if id, ok := ast.Unparen(n.Fun).(*ast.Ident); ok && id.Name == "append" {
				if _, builtin := pass.TypesInfo.Uses[id].(*types.Builtin); builtin && len(n.Args) > 1 {
					exprs = n.Args[1:]
				}
			}
		}
		for _, e := range exprs {
			if is(e) {
				report(e)
			}
		}
		return true
	})
}

// backgroundContexts returns the calls to context.Background and
// context.TODO that run while the builder runs.
func backgroundContexts(pass *analysis.Pass, body *ast.BlockStmt) []*ast.CallExpr {
	var out []*ast.CallExpr
	var visit func(n ast.Node) bool
	visit = func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return false // runs later, or is a builder checked on its own
		case *ast.CallExpr:
			if lit, ok := ast.Unparen(n.Fun).(*ast.FuncLit); ok {
				ast.Inspect(lit.Body, visit)
			}
			if fn, ok := typeutil.Callee(pass.TypesInfo, n).(*types.Func); ok && fn.Pkg() != nil && fn.Pkg().Path() == "context" && (fn.Name() == "Background" || fn.Name() == "TODO") {
				out = append(out, n)
			}
		}
		return true
	}
	ast.Inspect(body, visit)
	return out
}

func scopeParam(pass *analysis.Pass, lit *ast.FuncLit) types.Object {
	params := lit.Type.Params.List
	if len(params) == 0 || len(params[0].Names) == 0 {
		return nil
	}
	return pass.TypesInfo.Defs[params[0].Names[0]]
}

func scopeName(lit *ast.FuncLit) string {
	if params := lit.Type.Params.List; len(params) > 0 && len(params[0].Names) > 0 && params[0].Names[0].Name != "_" {
		return params[0].Names[0].Name
	}
	return "s"
}

func reportEager(pass *analysis.Pass, sites []site, forces map[*types.Func]string, ctx string) {
	for _, s := range sites {
		switch {
		case s.forced != "":
			pass.Reportf(s.call.Pos(), "%s forces Deferred provider %s during %s", methodName(s.call), s.forced, ctx)
		case s.callee != nil:
			if via := forcesVia(pass, s.callee, forces); via != "" {
				pass.Reportf(s.call.Pos(), "call to %s forces a Deferred provider (%s) during %s", s.callee.Name(), via, ctx)
			}
		}
	}
}

func firstForce(pass *analysis.Pass, sites []site, forces map[*types.Func]string) string {
	for _, s := range sites {
		if s.forced != "" {
			return s.forced
		}
		if s.callee != nil {
			if via := forcesVia(pass, s.callee, forces); via != "" {
				return s.callee.Name() + " → " + via
			}
		}
	}
	return ""
}

func forcesVia(pass *analysis.Pass, fn *types.Func, local map[*types.Func]string) string {
	if fn = fn.Origin(); fn.Pkg() == pass.Pkg {
		return local[fn]
	}
	var fact forcesDeferred
	if pass.ImportObjectFact(fn, &fact) {
		return fact.Via
	}
	return ""
}

// immediateSites lists the calls that execute when root is evaluated. Bodies
// of function literals are skipped (they run later), except literals invoked
// on the spot and, if intoEager, literals passed to Eagerly/TryEagerly. The
// reporting passes leave eager builders out since each is reported on its own.
func immediateSites(pass *analysis.Pass, root ast.Node, intoEager bool) []site {
	var out []site
	var visit func(n ast.Node) bool
	visit = func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.CallExpr:
			if lit, ok := ast.Unparen(n.Fun).(*ast.FuncLit); ok {
				ast.Inspect(lit.Body, visit)
			}
			if lit, name := builderArg(pass, n); intoEager && lit != nil && (name == "Eagerly" || name == "TryEagerly") {
				ast.Inspect(lit.Body, visit)
			}
			// wg.Go(func() { … }) and errgroup's g.Go run the literal now,
			// concurrently.
			if sel, ok := ast.Unparen(n.Fun).(*ast.SelectorExpr); ok && sel.Sel.Name == "Go" {
				for _, arg := range n.Args {
					if lit, ok := ast.Unparen(arg).(*ast.FuncLit); ok {
						ast.Inspect(lit.Body, visit)
					}
				}
			}
			switch {
			case isProviderMethod(pass, n, "Get", "Try", "TryContext", "Acquire"):
				s := site{call: n}
				if isDeferredProvider(pass, n) {
					s.forced = types.ExprString(n.Fun.(*ast.SelectorExpr).X)
				}
				out = append(out, s)
			default:
				if fn := typeutil.StaticCallee(pass.TypesInfo, n); fn != nil {
					out = append(out, site{call: n, callee: fn})
				}
			}
		}
		return true
	}
	ast.Inspect(root, visit)
	return out
}

// returnsGraph reports whether fd is a graph constructor: it returns a
// coldwire.Graph, or a struct (or pointer to one) embedding it. Everything
// such a function runs directly runs while the graph is constructed.
func returnsGraph(pass *analysis.Pass, fd *ast.FuncDecl) bool {
	fn, ok := pass.TypesInfo.Defs[fd.Name].(*types.Func)
	if !ok {
		return false
	}
	results := fn.Signature().Results()
	for i := range results.Len() {
		if isGraph(results.At(i).Type()) {
			return true
		}
	}
	return false
}

func isGraph(t types.Type) bool {
	if p, ok := types.Unalias(t).(*types.Pointer); ok {
		t = p.Elem()
	}
	named, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return false
	}
	if obj := named.Obj(); obj.Pkg() != nil && obj.Pkg().Path() == pkgPath && obj.Name() == "Graph" {
		return true
	}
	st, ok := named.Underlying().(*types.Struct)
	if !ok {
		return false
	}
	for i := range st.NumFields() {
		if f := st.Field(i); f.Embedded() && isGraph(f.Type()) {
			return true
		}
	}
	return false
}

// builderArg returns the function literal passed to a coldwire builder.
func builderArg(pass *analysis.Pass, call *ast.CallExpr) (*ast.FuncLit, string) {
	fn, ok := typeutil.Callee(pass.TypesInfo, call).(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != pkgPath || len(call.Args) != 1 {
		return nil, ""
	}
	switch fn.Name() {
	case "Eagerly", "TryEagerly", "Lazily", "TryLazily":
		lit, _ := ast.Unparen(call.Args[0]).(*ast.FuncLit)
		return lit, fn.Name()
	}
	return nil, ""
}

func providerType(pass *analysis.Pass, call *ast.CallExpr) *types.Named {
	sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	if !ok {
		return nil
	}
	s, ok := pass.TypesInfo.Selections[sel]
	if !ok {
		return nil
	}
	t := s.Recv()
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	named, ok := types.Unalias(t).(*types.Named)
	if !ok || named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != pkgPath || named.Obj().Name() != "Provider" {
		return nil
	}
	return named
}

func isProviderMethod(pass *analysis.Pass, call *ast.CallExpr, names ...string) bool {
	if providerType(pass, call) == nil {
		return false
	}
	m := methodName(call)
	for _, n := range names {
		if m == n {
			return true
		}
	}
	return false
}

func isDeferredProvider(pass *analysis.Pass, call *ast.CallExpr) bool {
	named := providerType(pass, call)
	if named == nil || named.TypeArgs().Len() != 2 {
		return false
	}
	k, ok := types.Unalias(named.TypeArgs().At(1)).(*types.Named)
	return ok && k.Obj().Name() == "Deferred" && k.Obj().Pkg() != nil && k.Obj().Pkg().Path() == pkgPath
}

func methodName(call *ast.CallExpr) string {
	if sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr); ok {
		return sel.Sel.Name
	}
	return fmt.Sprint(call.Fun)
}
