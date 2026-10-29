package hosttest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestEveryTestIsListed fails when a suite test function is missing from
// tests, where it would never run.
func TestEveryTestIsListed(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, tc := range tests {
		if listed[tc.name] {
			t.Errorf("%s is listed twice", tc.name)
		}
		listed[tc.name] = true
	}
	var missing []string
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !isSuiteTest(fn) {
				continue
			}
			if !listed[strings.TrimPrefix(fn.Name.Name, "test")] {
				missing = append(missing, fn.Name.Name)
			}
		}
	}
	slices.Sort(missing)
	if len(missing) > 0 {
		t.Errorf("not in tests (list.go): %s", strings.Join(missing, ", "))
	}
}

// isSuiteTest reports whether fn is func testX(t *testing.T, h Harness).
func isSuiteTest(fn *ast.FuncDecl) bool {
	name := fn.Name.Name
	if !strings.HasPrefix(name, "test") || len(name) == len("test") {
		return false
	}
	params := fn.Type.Params.List
	if len(params) != 2 || len(params[0].Names) != 1 || len(params[1].Names) != 1 {
		return false
	}
	star, ok := params[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "T" {
		return false
	}
	id, ok := params[1].Type.(*ast.Ident)
	return ok && id.Name == "Harness"
}
