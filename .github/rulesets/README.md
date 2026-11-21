# Rulesets

`main.json` protects `main`: no deletion or force-push, linear history, changes
only through a squash-merged pull request (code-owner review, stale approvals
dismissed, conversations resolved), and the `ci-ok` check must pass. `ci-ok` is
the aggregate job at the end of `.github/workflows/ci.yml`; `integration_id`
15368 is the GitHub Actions app.

The branch does not have to be up to date with `main` before merging
(`strict_required_status_checks_policy` is `false`), so a pull request is not
rebuilt after every other merge. Two pull requests that each pass alone can
still clash once both are merged; CI runs again on `main` after a merge, and
Railway waits for that run before it deploys, so a clash shows as a failing
`main` and does not reach production.

**One `main` run at a time.** Every push to `main` shares one concurrency group
in `ci.yml`, and a run in progress is never cancelled. When several pull
requests are merged close together, the first merge's run finishes, then the
newest merge's run starts; GitHub keeps only the newest waiting run in a group
and cancels the ones in between. The newest commit contains the earlier merges,
so its run tests all of them together. GitHub's merge queue would test each
pull request in turn before it lands, but it is only offered to repositories
owned by an organization, and this one is owned by a user account.

**Import:** Settings → Rules → Rulesets → New ruleset → Import a ruleset, then
pick `main.json`. Or: `gh api repos/andrewcgraves/civic-scanner/rulesets --input .github/rulesets/main.json`.
Editing this file does not change the live ruleset; re-import after changes.

**Solo maintainer:** GitHub does not let you approve your own pull request, so
the file sets `required_approving_review_count` to `0` while there is one
maintainer. Pull requests and `ci-ok` stay mandatory. Set it to `1` when there
is a second reviewer.
