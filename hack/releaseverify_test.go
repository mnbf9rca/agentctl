package hack_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func renderResults(t *testing.T, versions, artifactDir string) (string, error) {
	t.Helper()
	cmd := exec.Command("./release-verify.sh", "--render-results", versions, artifactDir)
	out, err := cmd.Output()
	return string(out), err
}

func TestRenderResultsMatchesGolden(t *testing.T) {
	cases := []struct {
		name        string
		artifactDir string
		goldenPath  string
	}{
		{"measure", "testdata/release-verify-measure-artifact", "testdata/release-verify-measure-results.golden"},
		{"verify live", "testdata/release-verify-live-artifact", "testdata/release-verify-live-results.golden"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := renderResults(t, "testdata/release-verify-versions.txt", tc.artifactDir)
			if err != nil {
				t.Fatalf("render-results failed: %v", err)
			}
			golden, err := os.ReadFile(tc.goldenPath)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(golden) {
				t.Fatalf("output does not match golden.\n--- got ---\n%s\n--- want ---\n%s", got, string(golden))
			}
		})
	}
}

func TestRenderResultsRejects(t *testing.T) {
	cases := []struct {
		name        string
		versions    string
		artifactDir string
	}{
		{"missing versions file", "testdata/absent-versions.txt", "testdata/release-verify-artifact"},
		{"missing artifact dir", "testdata/release-verify-versions.txt", "testdata/absent-artifact"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := renderResults(t, tc.versions, tc.artifactDir)
			var ee *exec.ExitError
			if !errors.As(err, &ee) || ee.ExitCode() != 1 {
				t.Fatalf("want exit status 1, got %v", err)
			}
		})
	}
}

func processCheck(t *testing.T, versions, artifactDir string) (string, error) {
	t.Helper()
	cmd := exec.Command("./release-verify.sh", "--process-check", versions, artifactDir)
	out, err := cmd.Output()
	return string(out), err
}

var requiredProbeEvidence = map[string][]string{
	"probe-1-argv.sh": {
		"OK exit=0",
		"empty-value set OK",
		"read role:  role1",
		"  -v read:   'two words'",
		"name=role1 role=role1 proc=2.1.220",
		"after killing =foo, has-session -t '=foobar': exit0 (foobar survived)",
		"list-panes -t 'probe:rev' resolves to: rev",
		"list-panes -t 'probe:=rev' resolves to: rev",
		"literal send OK",
		"Enter OK",
		"captured: '/clear'",
	},
	"probe-2-targeting.sh": {
		"set-option -t '=alpha':  no such session: =alpha",
		"set-option -t alpha:     OK",
		"show-options -qv -t alpha @k: 'v'",
		"set-option -t alph @k2 v: OK <- PREFIX MATCHED (bad)",
		"has-session -t betab (unique prefix): exit=0 (0 = prefix matched)",
		"has-session -t '=betab':             can't find session: betab\nexit=1",
		"list-panes -t 'alpha:rev'  -> reviewer",
		"list-panes -t 'alpha:=rev' -> can't find window: rev\n   exit=1",
		"list-panes -t 'alpha:dup' picks: can't find window: dup",
		"set:   '1' exit=0",
		"unset: '' exit=0",
		"unset no -q: 'invalid option: @agentctl_absent' exit=1",
		"display-message -p -t $PANE_ID '#{session_name}': alpha",
	},
	"probe-3-ids.sh": {
		"created session id = '$0'",
		"set-option -t $SESSION_ID:  OK",
		"show-options -qv -t $SESSION_ID @agentctl_managed: '1'",
		"decoy 'alphabet' contaminated? managed=''",
		"list-windows -t $SESSION_ID: alpha",
		"has-session  -t $SESSION_ID: exit=0",
		"new-window   -t $SESSION_ID: @2 %2",
		"model set-to-empty via -F: ''",
		"never-set option via -F:   ''",
		"stdout='@4'",
		"killed by id OK",
		"remaining:\n  alphabet",
	},
	"probe-4-attach.sh": {
		"sid=$0",
		"attach-session -t $SESSION_ID     : open terminal failed: not a terminal",
		"attach-session -t '=alpha'  : open terminal failed: not a terminal",
		"attach-session -t '=nope'   : can't find session: nope",
		"-CC attach-session -t $SESSION_ID : tcgetattr failed: Operation not supported by device",
	},
}

