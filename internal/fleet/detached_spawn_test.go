//go:build darwin

package fleet

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"syscall"
	"testing"
)

// This catches a detached starter that routes argv through a shell, changes the
// parent-owned stdio descriptors, or fails to establish the process-session
// and asynchronous-reaping contract before returning creation provenance.
func TestExecDetachedShimStarterPassesTypedRequestDirectlyAndWaitsOnce(t *testing.T) {
	t.Parallel()

	stdin, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close() }()
	stdout, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdout.Close() }()
	stderr, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stderr.Close() }()

	starter := ExecDetachedShimStarter{}
	request := DetachedShimRequest{
		Executable: "/usr/bin/true",
		Argv:       []string{"/usr/bin/true"},
		Directory:  "/",
		Environment: []string{
			"PATH=/usr/bin", "AGENTCTL_RUNTIME_ROOT=/runtime", "AGENTCTL_STATE_ROOT=/state",
		},
		Stdin: stdin, Stdout: stdout, Stderr: stderr,
	}

	process, err := starter.Start(request)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if process.PID() <= 0 {
		t.Fatalf("process.PID() = %d, want a started PID", process.PID())
	}
	if err := <-process.Wait(); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
}

// This catches an exec starter that substitutes or leaves standard descriptors
// unwritable, or starts the hidden shim without the required new session.
func TestExecDetachedShimStarterChildWritesStandardDescriptorsInOwnSession(t *testing.T) {
	t.Parallel()

	stdin, err := os.OpenFile("/dev/null", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close() }()
	stdout, err := os.OpenFile("/dev/null", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdout.Close() }()
	stderr, err := os.OpenFile("/dev/null", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stderr.Close() }()
	sidPath := filepath.Join(t.TempDir(), "sid")
	process, err := (ExecDetachedShimStarter{}).Start(DetachedShimRequest{
		Executable: os.Args[0], Argv: []string{os.Args[0], "-test.run=^TestDetachedShimChildWriterHelper$"}, Directory: "/",
		Environment: append(os.Environ(), "AGENTCTL_DETACHED_TEST_HELPER=1", "AGENTCTL_DETACHED_SID_PATH="+sidPath),
		Stdin:       stdin, Stdout: stdout, Stderr: stderr,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := <-process.Wait(); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	payload, err := os.ReadFile(sidPath)
	if err != nil {
		t.Fatalf("ReadFile(session ID): %v", err)
	}
	sid, err := strconv.Atoi(string(payload))
	if err != nil || sid != process.PID() {
		t.Fatalf("child session ID = %q (%v), want PID %d", payload, err, process.PID())
	}
}

func TestDetachedShimChildWriterHelper(t *testing.T) {
	if os.Getenv("AGENTCTL_DETACHED_TEST_HELPER") != "1" {
		t.Skip("helper process")
	}
	if _, err := fmt.Fprintln(os.Stdout, "stdout is writable"); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(os.Stderr, "stderr is writable"); err != nil {
		t.Fatal(err)
	}
	sid, _, errno := syscall.Syscall(syscall.SYS_GETSID, 0, 0, 0)
	if errno != 0 {
		t.Fatal(errno)
	}
	if err := os.WriteFile(os.Getenv("AGENTCTL_DETACHED_SID_PATH"), []byte(strconv.Itoa(int(sid))), 0o600); err != nil {
		t.Fatal(err)
	}
}

// This catches a production boundary that combines shell-sensitive elements,
// substitutes cwd/environment, or changes any of the caller-selected standard
// descriptors instead of passing one exact typed request to os/exec.
func TestExecDetachedShimStarterPreservesExactProcessBoundary(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	directory := filepath.Join(root, "cwd ; $literal")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(root, "result.json")
	stdinPath := filepath.Join(root, "stdin")
	stdoutPath := filepath.Join(root, "stdout")
	stderrPath := filepath.Join(root, "stderr")
	if err := os.WriteFile(stdinPath, []byte("stdin $HOME ; $(not-executed)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdin := openDetachedBoundaryFile(t, stdinPath, os.O_RDONLY)
	stdout := openDetachedBoundaryFile(t, stdoutPath, os.O_RDWR|os.O_CREATE)
	stderr := openDetachedBoundaryFile(t, stderrPath, os.O_RDWR|os.O_CREATE)
	executable := os.Args[0]
	argv := []string{
		executable,
		"-test.run=^TestDetachedShimExactBoundaryHelper$",
		"two words",
		"$HOME",
		"$(printf shell-was-here)",
		"semi;colon",
		"quote'\"pair",
	}
	environment := []string{
		"AGENTCTL_DETACHED_EXACT_HELPER=1",
		"AGENTCTL_DETACHED_EXACT_RESULT=" + resultPath,
		"AGENTCTL_RUNTIME_ROOT=/runtime root/$literal",
		"AGENTCTL_STATE_ROOT=/state root/$(literal)",
		"SELECTED_VALUE=spaces ; $dollars 'quotes'",
	}
	wantDescriptors := []detachedDescriptorIdentity{
		detachedFileIdentity(t, stdin), detachedFileIdentity(t, stdout), detachedFileIdentity(t, stderr),
	}
	wantDirectory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatal(err)
	}

	process, err := (ExecDetachedShimStarter{}).Start(DetachedShimRequest{
		Executable: executable, Argv: argv, Directory: directory, Environment: environment,
		Stdin: stdin, Stdout: stdout, Stderr: stderr,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := <-process.Wait(); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	payload, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("ReadFile(result): %v", err)
	}
	var got detachedBoundaryObservation
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("Unmarshal(result): %v", err)
	}
	if got.Executable != executable || !reflect.DeepEqual(got.Argv, argv) || got.Directory != wantDirectory || !reflect.DeepEqual(got.Environment, environment) || !reflect.DeepEqual(got.Descriptors, wantDescriptors) {
		t.Fatalf("direct exec observation = %#v, want executable=%q argv=%#v cwd=%q env=%#v descriptors=%#v", got, executable, argv, wantDirectory, environment, wantDescriptors)
	}
	if got.Stdin != "stdin $HOME ; $(not-executed)\n" {
		t.Fatalf("child stdin = %q, want exact parent-selected descriptor payload", got.Stdin)
	}
	if got.SessionID != process.PID() {
		t.Fatalf("child session ID = %d, want direct child PID %d", got.SessionID, process.PID())
	}
	if got, want := readDetachedBoundaryFile(t, stdout), "stdout $HOME ; $(not-executed)\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if got, want := readDetachedBoundaryFile(t, stderr), "stderr $HOME ; $(not-executed)\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

// This catches routing ExecDetachedShimStarter.Start through sh, tmux, or an
// indirect command builder even when such a wrapper ultimately execs the same
// helper and preserves its observable PID and argv.
func TestExecDetachedShimStarterStructurallyPinsDirectExec(t *testing.T) {
	t.Parallel()

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller could not locate detached_spawn_test.go")
	}
	productionPath := filepath.Join(filepath.Dir(currentFile), "detached_spawn.go")
	parsed, err := parser.ParseFile(token.NewFileSet(), productionPath, nil, 0)
	if err != nil {
		t.Fatalf("ParseFile(detached_spawn.go): %v", err)
	}
	var start *ast.FuncDecl
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "Start" || function.Recv == nil {
			continue
		}
		if len(function.Recv.List) == 1 && receiverNames(function.Recv.List[0].Type, "ExecDetachedShimStarter") {
			start = function
			break
		}
	}
	if start == nil {
		t.Fatal("ExecDetachedShimStarter.Start declaration not found")
	}
	directCalls := 0
	ast.Inspect(start.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		packageName, ok := selector.X.(*ast.Ident)
		if !ok || packageName.Name != "exec" {
			return true
		}
		if selector.Sel.Name != "Command" || len(call.Args) != 2 || call.Ellipsis == token.NoPos || !isRequestField(call.Args[0], "Executable") || !isRequestArgvTail(call.Args[1]) {
			t.Errorf("ExecDetachedShimStarter.Start exec call is not exact exec.Command(request.Executable, request.Argv[1:]...): %#v", call)
			return true
		}
		directCalls++
		return true
	})
	if directCalls != 1 {
		t.Fatalf("direct exec.Command calls = %d, want exactly one and no shell/tmux command route", directCalls)
	}
}

type detachedBoundaryObservation struct {
	Executable  string                       `json:"executable"`
	Argv        []string                     `json:"argv"`
	Directory   string                       `json:"directory"`
	Environment []string                     `json:"environment"`
	Descriptors []detachedDescriptorIdentity `json:"descriptors"`
	Stdin       string                       `json:"stdin"`
	SessionID   int                          `json:"session_id"`
}

type detachedDescriptorIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
	Mode   uint32 `json:"mode"`
	Rdev   uint64 `json:"rdev"`
}

