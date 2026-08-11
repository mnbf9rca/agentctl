# Release verification checklist

## Prerequisites

- Start at the repository root on the exact 0.5.0 release candidate. The worktree
  must be clean. Stop every pre-0.5 agentctl fleet first; 0.5 does not migrate it.
- Use macOS and iTerm2. Install `make`, Go, `tmux`, `claude`, `codex`, `amq`, and
  `install`.
- Sign in to Claude Code and Codex. Make sure SSH signing works and another
  person is available to review the evidence PR.

## Commands

Run these two commands from the repository root:

```bash
make build
hack/release-verify.sh
```

Stop if either command exits nonzero. Otherwise, leave the verifier running and
follow the numbered prompts it prints.

## Human-only steps

1. When instructed, open the second iTerm2 window and visually confirm that the
   attach message and the Claude Code and Codex tabs match the prompt.
2. Stage the requested unsubmitted input in each harness. Confirm in order that
   Claude Code clears, Codex clears, Claude Code compacts, the relaunched Codex
   pane is fresh, and the attachment detaches with the reported session state.
3. Choose one of the sign-in paths printed by the verifier. Approve only the
   exact file copy, Keychains link, temporary keychain, sign-in, or biometric
   prompt you intend to allow. Confirm that both harnesses reach ready prompts.
4. In both harnesses, visually confirm that `/skills` lists `agentctl`. Ask the
   exact question printed by the verifier. Confirm that both answers match the
   meaning it displays.
5. Answer `y` only for an observation you made. Answer `n` at the first mismatch,
   preserve the evidence, and record a failed verdict; delivery output alone is
   not evidence that a harness action executed.
6. After an exit-0 run prints `ALL VERIFIED — evidence appended`, read the new
   results block and record the final verdict. Put it in a signed, ready PR. Wait
   for that PR's own green CI run and another person's review. Approve the
   signing prompt when it appears. Do not merge your own PR.

## Evidence

The verifier creates its folders, removes the test fleets and temporary setup
it owns, and keeps raw files under `/tmp/agentctl-release-verify.*/`. The
results name the exact `verify-live` path. An exit-0 run adds one dated block beneath
`## Results history` in
[`docs/release-verification-notes.md`](release-verification-notes.md). Review and
commit that block without copying credentials or full terminal transcripts.
