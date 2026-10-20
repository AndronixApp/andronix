package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Every error the installer explains has a fixed class (telemetry, the
// --progress json error event, the app's automatic problem reports group
// by it): titles change, classes don't. Every class and kind is listed in
// docs/progress-json.md, where keys are only ever added.
var classRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

type errSite struct {
	pos   string
	class string // "" when it has none
}

// errorSites finds every ui.Errorf call and ui.UserError literal in dir.
func errorSites(t *testing.T, dir string) (sites []errSite, kinds []string) {
	t.Helper()
	fset := token.NewFileSet()
	files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(af, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				if se, ok := x.Fun.(*ast.SelectorExpr); ok && se.Sel.Name == "Errorf" && isIdent(se.X, "ui") {
					sites = append(sites, errSite{fset.Position(x.Pos()).String(), strLit(x.Args[0])})
				}
			case *ast.AssignStmt: // e.Class = "..." after the fact
				if se, ok := x.Lhs[0].(*ast.SelectorExpr); ok && se.Sel.Name == "Class" && len(x.Rhs) == 1 {
					sites = append(sites, errSite{fset.Position(x.Pos()).String(), strLit(x.Rhs[0])})
				}
			case *ast.CompositeLit:
				if se, ok := x.Type.(*ast.SelectorExpr); ok && se.Sel.Name == "UserError" && isIdent(se.X, "ui") {
					s := errSite{pos: fset.Position(x.Pos()).String()}
					for _, el := range x.Elts {
						kv := el.(*ast.KeyValueExpr)
						switch kv.Key.(*ast.Ident).Name {
						case "Class":
							s.class = strLit(kv.Value)
						case "Kind":
							if k := strLit(kv.Value); k != "" {
								kinds = append(kinds, k)
							}
						}
					}
					sites = append(sites, s)
				}
			}
			return true
		})
	}
	return sites, kinds
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

// strLit is a string literal's value, "" for anything else.
func strLit(e ast.Expr) string {
	if bl, ok := e.(*ast.BasicLit); ok && bl.Kind == token.STRING {
		s, _ := strconv.Unquote(bl.Value)
		return s
	}
	return ""
}

// constStrings is every string constant (or returned literal) of the given
// type or function in file.
func constStrings(t *testing.T, file, typ, fn string) []string {
	t.Helper()
	af, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	ast.Inspect(af, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.ValueSpec:
			if typ != "" && x.Type != nil && isIdent(x.Type, typ) {
				for _, v := range x.Values {
					out = append(out, strLit(v))
				}
			}
		case *ast.FuncDecl:
			if fn != "" && x.Name.Name == fn {
				ast.Inspect(x, func(n ast.Node) bool {
					if r, ok := n.(*ast.ReturnStmt); ok && len(r.Results) == 1 {
						if s := strLit(r.Results[0]); s != "" {
							out = append(out, s)
						}
					}
					return true
				})
			}
		}
		return true
	})
	return out
}

func TestEveryErrorHasAClass(t *testing.T) {
	doc, err := os.ReadFile("../../docs/progress-json.md")
	if err != nil {
		t.Fatal(err)
	}
	documented := func(key string) bool { return strings.Contains(string(doc), "`"+key+"`") }

	var sites []errSite
	var kinds []string
	for _, dir := range []string{".", "../../cmd/andronix"} {
		s, k := errorSites(t, dir)
		sites, kinds = append(sites, s...), append(kinds, k...)
	}
	if len(sites) < 150 {
		t.Fatalf("found only %d error sites: the scan is broken", len(sites))
	}
	for _, s := range sites {
		switch {
		case s.class == "":
			t.Errorf("%s: no class (give ui.Errorf a fixed class, or the UserError a Class)", s.pos)
		case !classRe.MatchString(s.class):
			t.Errorf("%s: class %q isn't a lower_case token", s.pos, s.class)
		case !documented(s.class):
			t.Errorf("%s: class %q isn't in docs/progress-json.md", s.pos, s.class)
		}
	}

	kinds = append(kinds, constStrings(t, "../pkgmgr/diag.go", "Kind", "")...)
	kinds = append(kinds, constStrings(t, "install.go", "", "downloadKind")...)
	for _, k := range kinds {
		if !documented(k) {
			t.Errorf("kind %q isn't in docs/progress-json.md", k)
		}
	}
	if !documented("http_<code>") {
		t.Errorf("the download kind http_<code> isn't in docs/progress-json.md")
	}
}