func probeAssertion(t *testing.T, probeName, output string) (string, error) {
	t.Helper()
	outputFile := filepath.Join(t.TempDir(), "probe.out")
	writeTestFile(t, outputFile, []byte(output), 0o644)
	command := exec.Command("./release-verify.sh", "--assert-probe", probeName, outputFile)
	result, err := command.CombinedOutput()
	return string(result), err
}

func TestProbeAssertionsRequireEveryExpectedObservation(t *testing.T) {
	for probeName, evidence := range requiredProbeEvidence {
		probeName, evidence := probeName, evidence
		t.Run(probeName, func(t *testing.T) {
			validOutput := strings.Join(evidence, "\n") + "\n"
			if output, err := probeAssertion(t, probeName, validOutput); err != nil {
				t.Fatalf("valid probe output rejected: %v\n%s", err, output)
			}

			for index, missing := range evidence {
				incomplete := append([]string(nil), evidence[:index]...)
				incomplete = append(incomplete, evidence[index+1:]...)
				output, err := probeAssertion(t, probeName, strings.Join(incomplete, "\n")+"\n")
				if err == nil {
					t.Fatalf("probe output passed without %q", missing)
				}
				if !strings.Contains(output, "PROBE ASSERT FAIL ("+probeName+")") {
					t.Fatalf("missing assertion diagnostic for %q:\n%s", missing, output)
				}
			}
		})
	}
}

func TestProcessCheckPasses(t *testing.T) {
	got, err := processCheck(t, "testdata/release-verify-versions.txt", "testdata/release-verify-artifact")
	if err != nil {
		t.Fatalf("process-check failed: %v", err)
	}
	want := "PROCESS CHECK PASS (claude=2.1.220, codex=codex)\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestProcessCheckRejects(t *testing.T) {
	cases := []struct {
		name        string
		versions    string
		artifactDir string
	}{
		{"claude process mismatch", "testdata/release-verify-versions.txt", "testdata/release-verify-artifact-mismatch"},
		{"missing verify row", "testdata/release-verify-versions.txt", "testdata/release-verify-artifact-missing-row"},
		{"missing artifact dir", "testdata/release-verify-versions.txt", "testdata/absent-artifact"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := processCheck(t, tc.versions, tc.artifactDir)
			var ee *exec.ExitError
			if !errors.As(err, &ee) || ee.ExitCode() != 1 {
				t.Fatalf("want exit status 1, got %v", err)
			}
		})
	}
}

type liveFixture struct {
	dir          string
	agentctlLog  string
	tmuxLog      string
	amqLog       string
	skillRootLog string
}

