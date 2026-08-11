# Release verification checklist

Run every step for 0.5.0. Stop at the first unexpected observation and keep
the evidence directory. Record facts, not conclusions: exit-0 delivery reports
bytes written and submit observed; only the harness surface can show that
`/clear` or `/compact` executed. Governing contracts:
[design §§1.1 and 15](superpowers/specs/2026-08-01-agentctl-design.md) and
[SECURITY.md](../SECURITY.md).

1. Stop every pre-0.5 fleet with the old binary before replacing it. **Observe:** the old binary no longer reports a managed fleet; 0.5 does not migrate or adopt tmux-metadata fleets.

2. Run `git fetch origin`. **Observe:** exit 0.

3. Run `git switch main`. **Observe:** Git reports that `main` is checked out.

4. Run `git pull --ff-only origin main`. **Observe:** a fast-forward or `Already up to date.`

5. Run `git status --short`. **Observe:** no output. Stop if any path is printed.

6. Run `make build`. **Observe:** `bin/agentctl` is built and the command exits 0.

7. Run `./bin/agentctl version`, then `git rev-parse HEAD`. **Observe:** `agentctl SHORT_SHA` and the full Git SHA identify the same commit.

8. Create the private evidence directory:

   ```bash
   release_evidence="$(mktemp -d /tmp/agentctl-release-0.5.0.XXXXXX)"
   chmod 0700 "$release_evidence"
   printf 'evidence=%s\n' "$release_evidence"
   ```

   **Observe:** one absolute `/tmp/agentctl-release-0.5.0.*` path.

9. Run the automated candidate walkthrough:

   ```bash
   hack/release-verify.sh --task8 "$(pwd -P)/bin/agentctl" "$release_evidence/task8"
   ```

   **Observe:** exit 0 and this output shape:

   ```text
   == Task 8: built release-candidate surface ==
   == Task 8: installed skill from release candidate ==
   == Task 8: separately built protocol-skew matrix ==
   == Task 8: live isolated layout/lifecycle evidence ==
   == Task 8: kernel absence/refusal and raw-token evidence ==
   == Task 8: structural, archive-license, and skill drift guards ==
   == Task 8: actual snapshot archive license contents ==
   TASK8 RELEASE WALKTHROUGH PASS evidence=...
   ```

   This command already covers the public `run` surface, hidden `__shim`,
   durable state-root guards, the complete runtime state vocabulary,
   `anchored`/`unanchored` confidence, optional presentation, tmux layout
   changes, closed delivery, no-tmux foreground operation, attach refusal,
   relaunch, kill, protocol skew, skill drift, archives, and isolated cleanup.

10. Run `sed -n '1p' "$release_evidence/task8/owned-process-sweep.txt"`. **Observe:** `TASK8 OWNED PROCESS SWEEP PASS`.

11. Run `sed -n '1p' "$release_evidence/task8/cleanup.txt"`. **Observe:** `TASK8 CLEANUP PASS root=... absent=true`.

12. Run `hack/probe-shim-sighup.sh --harness claude --output "$release_evidence/claude-sighup.txt"`. **Observe:** `probe-shim-sighup: recorded claude result in ...` and exit 0.

13. Run `hack/probe-shim-sighup.sh --harness codex --output "$release_evidence/codex-sighup.txt"`. **Observe:** `probe-shim-sighup: recorded codex result in ...` and exit 0.

14. Read both records with `sed -n '1,20p' "$release_evidence/claude-sighup.txt"` and the same command for `codex-sighup.txt`. **Observe in each:** the installed version; positive shim and child PIDs; `topology=shim-parent-of-harness-child-on-pty`; `child_ppid_matches=true`; a nonempty PTY; the exact harness path in `child_command`; `signal_target=owned-shim-only`; `shim_terminated=true`; the recorded `child_outcome`; and `default_tmux_targeted=false`. Do not turn `child_outcome` into an absence claim.

15. Prepare one isolated environment for the real-harness checks:

    ```bash
    repo="$(pwd -P)"
    install -d -m 0700 "$release_evidence/runtime" "$release_evidence/state" "$release_evidence/project"
    {
      printf 'export AGENTCTL_RUNTIME_ROOT=%q\n' "$release_evidence/runtime"
      printf 'export AGENTCTL_STATE_ROOT=%q\n' "$release_evidence/state"
      printf 'export PATH=%q\n' "$repo/bin:$PATH"
      printf 'cd %q\n' "$release_evidence/project"
    } >"$release_evidence/live.env"
    printf 'live environment=%s\n' "$release_evidence/live.env"
    ```

    **Observe:** all three directories and the absolute `live.env` path. In
    Terminals A and B, source that path once before running steps 16–29.

16. After sourcing `live.env`, run `amq coop init --agents claude,codex,user`. **Observe:** `Root: .agent-mail`, agents `claude, codex, user`, and `Created: .amqrc` below the throwaway project.

17. In Terminal A, run `agentctl run --session release-claude --role claude --harness claude`. **Observe:** Claude reaches its ready input surface; Terminal A remains attached and no tmux window opens.

18. In Terminal B, run `agentctl status --session release-claude`. **Observe:** the header includes `CONFIDENCE`, `SHIM`, `CHILD`, `PRESENTATION`, and `STATE`; Claude has positive shim and child PIDs with `anchored`, `gone`, and `running`.

19. In Terminal A, type junk without submitting it. **Observe:** the junk remains visible in Claude's input.

20. In Terminal B, run `agentctl clear --session release-claude claude`. **Observe:** `agentctl: clear for role "claude" in session "release-claude" wrote 6 bytes and observed submit`; Terminal A shows the junk disappear and Claude execute `/clear`.

21. In Terminal A, submit one short prompt. **Observe:** Claude completes its response before you continue.

22. In Terminal B, run `agentctl compact --session release-claude claude`. **Observe:** `agentctl: compact for role "claude" in session "release-claude" wrote 8 bytes and observed submit`; Terminal A shows Claude execute `/compact`. Record Claude's exact response.

23. In Terminal B, run `agentctl kill --session release-claude`. **Observe:** `agentctl: killed session "release-claude"; every recorded child was observed absent`; Terminal A returns to its shell with the foreground-role exit fact.

24. In Terminal A, run `agentctl run --session release-codex --role codex --harness codex`. **Observe:** Codex reaches its ready input surface; Terminal A remains attached and no tmux window opens.

25. In Terminal B, run `agentctl status --session release-codex`. **Observe:** Codex has positive shim and child PIDs with `anchored`, `gone`, and `running`.

26. In Terminal A, type junk without submitting it. **Observe:** the junk remains visible in Codex's input.

27. In Terminal B, run `agentctl clear --session release-codex codex`. **Observe:** `agentctl: clear for role "codex" in session "release-codex" wrote 6 bytes and observed submit`; Terminal A shows the junk disappear and Codex execute `/clear`.

28. In Terminal B, run `agentctl kill --session release-codex`. **Observe:** `agentctl: killed session "release-codex"; every recorded child was observed absent`; Terminal A returns to its shell with the foreground-role exit fact.

29. Run `agentctl status`. **Observe:** only the status table header; no role rows.

30. Add a dated factual record to `docs/release-verification-notes.md`. **Observe:** it names the candidate SHA, Task 8 final line, process-sweep and cleanup lines, installed harness versions, both SIGHUP `child_outcome` values, both `anchored`/`gone`/`running` rows, both real `/clear` observations, and Claude's exact `/compact` response. Do not copy credentials or terminal transcripts.

31. Merge the evidence update through a ready PR and reviewer gate. **Observe:** `origin/main` contains the dated record and the PR's own `pull_request` run is green.
