## Release promotion: main → release

Merging this PR triggers the release workflow (tag, build, sign, publish,
formula update). Check exactly one box; the claim below ships with the release.

- [ ] **Checklist run.** This release changes tmux targeting, harness startup,
  or injected command delivery. The release verification checklist was run and
  the results are recorded in `docs/release-checklist.md` on main.
- [ ] **Checklist not required.** No changes in checklist-covered areas since
  the last release.

Version: <!-- output of hack/next-version.sh, e.g. 0.1.2 -->