func newLiveFixture(t *testing.T) liveFixture {
	t.Helper()
	dir := t.TempDir()
	for _, path := range []string{"hack", "bin", "docs", "stubs"} {
		if err := os.Mkdir(filepath.Join(dir, path), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script, err := os.ReadFile("release-verify.sh")
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(dir, "hack/release-verify.sh"), script, 0o755)
	for name, evidence := range requiredProbeEvidence {
		body := "#!/usr/bin/env bash\nif IFS= read -r _; then echo 'stdin was not severed' >&2; exit 9; fi\ncat <<'PROBE_OUTPUT'\n" + strings.Join(evidence, "\n") + "\nPROBE_OUTPUT\n"
		writeTestFile(t, filepath.Join(dir, "hack", name), []byte(body), 0o755)
	}
	writeTestFile(t, filepath.Join(dir, "Makefile"), []byte("build:\n\t@true\n"), 0o644)
	writeTestFile(t, filepath.Join(dir, "docs/release-verification-notes.md"), []byte("# Notes\n\n## Results history\n"), 0o644)

	agentctlLog := filepath.Join(t.TempDir(), "agentctl.log")
	tmuxLog := filepath.Join(t.TempDir(), "tmux.log")
	amqLog := filepath.Join(t.TempDir(), "amq.log")
	skillRootLog := filepath.Join(t.TempDir(), "skill-root.log")
	agentctlOwned := filepath.Join(t.TempDir(), "owned")
	agentctlKilled := filepath.Join(t.TempDir(), "killed")
	agentctlRoleB := filepath.Join(t.TempDir(), "role-b")
	agentctlRelaunched := filepath.Join(t.TempDir(), "relaunched")
	pgrepCalls := filepath.Join(t.TempDir(), "pgrep-calls")
	agentctl := `#!/usr/bin/env bash
set -u
printf '%s\n' "$*" >>"$AGENTCTL_TEST_LOG"
case "$1" in
  version)
    echo 'agentctl version test'
    ;;
  status)
    if [ "${AGENTCTL_TEST_COLLISION:-0}" = 1 ] && [ ! -e "$AGENTCTL_TEST_OWNED" ] && [ ! -e "$AGENTCTL_TEST_KILLED" ]; then
      echo 'relverify exists'
      exit 0
    fi
    if [ -e "$AGENTCTL_TEST_OWNED" ]; then
      echo 'SESSION ROLE HARNESS MODEL EFFORT PANE PROCESS STATE'
      echo 'relverify a claude default default %5 2.1.220 running'
      if [ -e "$AGENTCTL_TEST_ROLE_B" ]; then
        if [ -e "$AGENTCTL_TEST_RELAUNCHED" ]; then
          echo "relverify b codex default high ${AGENTCTL_TEST_RELAUNCHED_PANE_ID:-%12} codex running"
        else
          echo 'relverify b codex default high %9 codex running'
        fi
      else
        echo 'relverify b codex default high   missing'
      fi
      exit 0
    fi
    if [ -e "$AGENTCTL_TEST_KILLED" ] && [ -n "${AGENTCTL_TEST_STATUS_AFTER_KILL_CODE:-}" ]; then
      echo "${AGENTCTL_TEST_STATUS_AFTER_KILL_MESSAGE:-agentctl: transport failure}" >&2
      exit "$AGENTCTL_TEST_STATUS_AFTER_KILL_CODE"
    fi
    echo 'agentctl: session "relverify" not found' >&2
    exit 3
    ;;
  skill)
    [ "$2" = install ] || exit 64
    mkdir -p "$HOME/.claude/skills/agentctl" "$HOME/.agents/skills/agentctl"
    printf '%s\n' "$HOME" >>"$AGENTCTL_TEST_SKILL_ROOT_LOG"
    ;;
  launch)
    if [ "$3" = skillverify ]; then
      touch "$AGENTCTL_TEST_SKILL_OWNED"
      echo 'launched skillverify'
      exit 0
    fi
    if [ "${AGENTCTL_TEST_COLLISION:-0}" = 1 ]; then
      echo 'agentctl: session "relverify" already exists' >&2
      exit 3
    fi
    touch "$AGENTCTL_TEST_OWNED"
    touch "$AGENTCTL_TEST_ROLE_B"
    echo 'launched relverify'
    ;;
  clear|compact)
    echo "delivered $1"
    ;;
  relaunch)
    touch "$AGENTCTL_TEST_ROLE_B" "$AGENTCTL_TEST_RELAUNCHED"
    pane_id=${AGENTCTL_TEST_RELAUNCHED_PANE_ID:-%12}
    echo "agentctl: relaunched b in relverify: window @11, pane $pane_id, harness codex (stored), model default (stored), effort high (stored), dir $PWD (stored)"
    ;;
  attach)
    if [ "$3" = skillverify ] && [ "${AGENTCTL_TEST_PART_C_ATTACH_FAIL:-0}" = 1 ]; then
      echo 'attach failed' >&2
      exit 1
    fi
    ;;
  kill)
    if [ "$3" = skillverify ]; then
      rm -f "$AGENTCTL_TEST_SKILL_OWNED"
      touch "$AGENTCTL_TEST_SKILL_KILLED"
    else
      rm -f "$AGENTCTL_TEST_OWNED" "$AGENTCTL_TEST_ROLE_B" "$AGENTCTL_TEST_RELAUNCHED"
      touch "$AGENTCTL_TEST_KILLED"
    fi
    ;;
  *)
    exit 64
    ;;
esac
`
	writeTestFile(t, filepath.Join(dir, "bin/agentctl"), []byte(agentctl), 0o755)
	tmux := `#!/usr/bin/env bash
set -u
printf '%s\n' "$*" >>"$AGENTCTL_TEST_TMUX_LOG"
case "$1" in
  -L)
    [ "$3" = kill-server ] || exit 64
    touch "$AGENTCTL_TEST_SKILL_SOCKET_KILLED"
    ;;
  -V)
    echo 'tmux 3.7b'
    ;;
  list-sessions)
    [ -e "$AGENTCTL_TEST_OWNED" ] && printf '$4\trelverify\n'
    ;;
  list-windows)
    if [ -e "$AGENTCTL_TEST_ROLE_B" ]; then
      if [ -e "$AGENTCTL_TEST_RELAUNCHED" ]; then
        printf '@11\t%s\tb\n' "${AGENTCTL_TEST_RELAUNCHED_PANE_ID:-%12}"
      else
        printf '@8\t%%9\tb\n'
      fi
    fi
    ;;
  kill-window)
    [ "$2" = -t ] && [ "$3" = @8 ] || exit 65
    rm -f "$AGENTCTL_TEST_ROLE_B"
    ;;
  *)
    exit 64
    ;;
esac
`
	writeTestFile(t, filepath.Join(dir, "stubs/tmux"), []byte(tmux), 0o755)
	writeTestFile(t, filepath.Join(dir, "stubs/claude"), []byte("#!/usr/bin/env bash\necho '2.1.220 (Claude Code)'\n"), 0o755)
	writeTestFile(t, filepath.Join(dir, "stubs/codex"), []byte("#!/usr/bin/env bash\necho 'codex-cli 0.146.0'\n"), 0o755)
	amq := `#!/usr/bin/env bash
set -u
printf '%s|%s|%s|%s\n' "$*" "$PWD" "$HOME" "$PATH" >>"$AGENTCTL_TEST_AMQ_LOG"
[ "$1" = coop ] && [ "$2" = init ] && [ "$3" = --agents ] && [ "$4" = a,b,user ] || exit 64
`
	writeTestFile(t, filepath.Join(dir, "stubs/amq"), []byte(amq), 0o755)
	pgrep := `#!/usr/bin/env bash
case "$*" in
  *agentctl-probe-*) exit 1 ;;
  *relverify*)
    calls=0
    [ ! -e "$AGENTCTL_TEST_PGREP_CALLS" ] || calls=$(cat "$AGENTCTL_TEST_PGREP_CALLS")
    calls=$((calls + 1))
    printf '%s\n' "$calls" >"$AGENTCTL_TEST_PGREP_CALLS"
    code=${AGENTCTL_TEST_PGREP_CODE:-1}
    if [ -n "${AGENTCTL_TEST_PGREP_CODES:-}" ]; then
      IFS=, read -r -a codes <<<"$AGENTCTL_TEST_PGREP_CODES"
      index=$((calls - 1))
      [ "$index" -lt "${#codes[@]}" ] || index=$((${#codes[@]} - 1))
      code=${codes[$index]}
    fi
    [ "$code" -eq 0 ] && echo '123 tmux relverify'
    [ "$code" -gt 1 ] && echo 'pgrep failed' >&2
    exit "$code"
    ;;
  *) exec /usr/bin/pgrep "$@" ;;
esac
`
	writeTestFile(t, filepath.Join(dir, "stubs/pgrep"), []byte(pgrep), 0o755)

	runCommand(t, dir, "git", "init", "-q")
	runCommand(t, dir, "git", "add", ".")
	runCommand(t, dir, "git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")

	t.Setenv("AGENTCTL_TEST_LOG", agentctlLog)
	t.Setenv("AGENTCTL_TEST_TMUX_LOG", tmuxLog)
	t.Setenv("AGENTCTL_TEST_AMQ_LOG", amqLog)
	t.Setenv("AGENTCTL_TEST_SKILL_ROOT_LOG", skillRootLog)
	t.Setenv("AGENTCTL_TEST_OWNED", agentctlOwned)
	t.Setenv("AGENTCTL_TEST_KILLED", agentctlKilled)
	t.Setenv("AGENTCTL_TEST_ROLE_B", agentctlRoleB)
	t.Setenv("AGENTCTL_TEST_RELAUNCHED", agentctlRelaunched)
	t.Setenv("AGENTCTL_TEST_SKILL_OWNED", filepath.Join(t.TempDir(), "skill-owned"))
	t.Setenv("AGENTCTL_TEST_SKILL_KILLED", filepath.Join(t.TempDir(), "skill-killed"))
	t.Setenv("AGENTCTL_TEST_SKILL_SOCKET_KILLED", filepath.Join(t.TempDir(), "skill-socket-killed"))
	t.Setenv("AGENTCTL_TEST_PGREP_CALLS", pgrepCalls)
	t.Setenv("PATH", filepath.Join(dir, "stubs")+string(os.PathListSeparator)+os.Getenv("PATH"))
	return liveFixture{dir: dir, agentctlLog: agentctlLog, tmuxLog: tmuxLog, amqLog: amqLog, skillRootLog: skillRootLog}
}

func writeTestFile(t *testing.T, path string, body []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, body, mode); err != nil {
		t.Fatal(err)
	}
}

