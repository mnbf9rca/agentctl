## Release promotion: main → release

Merging this PR triggers the release workflow (tag, build, sign, publish,
formula update). Check exactly one box; the claim below ships with the release.

- [ ] **Checklist run.** This release changes tmux targeting, harness startup,
  or injected command delivery. The release verification checklist was run and
  the results are recorded in `docs/release-verification-notes.md` on main.
- [ ] **Checklist not required.** No changes in checklist-covered areas since
  the last release.

When **Checklist run.** is checked, complete all four fields below.

- [ ] **Detached launch passed.** The ordinary-terminal detached-launch leg
  passed and is recorded in the evidence location below.
- [ ] **Per-role attach passed.** The attach/repaint/verbatim-input and clean
  disconnect/re-attach legs passed and are recorded below.
- [ ] **Signal and terminal restoration passed.** Every required
  handled/ignored/blocked signal and terminal-restoration leg passed and is
  recorded below.

Evidence location: <!-- committed path on main, e.g. docs/release-verification-notes.md -->

Version: <!-- output of hack/next-version.sh, e.g. 0.1.2 -->