func TestDetachedShimExactBoundaryHelper(t *testing.T) {
	if os.Getenv("AGENTCTL_DETACHED_EXACT_HELPER") != "1" {
		t.Skip("helper process")
	}
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := io.ReadAll(os.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	descriptors := make([]detachedDescriptorIdentity, 0, 3)
	for _, descriptor := range []*os.File{os.Stdin, os.Stdout, os.Stderr} {
		descriptors = append(descriptors, detachedFileIdentity(t, descriptor))
	}
	sid, _, errno := syscall.Syscall(syscall.SYS_GETSID, 0, 0, 0)
	if errno != 0 {
		t.Fatal(errno)
	}
	observation := detachedBoundaryObservation{
		Executable: os.Args[0], Argv: os.Args, Directory: directory, Environment: os.Environ(),
		Descriptors: descriptors, Stdin: string(stdin), SessionID: int(sid),
	}
	payload, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("AGENTCTL_DETACHED_EXACT_RESULT"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprint(os.Stdout, "stdout $HOME ; $(not-executed)\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprint(os.Stderr, "stderr $HOME ; $(not-executed)\n"); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func openDetachedBoundaryFile(t *testing.T, path string, flags int) *os.File {
	t.Helper()
	file, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func readDetachedBoundaryFile(t *testing.T, file *os.File) string {
	t.Helper()
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	payload, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(payload)
}

func detachedFileIdentity(t *testing.T, file *os.File) detachedDescriptorIdentity {
	t.Helper()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("descriptor stat type = %T, want *syscall.Stat_t", info.Sys())
	}
	return detachedDescriptorIdentity{Device: uint64(stat.Dev), Inode: uint64(stat.Ino), Mode: uint32(stat.Mode), Rdev: uint64(stat.Rdev)}
}

func receiverNames(expression ast.Expr, want string) bool {
	switch expression := expression.(type) {
	case *ast.Ident:
		return expression.Name == want
	case *ast.StarExpr:
		return receiverNames(expression.X, want)
	default:
		return false
	}
}

func isRequestField(expression ast.Expr, field string) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != field {
		return false
	}
	receiver, ok := selector.X.(*ast.Ident)
	return ok && receiver.Name == "request"
}

func isRequestArgvTail(expression ast.Expr) bool {
	slice, ok := expression.(*ast.SliceExpr)
	if !ok || slice.High != nil || slice.Max != nil || !isRequestField(slice.X, "Argv") {
		return false
	}
	low, ok := slice.Low.(*ast.BasicLit)
	return ok && low.Kind == token.INT && low.Value == "1"
}
