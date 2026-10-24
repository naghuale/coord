package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

func TestCoordinatorDelegationHasCompileTimeGate(t *testing.T) {
	root := "."
	fset := token.NewFileSet()
	constName := "CoordinatorDelegationEnabled"
	declarations := 0
	gateHelperFiles := map[string]bool{}

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
		// Identify gate-helper files: they declare CoordinatorDelegationEnabled
		// or coordinatorDelegationActive / delegationHarnessEnabled.
		base := filepath.Base(path)
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Name != nil && d.Name.Name == "coordinatorDelegationActive" {
					gateHelperFiles[base] = true
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for _, name := range vs.Names {
						if name.Name == constName || name.Name == "delegationHarnessEnabled" {
							gateHelperFiles[base] = true
						}
					}
				}
			}
		}

		ast.Inspect(file, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.ValueSpec:
				for _, name := range n.Names {
					if name.Name != constName {
						continue
					}
					literalFalse := false
					if len(n.Values) == 1 {
						if id, ok := n.Values[0].(*ast.Ident); ok && id.Name == "false" {
							literalFalse = true
						}
					}
					if n.Type != nil || !literalFalse {
						t.Errorf("%s non-literal declaration in %s", constName, path)
						continue
					}
					declarations++
				}
			case *ast.AssignStmt:
				for _, lhs := range n.Lhs {
					if id, ok := lhs.(*ast.Ident); ok && id.Name == constName {
						t.Errorf("production assignment of %s in %s", constName, path)
					}
				}
			case *ast.CallExpr:
				if !gateHelperFiles[base] {
					return true
				}
				if sel, ok := n.Fun.(*ast.SelectorExpr); ok {
					if pkg, ok := sel.X.(*ast.Ident); ok && (pkg.Name == "os" || pkg.Name == "flag") {
						// F6: gate helpers must not read env/flag/config.
						t.Errorf("gate helper %s must not call %s.%s", path, pkg.Name, sel.Sel.Name)
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
	if declarations != 1 {
		t.Fatalf("CoordinatorDelegationEnabled declarations=%d want 1", declarations)
	}
	if CoordinatorDelegationEnabled {
		t.Fatal("const must be false")
	}
	if !gateHelperFiles["delegation_gate.go"] {
		t.Fatal("expected delegation_gate.go to be identified as a gate helper file")
	}
}
