package coord

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

func TestA95AuthenticatedIngressHasSingleCompileTimeGate(t *testing.T) {
	root := "."
	fset := token.NewFileSet()
	constName := "AuthenticatedAgentIngressEnabled"
	declarations := 0
	var coordinatorFile, mainFile *ast.File

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root && (entry.Name() == ".git" || entry.Name() == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		if filepath.Clean(path) == "coordinator.go" {
			coordinatorFile = file
		}
		if filepath.Clean(path) == filepath.Join("cmd", "coordd", "main.go") {
			mainFile = file
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.ValueSpec:
				for i, name := range n.Names {
					if name.Name != constName {
						continue
					}
					value, literalFalse := ast.Expr(nil), false
					if len(n.Values) == 1 {
						value = n.Values[0]
						id, ok := value.(*ast.Ident)
						literalFalse = ok && id.Name == "false"
					}
					if file != coordinatorFile || n.Type != nil || !literalFalse {
						t.Errorf("%s has a non-literal or non-canonical declaration in %s", constName, path)
						continue
					}
					decl, ok := enclosingGenDecl(file, n)
					if !ok || decl.Tok != token.CONST || i != 0 {
						t.Errorf("%s must be declared as a const false in coordinator.go", constName)
						continue
					}
					declarations++
				}
			case *ast.AssignStmt:
				for _, lhs := range n.Lhs {
					if id, ok := lhs.(*ast.Ident); ok && id.Name == constName {
						t.Errorf("production assignment/shadow of %s in %s", constName, path)
					}
				}
			case *ast.FuncDecl:
				if n.Recv != nil {
					checkShadowNames(t, n.Recv, constName, path)
				}
				if n.Type.Params != nil {
					checkShadowNames(t, n.Type.Params, constName, path)
				}
				if n.Type.Results != nil {
					checkShadowNames(t, n.Type.Results, constName, path)
				}
			case *ast.FuncLit:
				if n.Type.Params != nil {
					checkShadowNames(t, n.Type.Params, constName, path)
				}
				if n.Type.Results != nil {
					checkShadowNames(t, n.Type.Results, constName, path)
				}
			case *ast.RangeStmt:
				for _, expr := range []ast.Expr{n.Key, n.Value} {
					if id, ok := expr.(*ast.Ident); ok && id.Name == constName {
						t.Errorf("production range shadow of %s in %s", constName, path)
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if declarations != 1 || coordinatorFile == nil || mainFile == nil {
		t.Fatalf("compile-time gate declarations=%d coordinator.go=%v cmd/coordd/main.go=%v", declarations, coordinatorFile != nil, mainFile != nil)
	}

	configure := functionDecl(mainFile, "configureAuthenticatedAgentIngress")
	if configure == nil || len(configure.Body.List) == 0 {
		t.Fatal("configureAuthenticatedAgentIngress missing")
	}
	guard, ok := configure.Body.List[0].(*ast.IfStmt)
	if !ok || !isNegatedGate(guard.Cond) || len(guard.Body.List) != 1 || !isReturnNil(guard.Body.List[0]) {
		t.Fatal("first ingress configuration statement must return nil when the literal-false gate is disabled")
	}
	if astUsesForbiddenFlag(guard.Cond) {
		t.Fatal("ingress configuration gate reads an environment, flag or config switch")
	}
	if astUsesForbiddenFlag(configure.Body) {
		t.Fatal("configureAuthenticatedAgentIngress reads an environment variable or flag")
	}

	run := functionDecl(coordinatorFile, "RunWithReadiness")
	if run == nil {
		t.Fatal("RunWithReadiness missing")
	}
	var selection *ast.IfStmt
	count := 0
	ast.Inspect(run.Body, func(node ast.Node) bool {
		if stmt, ok := node.(*ast.IfStmt); ok && isPositiveGate(stmt.Cond) {
			selection = stmt
			count++
		}
		return true
	})
	if count != 1 || selection == nil || astUsesForbiddenFlag(selection.Cond) {
		t.Fatalf("RunWithReadiness stream selection must depend only on %s (matching if count=%d)", constName, count)
	}
	if astUsesForbiddenFlag(run.Body) {
		t.Fatal("RunWithReadiness reads an environment variable or flag")
	}
	if receiveCalls(selection.Body, "ReceiveAuthenticated") != 1 || receiveCalls(selection.Body, "Receive") != 0 || selection.Else == nil || receiveCalls(selection.Else, "Receive") != 1 || receiveCalls(selection.Else, "ReceiveAuthenticated") != 0 {
		t.Fatal("RunWithReadiness must use authenticated Receive only in the gate's true branch and legacy Receive only in its false branch")
	}
	if receiveCalls(run.Body, "ReceiveAuthenticated") != 1 || receiveCalls(run.Body, "Receive") != 1 {
		t.Fatal("RunWithReadiness contains an unguarded or duplicate receive stream selection")
	}
}

func enclosingGenDecl(file *ast.File, spec *ast.ValueSpec) (*ast.GenDecl, bool) {
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, item := range gen.Specs {
			if item == spec {
				return gen, true
			}
		}
	}
	return nil, false
}

func checkShadowNames(t *testing.T, fields *ast.FieldList, name, path string) {
	t.Helper()
	for _, field := range fields.List {
		for _, ident := range field.Names {
			if ident.Name == name {
				t.Errorf("production parameter/receiver shadow of %s in %s", name, path)
			}
		}
	}
}

func functionDecl(file *ast.File, name string) *ast.FuncDecl {
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

func isNegatedGate(expr ast.Expr) bool {
	unary, ok := expr.(*ast.UnaryExpr)
	return ok && unary.Op == token.NOT && isPositiveGate(unary.X)
}

func isPositiveGate(expr ast.Expr) bool {
	switch gate := expr.(type) {
	case *ast.Ident:
		return gate.Name == "AuthenticatedAgentIngressEnabled"
	case *ast.SelectorExpr:
		pkg, ok := gate.X.(*ast.Ident)
		return ok && pkg.Name == "coord" && gate.Sel.Name == "AuthenticatedAgentIngressEnabled"
	default:
		return false
	}
}

func isReturnNil(stmt ast.Stmt) bool {
	ret, ok := stmt.(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return false
	}
	id, ok := ret.Results[0].(*ast.Ident)
	return ok && id.Name == "nil"
}

func astUsesForbiddenFlag(node ast.Node) bool {
	found := false
	ast.Inspect(node, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if pkg, ok := selector.X.(*ast.Ident); ok && (pkg.Name == "os" && (selector.Sel.Name == "Getenv" || selector.Sel.Name == "LookupEnv") || pkg.Name == "flag") {
			found = true
		}
		return true
	})
	return found
}

func receiveCalls(node ast.Node, method string) int {
	count := 0
	ast.Inspect(node, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if ok && selector.Sel.Name == method {
			count++
		}
		return true
	})
	return count
}
