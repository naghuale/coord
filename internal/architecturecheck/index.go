package architecturecheck

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// CodeIndex holds declared Go type, function, method and test names under root.
type CodeIndex struct {
	Types     map[string]bool
	Funcs     map[string]bool
	Methods   map[string]bool
	Tests     map[string]bool
	AllNames  map[string]bool
}

// BuildCodeIndex walks root for .go files and indexes declarations via go/ast.
// It skips .git and .scratch. Parse errors on individual files are ignored so
// generated or incomplete fixtures still index the rest of the tree.
func BuildCodeIndex(root string) (*CodeIndex, error) {
	idx := &CodeIndex{
		Types:    map[string]bool{},
		Funcs:    map[string]bool{},
		Methods:  map[string]bool{},
		Tests:    map[string]bool{},
		AllNames: map[string]bool{},
	}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if name == ".git" || name == ".scratch" || name == "vendor" || name == "testdata" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if err != nil {
			return nil
		}
		isTest := strings.HasSuffix(name, "_test.go")
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				if d.Tok != token.TYPE {
					continue
				}
				for _, spec := range d.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok || ts.Name == nil {
						continue
					}
					idx.Types[ts.Name.Name] = true
					idx.AllNames[ts.Name.Name] = true
				}
			case *ast.FuncDecl:
				if d.Name == nil {
					continue
				}
				name := d.Name.Name
				if d.Recv != nil && len(d.Recv.List) > 0 {
					idx.Methods[name] = true
					idx.AllNames[name] = true
					continue
				}
				idx.Funcs[name] = true
				idx.AllNames[name] = true
				if isTest && strings.HasPrefix(name, "Test") {
					idx.Tests[name] = true
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return idx, nil
}

// HasSymbol reports whether name is a declared type, function or method.
func (idx *CodeIndex) HasSymbol(name string) bool {
	if idx == nil {
		return false
	}
	return idx.AllNames[name]
}

// HasTest reports whether name is a declared Test* function in a _test.go file.
func (idx *CodeIndex) HasTest(name string) bool {
	if idx == nil {
		return false
	}
	return idx.Tests[name]
}