func runCommand(t *testing.T, dir, name string, arguments ...string) {
	t.Helper()
	command := exec.Command(name, arguments...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%s %v failed: %v\n%s", name, arguments, err, output)
	}
}

func (fixture liveFixture) run(t *testing.T, input string, environment ...string) (string, error) {
	t.Helper()
	command := exec.Command("bash", "hack/release-verify.sh", "--non-interactive")
	command.Dir = fixture.dir
	command.Stdin = strings.NewReader(input)
	command.Env = append(os.Environ(), environment...)
	output, err := command.CombinedOutput()
	return string(output), err
}

func (fixture liveFixture) calls(t *testing.T) []string {
	t.Helper()
	body, err := os.ReadFile(fixture.agentctlLog)
	if err != nil {
		t.Fatal(err)
	}
	return strings.FieldsFunc(strings.TrimSpace(string(body)), func(character rune) bool { return character == '\n' })
}

func (fixture liveFixture) tmuxCalls(t *testing.T) []string {
	t.Helper()
	body, err := os.ReadFile(fixture.tmuxLog)
	if err != nil {
		t.Fatal(err)
	}
	return strings.FieldsFunc(strings.TrimSpace(string(body)), func(character rune) bool { return character == '\n' })
}

func TestLiveVerificationCompletesAndAppendsEvidence(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 11))
	if err != nil {
		t.Fatalf("release verification failed: %v\n%s", err, output)
	}
	for _, want := range []string{
		"=== Part A — Automated release checks ===",
		"[PASS A.",
		"=== Part B — Live release-candidate delivery ===",
		"Expected output:",
		"operator confirmed:",
		"=== Part C — Live skill discovery and meaning ===",
		"harness lists the agentctl skill",
		"probe answer matches references/status-states.md",
		"ALL VERIFIED — evidence appended",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
	for index, probe := range []string{"probe-1-argv.sh", "probe-2-targeting.sh", "probe-3-ids.sh", "probe-4-attach.sh"} {
		want := fmt.Sprintf("[PASS A.%d] %s assertion completed", index+2, probe)
		if !strings.Contains(output, want) {
			t.Fatalf("probe completion missing %q:\n%s", want, output)
		}
	}
	notes, err := os.ReadFile(filepath.Join(fixture.dir, "docs/release-verification-notes.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(notes), "- Mode: `verify-live`; harness: `both`") {
		t.Fatalf("notes missing live evidence:\n%s", notes)
	}
	for _, want := range []string{
		"- Part A:",
		"- Part B:",
		"- Part C:",
		"operator confirmed: attach narration",
		"operator confirmed: harness lists the agentctl skill",
	} {
		if !strings.Contains(string(notes), want) {
			t.Fatalf("notes missing %q:\n%s", want, notes)
		}
	}
	wantCalls := []string{
		"version",
		"status --session relverify",
		"launch --session relverify --roles a:claude,b:codex --efforts b:high",
		"clear --session relverify a",
		"clear --session relverify b",
		"compact --session relverify a",
		"status --session relverify",
		"relaunch --session relverify b",
		"status --session relverify",
		"kill --session relverify",
		"status --session relverify",
		"skill install",
		"launch --session skillverify --roles a:claude,b:codex --dir " + filepath.Join(strings.TrimSpace(readTestFile(t, fixture.skillRootLog)), "..", "project"),
		"attach --session skillverify",
		"kill --session skillverify",
	}
	if got := fixture.calls(t); strings.Join(got, "\n") != strings.Join(wantCalls, "\n") {
		t.Fatalf("agentctl calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantCalls, "\n"))
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestLiveVerificationPartCRejectCleansOnlyNamedResources(t *testing.T) {
	fixture := newLiveFixture(t)
	originalHome := os.Getenv("HOME")
	originalPath := os.Getenv("PATH")
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	output, err := fixture.run(t, strings.Repeat("y\n", 9)+"n\n")
	if err == nil {
		t.Fatalf("release verification accepted Part C refusal:\n%s", output)
	}
	if !strings.Contains(output, "operator refused checkpoint: harness lists the agentctl skill") {
		t.Fatalf("output missing Part C refusal:\n%s", output)
	}
	calls := strings.Join(fixture.calls(t), "\n")
	for _, want := range []string{"kill --session skillverify", "kill --session relverify"} {
		if !strings.Contains(calls, want) {
			t.Fatalf("calls missing %q:\n%s", want, calls)
		}
	}
	if !strings.Contains(strings.Join(fixture.tmuxCalls(t), "\n"), "-L agentctl-skill-verify-") || !strings.Contains(strings.Join(fixture.tmuxCalls(t), "\n"), "kill-server") {
		t.Fatalf("named Part C tmux socket was not removed:\n%s", strings.Join(fixture.tmuxCalls(t), "\n"))
	}
	for _, path := range []string{os.Getenv("AGENTCTL_TEST_SKILL_KILLED"), os.Getenv("AGENTCTL_TEST_SKILL_SOCKET_KILLED")} {
		if _, statErr := os.Stat(path); statErr != nil {
			t.Fatalf("cleanup marker %q: %v", path, statErr)
		}
	}
	for _, call := range fixture.tmuxCalls(t) {
		if call == "kill-server" {
			t.Fatalf("bare/default-socket kill-server invoked:\n%s", strings.Join(fixture.tmuxCalls(t), "\n"))
		}
	}
	amqRecord := strings.TrimSpace(readTestFile(t, fixture.amqLog))
	if !strings.Contains(amqRecord, "coop init --agents a,b,user|") || strings.Contains(amqRecord, "|"+originalDir+"|"+originalHome+"|"+originalPath) {
		t.Fatalf("Part C did not use isolated cwd/HOME/PATH: %q", amqRecord)
	}
	skillHome := strings.TrimSpace(readTestFile(t, fixture.skillRootLog))
	if _, statErr := os.Stat(skillHome); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("temporary Part C HOME survived cleanup: %q, err=%v", skillHome, statErr)
	}
	if got, getwdErr := os.Getwd(); getwdErr != nil || got != originalDir || os.Getenv("HOME") != originalHome || os.Getenv("PATH") != originalPath {
		t.Fatalf("fixture environment was not restored: cwd=%q err=%v HOME=%q PATH=%q", got, getwdErr, os.Getenv("HOME"), os.Getenv("PATH"))
	}
}

func TestLiveVerificationPartCAttachAbortCleansResources(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 9), "AGENTCTL_TEST_PART_C_ATTACH_FAIL=1")
	if err == nil {
		t.Fatalf("release verification accepted Part C attach failure:\n%s", output)
	}
	if !strings.Contains(output, "Part C attach guidance failed") {
		t.Fatalf("output missing attach failure:\n%s", output)
	}
	calls := strings.Join(fixture.calls(t), "\n")
	if !strings.Contains(calls, "kill --session skillverify") || !strings.Contains(calls, "kill --session relverify") {
		t.Fatalf("cleanup calls missing:\n%s", calls)
	}
	if _, statErr := os.Stat(os.Getenv("AGENTCTL_TEST_SKILL_SOCKET_KILLED")); statErr != nil {
		t.Fatalf("named socket cleanup missing: %v", statErr)
	}
}

func TestLiveVerificationRejectsAttachNarrationAndTearsDown(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, "n\n")
	if err == nil {
		t.Fatalf("release verification accepted attach-narration refusal:\n%s", output)
	}
	if !strings.Contains(output, "operator refused checkpoint: attach narration") {
		t.Fatalf("output missing attach-narration refusal:\n%s", output)
	}
	calls := strings.Join(fixture.calls(t), "\n")
	if !strings.Contains(calls, "kill --session relverify") || strings.Contains(calls, "clear --session relverify") || strings.Contains(calls, "compact --session relverify") || strings.Contains(calls, "relaunch --session relverify") {
		t.Fatalf("Part B commands continued after attach-narration refusal:\n%s", calls)
	}
}

