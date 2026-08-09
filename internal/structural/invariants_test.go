package structural_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const shellqImportPath = "github.com/mnbf9rca/agentctl/internal/shellq"

type sourceFile struct {
	rel  string
	fset *token.FileSet
	file *ast.File
}

func TestExactlyOneProductionShellqJoinCall(t *testing.T) {
	root := repositoryRoot(t)
	var sites []string

	for _, src := range parseProductionGo(t, root) {
		aliases := make(map[string]bool)
		dotImport := false
		for _, spec := range src.file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil || importPath != shellqImportPath {
				continue
			}
			if spec.Name == nil {
				aliases["shellq"] = true
				continue
			}
			switch spec.Name.Name {
			case ".":
				dotImport = true
			case "_":
			default:
				aliases[spec.Name.Name] = true
			}
		}

		ast.Inspect(src.file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			matched := false
			switch fun := call.Fun.(type) {
			case *ast.SelectorExpr:
				ident, ok := fun.X.(*ast.Ident)
				matched = ok && aliases[ident.Name] && fun.Sel.Name == "Join"
			case *ast.Ident:
				matched = dotImport && fun.Name == "Join"
			}
			if matched {
				sites = append(sites, sourceSite(src, call.Pos()))
			}
			return true
		})
	}

	sort.Strings(sites)
	if len(sites) != 1 {
		t.Fatalf("shellq.Join production call sites = %d, want 1; found: %s", len(sites), strings.Join(sites, ", "))
	}
}

func TestProductionSendKeysLiteralsAreInsideDeliverPayload(t *testing.T) {
	root := repositoryRoot(t)
	var violations []string

	for _, src := range parseProductionGo(t, root) {
		var sanctioned []*ast.FuncDecl
		if filepath.ToSlash(filepath.Dir(src.rel)) == "internal/tmuxx" {
			for _, decl := range src.file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if ok && fn.Name.Name == "DeliverPayload" && fn.Body != nil {
					sanctioned = append(sanctioned, fn)
				}
			}
		}

		ast.Inspect(src.file, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil || value != "send-keys" {
				return true
			}
			for _, fn := range sanctioned {
				if fn.Body.Pos() <= literal.Pos() && literal.End() <= fn.Body.End() {
					return true
				}
			}
			violations = append(violations, sourceSite(src, literal.Pos()))
			return true
		})
	}

	sort.Strings(violations)
	if len(violations) != 0 {
		t.Fatalf("production send-keys literals outside internal/tmuxx.DeliverPayload: %s", strings.Join(violations, ", "))
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate structural test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}

func parseProductionGo(t *testing.T, root string) []sourceFile {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".worktrees", "dist", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) == ".go" && !strings.HasSuffix(path, "_test.go") {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk production Go source: %v", err)
	}
	sort.Strings(paths)

	sources := make([]sourceFile, 0, len(paths))
	for _, path := range paths {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatalf("make %s relative to repository root: %v", path, err)
		}
		sources = append(sources, sourceFile{rel: rel, fset: fset, file: file})
	}
	return sources
}

func sourceSite(src sourceFile, pos token.Pos) string {
	position := src.fset.Position(pos)
	return filepath.ToSlash(src.rel) + ":" + strconv.Itoa(position.Line)
}
