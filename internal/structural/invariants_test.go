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

	"github.com/mnbf9rca/agentctl/internal/control"
)

const shellqImportPath = "github.com/mnbf9rca/agentctl/internal/shellq"

type sourceFile struct {
	rel  string
	fset *token.FileSet
	file *ast.File
}

// This syntactic guard catches direct calls through normal, aliased, and dot
// imports. The second named site is the transitional shim compatibility path;
// PR 7 removes the legacy site and restores the one-site invariant.
// Function-value indirection is deliberately outside its boundary: evading
// the repository's own tests is excluded by the same-user threat model.
func TestProductionShellqJoinCallsStayAtTransitionalAuthorizedSites(t *testing.T) {
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
				sites = append(sites, src.rel+":"+enclosingFunctionName(src.file, call.Pos()))
			}
			return true
		})
	}

	sort.Strings(sites)
	want := []string{
		"internal/fleet/fleet.go:agentCommand",
		"internal/fleet/shim.go:shimWindowCommand",
	}
	if strings.Join(sites, "\n") != strings.Join(want, "\n") {
		t.Fatalf("shellq.Join production call sites = %q, want exact transitional sites %q", sites, want)
	}
}

func enclosingFunctionName(file *ast.File, position token.Pos) string {
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Pos() <= position && position < function.End() {
			return function.Name.Name
		}
	}
	return "<package>"
}

// This syntactic guard catches quoted and raw send-keys literals at every
// production scope. Strings assembled from multiple literals are deliberately
// outside its boundary for the same threat-model reason.
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

func TestShimWireRequestHasExactlyFourApprovedFields(t *testing.T) {
	root := repositoryRoot(t)
	var found []string
	for _, src := range parseProductionGo(t, root) {
		if src.rel != "internal/shim/protocol.go" {
			continue
		}
		for _, declaration := range src.file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.TYPE {
				continue
			}
			for _, specification := range general.Specs {
				typeSpec, ok := specification.(*ast.TypeSpec)
				if !ok || typeSpec.Name.Name != "Request" {
					continue
				}
				structure, ok := typeSpec.Type.(*ast.StructType)
				if !ok {
					t.Fatal("shim.Request is not a struct")
				}
				for _, field := range structure.Fields.List {
					if len(field.Names) != 1 {
						t.Fatalf("shim.Request field declaration has %d names, want exactly one", len(field.Names))
					}
					found = append(found, field.Names[0].Name)
				}
			}
		}
	}
	want := []string{"Version", "Session", "Role", "Operation"}
	if strings.Join(found, ",") != strings.Join(want, ",") {
		t.Fatalf("shim.Request fields = %#v, want exact argument-free wire schema %#v", found, want)
	}
}

func TestControlRegistryLifecycleOperationsCarryNoPayload(t *testing.T) {
	for _, command := range control.Operations() {
		if command.Kind == control.OperationControl && command.Payload != "" {
			t.Fatalf("control operation %q carries payload %q; lifecycle operations must be structurally payload-free", command.Operation, command.Payload)
		}
	}
}

func TestShimCompatibilityAPIsExposeNoPayloadParameter(t *testing.T) {
	root := repositoryRoot(t)
	wantParameters := map[string][]string{
		"internal/control/shim_dispatcher.go:Execute":     {"ctx", "operation", "session", "role"},
		"internal/shim/client.go:DeliverOperationGuarded": {"ctx", "session", "role", "operation", "guard"},
	}
	found := make(map[string][]string)
	for _, src := range parseProductionGo(t, root) {
		for _, declaration := range src.file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Type.Params == nil {
				continue
			}
			key := src.rel + ":" + function.Name.Name
			if _, tracked := wantParameters[key]; !tracked {
				continue
			}
			for _, field := range function.Type.Params.List {
				for _, name := range field.Names {
					found[key] = append(found[key], name.Name)
				}
			}
		}
	}
	for key, want := range wantParameters {
		if strings.Join(found[key], ",") != strings.Join(want, ",") {
			t.Fatalf("%s parameters = %#v, want exact operation-name-only boundary %#v", key, found[key], want)
		}
	}
}

func TestLegacyTargetAndPayloadDeliveryStayAtTransitionalInventory(t *testing.T) {
	root := repositoryRoot(t)
	var targetImports []string
	var deliveryCalls []string
	var deliveryDeclarations []string
	for _, src := range parseProductionGo(t, root) {
		for _, specification := range src.file.Imports {
			path, err := strconv.Unquote(specification.Path.Value)
			if err == nil && path == "github.com/mnbf9rca/agentctl/internal/target" {
				targetImports = append(targetImports, src.rel)
			}
		}
		ast.Inspect(src.file, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.FuncDecl:
				if value.Name.Name == "DeliverPayload" {
					deliveryDeclarations = append(deliveryDeclarations, src.rel+":"+value.Name.Name)
				}
			case *ast.CallExpr:
				selector, ok := value.Fun.(*ast.SelectorExpr)
				if ok && selector.Sel.Name == "DeliverPayload" {
					deliveryCalls = append(deliveryCalls, src.rel+":"+enclosingFunctionName(src.file, value.Pos()))
				}
			}
			return true
		})
	}
	sort.Strings(targetImports)
	sort.Strings(deliveryCalls)
	sort.Strings(deliveryDeclarations)
	if got, want := strings.Join(targetImports, ","), "cmd/agentctl/main.go"; got != want {
		t.Fatalf("legacy internal/target imports = %q, want transitional inventory %q", got, want)
	}
	if got, want := strings.Join(deliveryCalls, ","), "internal/control/dispatcher.go:Execute"; got != want {
		t.Fatalf("legacy DeliverPayload calls = %q, want transitional inventory %q", got, want)
	}
	if got, want := strings.Join(deliveryDeclarations, ","), "internal/tmuxx/control.go:DeliverPayload"; got != want {
		t.Fatalf("legacy DeliverPayload declarations = %q, want transitional inventory %q", got, want)
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