func TestLiveVerificationRelaunchesCodexFromStoredQuadByExactIDs(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 11))
	if err != nil {
		t.Fatalf("release verification failed: %v\n%s", err, output)
	}
	canonicalDir, canonicalErr := filepath.EvalSymlinks(fixture.dir)
	if canonicalErr != nil {
		t.Fatal(canonicalErr)
	}
	for _, want := range []string{
		"RELAUNCH PASS (role b reported missing after exact-ID removal)",
		"agentctl: relaunched b in relverify: window @11, pane %12, harness codex (stored), model default (stored), effort high (stored), dir " + canonicalDir + " (stored)",
		"RELAUNCH PASS (role b restored to running)",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
	wantAgentctl := []string{
		"version",
		"status --session relverify",
		"launch --session relverify --roles a:claude,b:codex --efforts b:high",
		"clear --session relverify a",
		"clear --session relverify b",
		"compact --session relverify a",
		"status --session relverify",
		"relaunch --session relverify b",
		"status --session relverify",
		"kill --session relverify",
		"status --session relverify",
		"skill install",
		"launch --session skillverify --roles a:claude,b:codex --dir " + filepath.Join(strings.TrimSpace(readTestFile(t, fixture.skillRootLog)), "..", "project"),
		"attach --session skillverify",
		"kill --session skillverify",
	}
	if got := fixture.calls(t); strings.Join(got, "\n") != strings.Join(wantAgentctl, "\n") {
		t.Fatalf("agentctl calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantAgentctl, "\n"))
	}
	wantTmux := []string{
		"-V",
		"list-sessions -F #{session_id}\t#{session_name}",
		"list-windows -t $4 -F #{window_id}\t#{pane_id}\t#{@agentctl_role}",
		"kill-window -t @8",
		"list-windows -t $4 -F #{window_id}\t#{pane_id}\t#{@agentctl_role}",
	}
	gotTmux := fixture.tmuxCalls(t)
	if len(gotTmux) != len(wantTmux)+1 || strings.Join(gotTmux[:len(wantTmux)], "\n") != strings.Join(wantTmux, "\n") || !strings.HasPrefix(gotTmux[len(wantTmux)], "-L agentctl-skill-verify-") || !strings.HasSuffix(gotTmux[len(wantTmux)], " kill-server") {
		t.Fatalf("tmux calls:\n%s\nwant:\n%s", strings.Join(gotTmux, "\n"), strings.Join(wantTmux, "\n"))
	}
	notes, err := os.ReadFile(filepath.Join(fixture.dir, "docs/release-verification-notes.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"- Relaunch: PASS (stored codex/default/high provenance; pane ID changed); fresh codex input with no junk: operator confirmed: y",
		"- Teardown status: exit 3 (session absent; other tmux sessions remained)",
	} {
		if !strings.Contains(string(notes), want) {
			t.Fatalf("notes missing %q:\n%s", want, notes)
		}
	}
}

func TestLiveVerificationPairsNewPaneWithFreshInputObservation(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 11))
	if err != nil {
		t.Fatalf("release verification failed: %v\n%s", err, output)
	}
	for _, want := range []string{
		"In the codex tab, type junk into the input box again; do NOT press Enter.",
		"RELAUNCH PASS (role b pane changed from %9 to %12)",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
	script, readErr := os.ReadFile(filepath.Join(fixture.dir, "hack/release-verify.sh"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	wantPrompt := `One of the fleet's harnesses was terminated, and agentctl relaunched it from
the fleet's stored configuration. The new pane is a new process: its harness,
model and effort carry over; its conversation does not, so the junk you typed
is gone.

Do you see a fresh, ready codex input surface with no trace of that junk?`
	if !strings.Contains(string(script), wantPrompt) {
		t.Fatalf("release verifier missing final relaunch prompt:\n%s", wantPrompt)
	}
}

func TestLiveVerificationRejectsReusedRelaunchPaneID(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 9), "AGENTCTL_TEST_RELAUNCHED_PANE_ID=%9")
	if err == nil {
		t.Fatalf("release verification accepted reused pane ID:\n%s", output)
	}
	if !strings.Contains(output, "RELAUNCH FAIL (recreated role b reused original pane %9)") {
		t.Fatalf("output missing reused-pane failure:\n%s", output)
	}
}

func TestLiveVerificationPrintsProbeDiagnosticsOnAssertionFailure(t *testing.T) {
	fixture := newLiveFixture(t)
	writeTestFile(t, filepath.Join(fixture.dir, "hack/probe-1-argv.sh"), []byte("#!/usr/bin/env bash\necho 'tmux diagnostic: observed broken behavior'\n"), 0o755)
	runCommand(t, fixture.dir, "git", "add", "hack/probe-1-argv.sh")
	runCommand(t, fixture.dir, "git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "-qm", "broken probe")
	output, err := fixture.run(t, "")
	if err == nil {
		t.Fatalf("release verification passed without required probe evidence:\n%s", output)
	}
	for _, want := range []string{
		"tmux diagnostic: observed broken behavior",
		"PROBE ASSERT FAIL (probe-1-argv.sh)",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}

func TestLiveVerificationRejectsAttestationAndTearsDown(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, "y\ny\nn\n")
	if err == nil {
		t.Fatalf("release verification passed after rejection:\n%s", output)
	}
	calls := strings.Join(fixture.calls(t), "\n")
	if !strings.Contains(calls, "kill --session relverify") {
		t.Fatalf("kill not attempted after rejection:\n%s", calls)
	}
	if strings.Contains(calls, "clear --session relverify b") || strings.Contains(calls, "compact --session relverify a") {
		t.Fatalf("flow continued after rejection:\n%s", calls)
	}
	for _, want := range []string{
		"TEARDOWN PASS (agentctl status exit 3 proves relverify is absent)",
		"TEARDOWN PASS (no relverify tmux process remains)",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q after rejection:\n%s", want, output)
		}
	}
}

func TestLiveVerificationRefusesExistingSessionWithoutKilling(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, "", "AGENTCTL_TEST_COLLISION=1")
	if err == nil {
		t.Fatalf("release verification passed with existing session:\n%s", output)
	}
	calls := strings.Join(fixture.calls(t), "\n")
	if strings.Contains(calls, "launch --session relverify") || strings.Contains(calls, "kill --session relverify") {
		t.Fatalf("existing session was launched or killed:\n%s", calls)
	}
}

