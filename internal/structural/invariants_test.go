package structural_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/control"
	"github.com/mnbf9rca/agentctl/internal/shim"
)

const shellqImportPath = "github.com/mnbf9rca/agentctl/internal/shellq"

type sourceFile struct {
	rel  string
	fset *token.FileSet
	file *ast.File
}

// This syntactic guard catches direct calls through normal, aliased, and dot
// imports. The shim window command is the sole production shell composition.
// Function-value indirection is deliberately outside its boundary: evading
// the repository's own tests is excluded by the same-user threat model.
func TestProductionShellqJoinCallStaysAtShimWindowCommand(t *testing.T) {
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
	want := []string{"internal/fleet/shim.go:shimWindowCommand"}
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
func TestProductionContainsNoTmuxSendKeysLiteral(t *testing.T) {
	root := repositoryRoot(t)
	var violations []string

	for _, src := range parseProductionGo(t, root) {
		ast.Inspect(src.file, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil || value != "send-keys" {
				return true
			}
			violations = append(violations, sourceSite(src, literal.Pos()))
			return true
		})
	}

	sort.Strings(violations)
	if len(violations) != 0 {
		t.Fatalf("production send-keys literals remain: %s", strings.Join(violations, ", "))
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
		if strings.Contains(command.Operation, "attach") || strings.Contains(command.Payload, "attach") {
			t.Fatalf("control registry contains attach bytes in operation %q payload %q; attach is a separate wire surface", command.Operation, command.Payload)
		}
	}
}

func TestAttachNamespaceIsTheSocketPathValidatedAgainstDarwinCapacity(t *testing.T) {
	root := repositoryRoot(t)
	var guardedPaths []string
	for _, src := range parseProductionGo(t, root) {
		if src.rel != "internal/shim/namespace.go" {
			continue
		}
		for _, declaration := range src.file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Name.Name != "validatedRolePaths" {
				continue
			}
			ast.Inspect(function, func(node ast.Node) bool {
				literal, ok := node.(*ast.CompositeLit)
				if !ok {
					return true
				}
				name, ok := literal.Type.(*ast.Ident)
				if !ok || name.Name != "SocketPathTooLongError" {
					return true
				}
				for _, element := range literal.Elts {
					pair, ok := element.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, keyOK := pair.Key.(*ast.Ident)
					value, valueOK := pair.Value.(*ast.Ident)
					if keyOK && valueOK && key.Name == "Path" {
						guardedPaths = append(guardedPaths, value.Name)
					}
				}
				return true
			})
		}
	}
	if got, want := strings.Join(guardedPaths, ","), "attachPath"; got != want {
		t.Fatalf("validatedRolePaths Darwin capacity guard targets = %q, want longest path %q", guardedPaths, want)
	}
}

func TestAttachControlUnionHasExactlyApprovedFields(t *testing.T) {
	typeOfControl := reflect.TypeOf(shim.AttachControl{})
	got := make([]string, 0, typeOfControl.NumField())
	for index := 0; index < typeOfControl.NumField(); index++ {
		got = append(got, typeOfControl.Field(index).Name)
	}
	want := []string{
		"Version", "Kind", "Session", "Role", "Rows", "Cols", "Outcome", "ViewerPID",
		"PeerPID", "PeerUID", "ShimUID", "Cause", "Disposition", "Bytes", "Undelivered", "KnownUndelivered",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("shim.AttachControl fields = %#v, want exact closed union %#v", got, want)
	}
}

func TestShimHasOneProductionProtocolVersion(t *testing.T) {
	root := repositoryRoot(t)
	var declarations []string
	for _, src := range parseProductionGo(t, root) {
		if !strings.HasPrefix(filepath.ToSlash(src.rel), "internal/shim/") {
			continue
		}
		for _, declaration := range src.file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.CONST {
				continue
			}
			for _, specification := range general.Specs {
				value, ok := specification.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, name := range value.Names {
					if strings.HasSuffix(strings.ToLower(name.Name), "protocolversion") {
						declarations = append(declarations, filepath.ToSlash(src.rel)+":"+name.Name)
					}
				}
			}
		}
	}
	want := []string{"internal/shim/namespace.go:ShimProtocolVersion"}
	if !reflect.DeepEqual(declarations, want) {
		t.Fatalf("shim protocol version declarations = %#v, want one shared production version %#v", declarations, want)
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

func TestLegacyTargetAndPayloadDeliveryAreRetired(t *testing.T) {
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
	if len(targetImports) != 0 {
		t.Fatalf("legacy internal/target imports remain: %q", targetImports)
	}
	if len(deliveryCalls) != 0 {
		t.Fatalf("legacy DeliverPayload calls remain: %q", deliveryCalls)
	}
	if len(deliveryDeclarations) != 0 {
		t.Fatalf("legacy DeliverPayload declarations remain: %q", deliveryDeclarations)
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
