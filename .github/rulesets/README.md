# Rulesets

`main.json` protects `main`: no deletion or force-push, linear history, changes
only through a pull request (1 approval, code-owner review, stale approvals
dismissed, conversations resolved), and the `ci-ok` check must pass on an
up-to-date branch. `ci-ok` is the aggregate job at the end of
`.github/workflows/ci.yml`; `integration_id` 15368 is the GitHub Actions app.

**Import:** Settings → Rules → Rulesets → New ruleset → Import a ruleset, then
pick `main.json`. Or: `gh api repos/andrewcgraves/civic-scanner/rulesets --input .github/rulesets/main.json`.
Editing this file does not change the live ruleset; re-import after changes.

**Solo-maintainer catch:** GitHub does not let you approve your own pull
request, so "1 approval" blocks every PR while there is one maintainer. Either:

- add yourself as a bypass actor (Repository admin, "for pull requests only"),
  so you can merge your own PR after `ci-ok` passes by ticking the bypass box:
  `"bypass_actors": [{ "actor_id": 5, "actor_type": "RepositoryRole", "bypass_mode": "pull_request" }]`
  (note that a bypass skips every rule in the ruleset, the status check included); or
- set `required_approving_review_count` to `0` and `require_code_owner_review`
  to `false` until there is a second reviewer. PRs and `ci-ok` stay mandatory.
