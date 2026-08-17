## Release promotion: main → release

Merging this PR triggers the release workflow (tag, build, sign, publish,
formula update). Check exactly one box; the claim below ships with the release.

- [ ] **Checklist run.** This release changes tmux targeting, harness startup,
  or injected command delivery. The release verification checklist was run and
  the results are recorded in `docs/release-verification-notes.md` on main.
- [ ] **Checklist not required.** No changes in checklist-covered areas since
  the last release.

When **Checklist run.** is checked, complete all four fields below.

- [ ] **Detached launch passed.** The ordinary-terminal detached-launch smoke
  passed and its Task 8 record is identified by the evidence location below.
- [ ] **Per-role attach passed.** The B.C1–B.C3 attach smoke observations passed
  and are recorded below; repaint, verbatim input, single-viewer arbitration,
  and clean-EOF readmission passed the named automated guards in
  `docs/release-checklist.md`.
- [ ] **Signal and terminal restoration passed.** The required
  handled/ignored/blocked signal and terminal-restoration properties passed the
  named automated guards in `docs/release-checklist.md`; this box does not
  claim that those properties were observed in the live smoke.

Evidence location: <!-- committed path on main, e.g. docs/release-verification-notes.md -->

Version: <!-- output of hack/next-version.sh, e.g. 0.1.2 -->