func TestLiveVerificationRejectsUnexpectedStatusFailure(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 11), "AGENTCTL_TEST_STATUS_AFTER_KILL_CODE=6", "AGENTCTL_TEST_STATUS_AFTER_KILL_MESSAGE=agentctl: transport failure")
	if err == nil || !strings.Contains(output, "TEARDOWN FAIL (agentctl status") {
		t.Fatalf("unexpected status failure was not rejected: err=%v\n%s", err, output)
	}
}

func TestLiveVerificationAcceptsNoServerStatusAsAbsent(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 11), "AGENTCTL_TEST_STATUS_AFTER_KILL_CODE=6", "AGENTCTL_TEST_STATUS_AFTER_KILL_MESSAGE=agentctl: tmux list sessions: exit status 1: no server running")
	if err != nil {
		t.Fatalf("no-server status was not accepted as absence: %v\n%s", err, output)
	}
	notes, readErr := os.ReadFile(filepath.Join(fixture.dir, "docs/release-verification-notes.md"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(notes), "- Teardown status: exit 6 (session absent; relverify was last and tmux server exited)") {
		t.Fatalf("notes did not record expected exit 6 outcome:\n%s", notes)
	}
}

func TestLiveVerificationRejectsPgrepFailure(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 11), "AGENTCTL_TEST_PGREP_CODE=2")
	if err == nil || !strings.Contains(output, "TEARDOWN FAIL (pgrep exited 2)") {
		t.Fatalf("pgrep failure was not rejected: err=%v\n%s", err, output)
	}
}

func TestLiveVerificationWaitsForTmuxAttachClientToExit(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, strings.Repeat("y\n", 11), "AGENTCTL_TEST_PGREP_CODES=0,1")
	if err != nil {
		t.Fatalf("transient tmux survivor was not given time to exit: %v\n%s", err, output)
	}
	if !strings.Contains(output, "TEARDOWN PASS (no relverify tmux process remains)") {
		t.Fatalf("output missing teardown pass after transient survivor:\n%s", output)
	}
}

func TestLiveVerificationPromptsRejectInvalidInput(t *testing.T) {
	fixture := newLiveFixture(t)
	output, err := fixture.run(t, "\nmaybe\n"+strings.Repeat("y\n", 11))
	if err != nil {
		t.Fatalf("release verification failed: %v\n%s", err, output)
	}
	for _, want := range []string{"unrecognised: '' — answer y or n", "unrecognised: 'maybe' — answer y or n"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}
