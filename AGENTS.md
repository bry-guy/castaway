# Agent Guidelines for Castaway

## Build/Lint/Test Commands

The app uses mise for task management. Available tasks:

- **Root level** (`mise tasks ls` in repo root):
  - `ci`: Run all monorepo CI tasks (runs `mise run //apps/...:ci`)

- **Apps level** (`mise tasks ls` in `./apps`):
  - `ci`: Run all monorepo CI tasks

- **CLI app level** (`mise tasks ls` in `./apps/cli`):
  - `lint`: Run golangci-lint
  - `test`: Run tests (depends on lint)
  - `run`: Run the app (depends on lint)
  - `clean`: Remove bin directory
  - `build`: Build the app (depends on clean)
  - `ci`: Run lint, test, build for CI

## Change Rules

- ALWAYS ensure the app lints, tests, builds, and runs before committing or PRing
- Commit complete thoughts frequently; this repo squash merges PRs, so prefer smaller committed checkpoints over large uncommitted changesets
- Regression and integration tests may be changed or removed only after the advisor reviews the change and confirms it doesn't regress gameplay or scoring behavior; mention each such change to the user

## Documentation Rules

- Keep repository and app docs aligned with `docs/documentation-standards.md`
- Each app should keep `README.md`, `CHANGELOG.md`, `functional-requirements.md`, `non-functional-requirements.md`, `production-readiness-checklist.md`, and `plans/` present and in sync
- Plan documents should be well-named and carry a status of `planning`, `in-progress`, or `done`

## Roles

Follow the `least-access` skill. Agents get no castaway secrets by default: fnox reads the `castaway` vault through
Bryan's 1Password desktop app, so every secret read needs his Touch ID — treat that prompt as an admin request and
only trigger it after saying why.

| Role | Can | How |
|---|---|---|
| castaway-operator | read the cluster (k3s view), read prod data | `~/.kube/config` is read-only; prod DB reads use `castaway_web_ro` / `castaway_discord_bot_ro` (passwords in infra's `infra-developer` vault) |
| castaway-developer | operator + deploy | git push; Argo CD syncs. No direct DB writes — missing capabilities go to an admin CLI request |
| castaway-admin | secrets, migrations by hand, DB writes | Bryan only: print the exact command and wait |
