# gitea-mq

A merge queue for Gitea, Forgejo, and GitHub. It tests PRs against their target
branch before landing them, keeping concurrent changes from breaking main.
This fork builds on [Mic92/gitea-mq](https://github.com/Mic92/gitea-mq).

## How it works

1. Schedule auto-merge or add the `merge-queue` label with a write-capable account.
2. gitea-mq waits for required PR checks, dependencies, and configured approvals.
3. CI tests the combined changes on a temporary `gitea-mq/*` branch.
4. Passing changes land; failures are isolated and reported to the PR.

Drafts stay out of the queue. Optional batches test up to 10 PRs together and
bisect failures. Adaptive grouping considers changed files, suite overlap,
dependencies, and queue age. Rejected heads stay held until corrected or
explicitly rescheduled.

## Get started

Requires PostgreSQL, Git, and either Gitea/Forgejo (Gitea API >= 1.22) or a GitHub App.

```sh
go build -o gitea-mq ./cmd/gitea-mq

export GITEA_MQ_DATABASE_URL='postgres://user:password@localhost/gitea_mq'
export GITEA_MQ_EXTERNAL_URL='https://mq.example.com'
export GITEA_MQ_GITEA_URL='https://gitea.example.com'
export GITEA_MQ_REPOS='owner/repository'
export GITEA_MQ_GITEA_TOKEN_FILE='/run/secrets/gitea-token'
export GITEA_MQ_WEBHOOK_SECRET_FILE='/run/secrets/webhook-secret'
./gitea-mq
```

Configure CI to run on `gitea-mq/*` branches. Startup auto-setup configures
protection and webhooks when credentials permit; it needs administrative
access. For preconfigured repositories, use the
[non-admin setup](docs/reference.md#running-without-admin-rights).

[GitHub App setup](docs/reference.md#github-setup) ·
[Container deployment](docs/reference.md#container) ·
[NixOS module](docs/reference.md#nixos-module)

## Choose your gates

| Setting | Purpose |
|---|---|
| `GITEA_MQ_TARGET_BRANCHES=main` | Admit only PRs targeting main. |
| `GITEA_MQ_REQUIRE_APPROVAL=true` | Check approvals before admission and landing. |
| `GITEA_MQ_ENQUEUE_ON_APPROVAL=true` | Use approval as merge intent. |
| `GITEA_MQ_PR_REQUIRED_CHECKS` | Required contexts on the PR head. |
| `GITEA_MQ_MERGE_REQUIRED_CHECKS` | Required contexts on merge branches. |
| `GITEA_MQ_BATCH_MAX=2` through `10` | Enable batching; default `1` tests individually. |
| `GITEA_MQ_BATCH_POLICY_PATH` | Enable adaptive grouping with a target-committed policy. |
| `GITEA_MQ_SKIP_QUEUE_IF_UP_TO_DATE=false` | Always require a merge-branch run. |

Batches fast-forward the protected target to the tested SHA and need push
permission. Check [batching prerequisites](docs/reference.md#batching-bors-style)
and [CI configuration](docs/reference.md#ci-configuration) before enabling them.

## Observe and recover

- Dashboard: `/`; liveness: `/healthz`; database/forge readiness: `/readyz`.
- [Events, queue API, and metrics](docs/reference.md#queue-events).
- [Failure details](docs/reference.md#failure-details) and
  [bounded infrastructure retries](docs/reference.md#retrying-infrastructure-failures).
- [PR-head failure groups](docs/pr-failure-diagnosis.md), including failures before queue admission.
- [Approval and rejected-head recovery](docs/reference.md#approvals).

See the [full configuration and operations reference](docs/reference.md) for
all environment variables, defaults, secret files, permissions, and rollback.

## Develop

```sh
nix develop             # Go, PostgreSQL, and test tools
go test ./...          # integration tests also use Gitea when available
nix fmt
nix flake check -L
```

[MIT license](LICENSE)
