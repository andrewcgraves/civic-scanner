# Rulesets

`main.json` protects `main`: no deletion or force-push, linear history, changes
only through a squash-merged pull request (code-owner review, stale approvals
dismissed, conversations resolved), and the `ci-ok` check must pass. `ci-ok` is
the aggregate job at the end of `.github/workflows/ci.yml`; `integration_id`
15368 is the GitHub Actions app.

The branch does not have to be up to date with `main` before merging
(`strict_required_status_checks_policy` is `false`), so a pull request is not
rebuilt after every other merge. Two pull requests that each pass alone can
still clash once both are merged; CI runs again on `main` after every merge,
and Railway waits for that run before it deploys, so a clash shows as a failing
`main` and does not reach production.

**Import:** Settings → Rules → Rulesets → New ruleset → Import a ruleset, then
pick `main.json`. Or: `gh api repos/andrewcgraves/civic-scanner/rulesets --input .github/rulesets/main.json`.
Editing this file does not change the live ruleset; re-import after changes.

**Solo maintainer:** GitHub does not let you approve your own pull request, so
the file sets `required_approving_review_count` to `0` while there is one
maintainer. Pull requests and `ci-ok` stay mandatory. Set it to `1` when there
is a second reviewer.
