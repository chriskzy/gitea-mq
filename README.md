# gitea-mq

Status: stable

A merge queue for [Gitea](https://gitea.com) and GitHub. Serializes PR merges
so your main branch stays green.

## Workflow

There are no bot commands. gitea-mq registers itself as a required status check
and hooks into the existing "Merge when checks succeed" / auto-merge button.

When someone clicks that button, gitea-mq notices the pending automerge and
enqueues the PR. For the PR at the head of the queue it creates a temporary
branch (`gitea-mq/<pr>`) that merges the PR into the current target branch, and
CI runs there. If all required checks pass, gitea-mq marks its own status
`success` and the forge's automerge completes the merge. If a check fails or
times out, gitea-mq withdraws the merge intent (cancels automerge, removes
the merge label) and posts a comment saying what went wrong.

Alternatively, adding the merge label (default `merge-queue`, see
`GITEA_MQ_MERGE_LABEL`) to a PR enqueues it without automerge. On GitHub this
is the way to merge [stacked pull requests](https://gh.io/stacks), which have
no auto-merge: label the topmost PR you want to merge, gitea-mq tests the
whole stack against the stack's base branch and, on green, marks all stack
heads as passed and performs the atomic stack merge itself. Stacked PRs
without the label get a pending `gitea-mq` status hinting at this workflow.
Removing the label removes the PR from the queue.

By default one PR is tested at a time per target branch (see Batching below
for testing several at once). PRs targeting different
branches get independent queues. If the head-of-queue PR already contains the
current target tip, the merge-branch run is skipped and the PR's own green CI
is accepted, since the merged tree would be identical (disable with
`GITEA_MQ_SKIP_QUEUE_IF_UP_TO_DATE=false` if your `gitea-mq/*` CI runs a
larger suite than PR CI).

## Requirements

- Gitea >= 1.22 and/or a GitHub App. With Gitea >= 1.24 (or a Forgejo release
  based on it) CI results arrive via the commit-status webhook and idle repos
  are only reconciled every `GITEA_MQ_IDLE_POLL_INTERVAL`; older versions fall
  back to polling every `GITEA_MQ_POLL_INTERVAL`.
- PostgreSQL
- For Gitea/Forgejo: an API token with `repository` and `issue` read/write scopes
  (the PR timeline and comments are served via the issue API)

## Configuration

gitea-mq can manage Gitea repos, GitHub repos, or both from one process. At
least one backend must be configured. All configuration is via environment
variables.

Every secret marked `/ _FILE` in the table below can be read from a file
instead by appending `_FILE` to the variable name, e.g.
`GITEA_MQ_GITEA_TOKEN_FILE=/run/secrets/gitea-token`. This keeps the value out
of the process environment, where `docker inspect`, `/proc/<pid>/environ` and
child processes can see it. Trailing newlines in the file are ignored (the PEM
key is read verbatim). If both forms are set, the plain variable wins.

| Variable | Required | Default | Description |
|---|---|---|---|
| `GITEA_MQ_GITEA_URL` | gitea | - | Gitea instance URL. Setting this enables the Gitea backend. |
| `GITEA_MQ_GITEA_TOKEN` / `_FILE` | gitea | - | API token with `repository` and `issue` read/write scopes, or path to a file containing it |
| `GITEA_MQ_REPOS` | no | - | Comma-separated `owner/repo` list of Gitea repos (optional when `GITEA_MQ_TOPIC` is set) |
| `GITEA_MQ_TOPIC` | no | - | Discover Gitea repos by topic instead of (or in addition to) a static list |
| `GITEA_MQ_WEBHOOK_SECRET` / `_FILE` | gitea | - | Shared secret for the Gitea webhook HMAC, or path to a file containing it |
| `GITEA_MQ_GITHUB_APP_ID` | github | - | GitHub App ID. Setting this enables the GitHub backend. |
| `GITEA_MQ_GITHUB_PRIVATE_KEY` / `_FILE` | github | - | PEM-encoded App private key, or path to a file containing it |
| `GITEA_MQ_GITHUB_WEBHOOK_SECRET` / `_FILE` | github | - | Webhook secret configured on the GitHub App, or path to a file containing it |
| `GITEA_MQ_GITHUB_REPOS` | no | - | Comma-separated `owner/repo` list of GitHub repos to manage in addition to all repos the App is installed on |
| `GITEA_MQ_GITHUB_POLL_INTERVAL` | no | `GITEA_MQ_POLL_INTERVAL` | Override the reconcile poll interval for GitHub (its rate limit is much higher) |
| `GITEA_MQ_DATABASE_URL` / `_FILE` | yes | - | PostgreSQL connection string, or path to a file containing it |
| `GITEA_MQ_LISTEN_ADDR` | no | `:8080` | HTTP listen address |
| `GITEA_MQ_WEBHOOK_PATH` | no | `/webhook` | Legacy alias for the Gitea webhook endpoint (the canonical path is `/webhook/gitea`) |
| `GITEA_MQ_EXTERNAL_URL` | yes | - | URL where Gitea can reach this service (used for webhook auto-setup and commit status target URLs) |
| `GITEA_MQ_POLL_INTERVAL` | no | `30s` | Reconcile poll interval for repos with active queue work |
| `GITEA_MQ_IDLE_POLL_INTERVAL` | no | `15m` | Reconcile poll interval for idle repos (no active queue entries). Only used on forges that deliver CI status via webhooks (GitHub, Gitea >= 1.24); there idle repos are driven by webhooks and this periodic poll is just a safety net for deliveries missed while the service was down. Keeping it long bounds forge API usage to the number of repos with live queues rather than all managed repos |
| `GITEA_MQ_CHECK_TIMEOUT` | no | `1h` | Timeout for required checks |
| `GITEA_MQ_TARGET_BRANCHES` | no | empty (all) | Comma-separated exact branch names eligible for new queue admission; both PR base and resolved queue target must match. Set `main` for a main-only landing gate. |
| `GITEA_MQ_SKIP_QUEUE_IF_UP_TO_DATE` | no | `true` | Skip the merge-branch CI run when a PR is already rebased onto the target branch tip (its own green CI already covers the merged tree) |
| `GITEA_MQ_REQUIRED_CHECKS` | no | - | Fallback required CI contexts when branch protection has none (comma-separated) |
| `GITEA_MQ_PR_REQUIRED_CHECKS` | no | unset | Checks the PR head must pass before it is queued, instead of protection / `GITEA_MQ_REQUIRED_CHECKS`. Set to an empty value to not gate the PR head on CI. See [CI configuration](#ci-configuration) |
| `GITEA_MQ_MERGE_REQUIRED_CHECKS` | no | unset | Checks required on `gitea-mq/*` merge and batch branches, instead of protection / `GITEA_MQ_REQUIRED_CHECKS` |
| `GITEA_MQ_REQUIRED_CHECKS_SOURCE` | no | `protection` | `protection`: read required checks from branch protection, falling back to `GITEA_MQ_REQUIRED_CHECKS`. `config`: use `GITEA_MQ_REQUIRED_CHECKS` only and never read branch protection (see [Running without admin rights](#running-without-admin-rights)) |
| `GITEA_MQ_AUTO_SETUP` | no | `true` | Configure branch protection and webhooks on startup (see [Auto-setup](#auto-setup)) |
| `GITEA_MQ_MERGE_LABEL` | no | `merge-queue` | Label that enqueues a PR (stack-aware on GitHub); set to `none` to disable |
| `GITEA_MQ_REQUIRE_APPROVAL` | no | `false` | Keep PRs out of the queue until approved, and re-check right before landing (see [Approvals](#approvals)) |
| `GITEA_MQ_REQUIRED_APPROVALS` | no | `0` | Approvals needed. `0` = take it from branch protection when readable, else 1 |
| `GITEA_MQ_REQUIRED_DISTINCT_REVIEWERS` | no | `0` | Minimum approvals from distinct reviewer accounts; raises the count above when higher |
| `GITEA_MQ_ENQUEUE_ON_APPROVAL` | no | `false` | Approval counts as merge intent: an approved PR with green CI is queued without automerge or the merge label. Implies `GITEA_MQ_REQUIRE_APPROVAL` |
| `GITEA_MQ_PRIORITY_LABEL` | no | - | PRs carrying this label are tested ahead of unlabelled ones, without interrupting running tests |
| `GITEA_MQ_LABEL_PREFIX` | no | `mq/` | Prefix of the queue state labels set on PRs (see [Queue labels](#queue-labels)); set to `none` to disable |
| `GITEA_MQ_BATCH_MAX` | no | `1` | Max PRs tested together as one batch. `1` = batching off (legacy behaviour). `0` = everything currently queued |
| `GITEA_MQ_BISECT_MAX_STEPS` | no | `0` | Cap on CI builds spent bisecting one batch. `0` = unlimited |
| `GITEA_MQ_REFRESH_INTERVAL` | no | `10s` | Dashboard auto-refresh interval |
| `GITEA_MQ_DISCOVERY_INTERVAL` | no | `5m` | How often to re-scan Gitea topics and GitHub installations |
| `GITEA_MQ_CACHE_DIR` | no | `$XDG_CACHE_HOME/gitea-mq` | Directory for persistent bare git clones used for merge operations; unused repos are removed after 30 days |
| `GITEA_MQ_LOG_LEVEL` | no | `info` | Log level: debug, info, warn, error |
| `GITEA_MQ_METRICS` | no | `false` | Serve in-memory event counters in Prometheus format at `/metrics` (see [Queue events](#queue-events)) |
| `GITEA_MQ_NOTIFY_URL` | no | - | POST every queue event, signed, to this URL (see [Event notifications](#event-notifications)) |
| `GITEA_MQ_NOTIFY_SECRET` / `_FILE` | with notify URL | - | HMAC secret for `X-Gitea-MQ-Signature` |
| `GITEA_MQ_NOTIFY_EVENTS` | no | all | Comma-separated event kinds to deliver |
| `GITEA_MQ_FAILURE_DETAILS` | no | `false` | On ejection for a failed check, post the failing checks and (Gitea Actions) the job's log tail to the PR and the event (see [Failure details](#failure-details)) |
| `GITEA_MQ_FAILURE_LOG_LINES` | no | `30` | Log lines per failing job; `0` = checks only, no logs |
| `GITEA_MQ_EVENT_RETENTION` | no | `2160h` | How long queue events are kept in PostgreSQL (90 days) |

## Batching (bors-style)

With `GITEA_MQ_BATCH_MAX` ≠ 1, gitea-mq tests up to N queued PRs at once on a
single `gitea-mq/batch/<id>` branch. On green it **fast-forwards the target
branch itself** to the tested SHA (the merged tree is exactly what CI saw). On
red it bisects: split, retest the first half from the current target tip, land
passing halves immediately, eject the failing singleton, then continue with the
pending halves.

Trade-offs and prerequisites:

- gitea-mq must be allowed to push to the protected target branch.
  - **GitHub**: the App is already a bypass actor on the `gitea-mq` ruleset it
    creates; no extra setup.
  - **Gitea**: add the API token's user to the branch protection's *push
    whitelist*. If you forget, every PR in the batch is removed with a comment
    naming the branch and the user to add.
- Batched PRs land as **merge commits** regardless of the repo's configured
  merge style. A single PR that already contains the target tip is landed
  by fast-forwarding to its head (no merge commit, no extra CI run). Repos that mandate squash/rebase should leave `BATCH_MAX=1`.
- A semantic conflict between two PRs that bisection puts in different halves
  can land both (each half passes alone). This matches bors-ng; keep
  `BATCH_MAX` modest if it bothers you.
- On Gitea, gitea-mq records each landed PR as merged by the batch SHA
  ("manually merged") right after the fast-forward. This needs the
  repository setting *Allow manual merge* (`allow_manual_merge`) and the
  token's user being allowed to merge under branch protection. Without it, or
  if the call fails, gitea-mq falls back to waiting ~10s for the forge to
  detect the merge and otherwise closes the PR with a "Merged as `<sha>`"
  comment.

## Draft PRs

Draft PRs (GitHub drafts, or a Gitea title starting with a work-in-progress
prefix such as `WIP:`) are never queued, even when automerge is armed or the
merge label is set. Their `gitea-mq` status explains why; the merge intent is
kept, so the PR enters the queue once it is marked ready. A queued PR that is
converted back to a draft is removed from the queue, again keeping its intent.

## Approvals

By default gitea-mq does not look at reviews. If branch protection requires
approvals, an armed but unapproved PR is still tested on a merge branch; the
forge then refuses to merge it and it holds the head of the queue until the
success timeout removes it.

`GITEA_MQ_REQUIRE_APPROVAL=true` closes that gap: a PR with merge intent is
only queued once it has enough approvals and no outstanding change request;
until then its `gitea-mq` status reads `Waiting for approval (x/N)`. A queued
PR whose approval is dismissed, or that gets a change request, leaves the
queue with its intent kept and re-enters once approved again. Approval is
checked again right before a batch fast-forwards the target, since that
bypasses the forge's own merge checks.

How many approvals: `GITEA_MQ_REQUIRED_APPROVALS`, or, when it is `0`, the
branch protection's *required approvals* (read only if the token may read
protection and `GITEA_MQ_REQUIRED_CHECKS_SOURCE` is not `config`), with 1 as
the floor. `GITEA_MQ_REQUIRED_DISTINCT_REVIEWERS=N` raises that to at least N
approvals from distinct accounts. Each reviewer account has one verdict, its
latest review; dismissed reviews are ignored. On Gitea only official reviews
(from users allowed to approve) count, and only reviews submitted on the
current head commit are evaluated.

When reviews at the head disagree, the `gitea-mq` status names them, e.g.
`Conflicting reviews at head: #11 approved, #12 changes requested`. This
happens when one account approves and later requests changes (common when
several automated reviewers post as the same user), or when one reviewer
approves while another requests changes. A `review_conflict` event with the
review IDs and accounts is recorded once per head. A new approval at the head
from the account that requested changes clears it; so does a push.

`GITEA_MQ_ENQUEUE_ON_APPROVAL=true` additionally makes approval itself the
merge signal: an approved, non-draft PR with green CI enters the queue without
anyone arming automerge or adding the label, and gitea-mq merges it when the
queue passes. This costs one review lookup per open, unqueued PR per poll.

Rejected heads are held in PostgreSQL after a merge conflict, merge error, or
terminal check failure. Polling, webhook wakes, a service restart, and an old
approval do not re-admit the same PR/head/target combination. This applies to
approval, automerge, and merge-label admission and avoids repeated removal
comments for an unchanged failure. Dependency and approval holds retain their
existing automatic recovery behavior.

Push a corrected head to recover normally. To retry the same head explicitly,
first clear both automerge and the merge label and allow a poll to observe
that cleared state, then schedule automerge or re-add the merge label. This
observation is persisted, so a failed cancellation or an unchanged signal is
not mistaken for a new request. Approval alone never resets the hold. A
changed target-branch tip alone does not reset it either. All normal approval,
dependency and CI checks still apply to a retry.

Removal notification is claimed once per queue attempt before forge calls, so
overlapping failure handlers cannot duplicate it or delete a replacement
attempt. Delivery is best effort: a process crash or failed forge request can
lose that notification; rejection remains durable and must be inspected rather
than retrying the source automatically.

Migration 008 adds the rejection table without altering live queue entries.
Before rolling back to a binary that predates rejection holds, contain known
failing PRs as drafts: the older binary does not enforce the new table. The
down migration removes rejection history; it is not needed for a binary
rollback.

## Issue dependencies (Gitea)

Gitea refuses to merge a PR with an open dependency, but batches land by
fast-forwarding the target, which bypasses that check. gitea-mq enforces it:

- A PR with an open dependency is not enqueued. Its `gitea-mq` status shows
  `Blocked by open dependency #N`; its merge intent is kept, so it is queued
  once the dependency closes.
- A queued PR that gains an open dependency is removed, intent kept.
- Right before a batch lands, members are checked again; those that gained a
  dependency are held back and the rest is re-tested.

The token must be able to read every repository a dependency may point to, as
Gitea omits unreadable ones.

## Queue events

Every queue transition is appended to a `queue_events` table in the same
PostgreSQL database, so queue behaviour can be measured after the fact: how
long PRs wait, why they are ejected, how many builds bisection costs.

| Kind | Meaning |
|---|---|
| `enqueued` | PR entered the queue (`reason`: automerge detection / merge label) |
| `testing_started` | Merge branch built (per PR), or batch build N started (per batch) |
| `passed` | Merge branch green (`reason=up_to_date` when the run was skipped) |
| `merged` | PR landed (`reason=batch` for batches) |
| `ejected` | gitea-mq removed the PR; `reason` is one of `check_failed`, `check_timeout`, `testing_timeout`, `success_timeout`, `merge_conflict`, `merge_error`, `pushed`, `retargeted`, `blocked`, `target_moved`, `push_denied`, `bisect_limit` |
| `removed` | PR left on its own (`reason`: `closed`, `cancelled`) |
| `batch_formed`, `batch_bisected`, `batch_landed` | Batch lifecycle; `detail` carries member counts and the build number |

Read-only JSON endpoints:

- `GET /api/events` with optional `repo=<forge>:<owner>/<name>`, `kind`,
  `since` (duration such as `24h`, or RFC3339), `after=<id>` and `limit`
  (default 100, max 1000). Events come in id order with a `next_after`
  cursor, so a consumer can follow ejections by polling
  `/api/events?kind=ejected&after=<last id>`.
- `GET /api/events/summary?since=24h` counts events per repo, kind and
  reason.
- `GET /api/v1/repos/{forge}/{owner}/{name}/events` is `/api/events` scoped
  to one repo (same parameters).
- `GET /api/v1/repos/{forge}/{owner}/{name}/queue` returns the repo's live
  queue per target branch, in queue order: PR, position, state, head SHA,
  priority, enqueue and testing-start times, merge branch and batch id.

With `GITEA_MQ_METRICS=true`, `/metrics` exposes the same events as the
counter `gitea_mq_events_total{repo,kind,reason}`, plus the histogram
`gitea_mq_queue_duration_seconds{repo,outcome}`: time from enqueue until the
PR was merged, ejected or removed (buckets from one minute to one day).
Terminal events also carry `queued_seconds` in their `detail`. It is kept in memory and
resets on restart; use the API for durable numbers. Events older than
`GITEA_MQ_EVENT_RETENTION` are pruned hourly.

### Event notifications

Set `GITEA_MQ_NOTIFY_URL` to have every recorded event POSTed there as JSON
(the same fields as `/api/events`, including `id`), e.g. so a dispatcher can
tell an author within seconds that their PR was ejected and why:

```json
{"id":1234,"time":"…","repo":"gitea:org/app","kind":"ejected","reason":"check_failed",
 "pr":42,"target_branch":"main","head_sha":"…","detail":{"check":"ci","target_url":"https://ci/…"}}
```

Each request carries `X-Gitea-MQ-Event: <kind>` and
`X-Gitea-MQ-Signature: sha256=<hex HMAC-SHA256 of the body>` keyed with
`GITEA_MQ_NOTIFY_SECRET` (or `_FILE`), which is required.
`GITEA_MQ_NOTIFY_EVENTS=ejected,merged` limits the kinds sent. Delivery is
best-effort: a few retries on errors, 5xx and 429, then the event is
dropped; the events table stays the durable record, so a consumer can
catch up with `/api/events?after=<last id>`.

## Failure details

With `GITEA_MQ_FAILURE_DETAILS=true`, a PR ejected for a failed check gets
one comment listing every failing required check on the tested commit
(context, state, description and link). For Gitea Actions jobs it also
includes the job name and the last `GITEA_MQ_FAILURE_LOG_LINES` lines of the
job log, fetched with `GET /repos/{owner}/{repo}/actions/jobs/{id}/logs`, in
a collapsed block. The same data is stored in the `ejected` event's
`detail.failures`, so `/api/events` and event notifications carry it.

Output is bounded (three jobs, 400 characters per line, 32 KiB per comment)
and redacted: gitea-mq's own token and secrets, `key=value` pairs whose key
looks like a secret, `Authorization`/`Bearer` values, credentials in URLs and
common token formats are replaced by `[REDACTED]`. The token needs read
access to Actions. Logs of external CI systems are not fetched; their checks
are still listed with their links.

## Retrying infrastructure failures

Some failures are not the PR's fault: a runner losing its container socket,
a download timing out, a simulator failing to start. List them as regexes,
one per line (`#` starts a comment), in `GITEA_MQ_INFRA_RETRY_PATTERNS` or a
file named by `GITEA_MQ_INFRA_RETRY_PATTERNS_FILE`:

```text
did not return a process handle
engine\.sock: no such file
curl: \(\d+\)
```

When a required check fails, the patterns are matched against each failing
check's description and its Gitea Actions log tail. On a match gitea-mq
reruns the failed jobs on the same commit with
`POST /repos/{owner}/{repo}/actions/runs/{run}/jobs/{job}/rerun`, keeps the
PR (or batch) in the queue and records an `infra_retry` event with the
pattern, the rerun checks and the attempt number. A failure that matches
nothing ejects or bisects as before.

`GITEA_MQ_INFRA_RETRY_MAX` (default 1) caps reruns per queue entry, or per
batch build. A failure after the last retry ejects normally. A batch that
needed a retry is marked flaky even when it then lands. The token needs write
access to Actions; checks from external CI cannot be rerun and are never
retried.

## Repo selection

There are three ways to tell gitea-mq which repos to manage.

Static list, listing repos explicitly:

```bash
GITEA_MQ_REPOS=org/app,org/lib
```

Topic discovery: tag repos in Gitea with a topic (e.g. `merge-queue`) and let
gitea-mq find them. Any repo the API token has admin access to and that carries
the topic gets picked up. Repos are re-discovered periodically, so adding or
removing the topic on a repo is enough.

```bash
GITEA_MQ_TOPIC=merge-queue
```

Both: combine a static list with topic discovery. Explicitly listed repos are
always managed regardless of their topics.

```bash
GITEA_MQ_REPOS=org/critical-service
GITEA_MQ_TOPIC=merge-queue
```

## GitHub setup

gitea-mq talks to GitHub as a [GitHub App](https://docs.github.com/en/apps/creating-github-apps).
Use the [App creation helper](https://mic92.github.io/gitea-mq/) to pre-fill
the registration form, or register one manually with:

- Webhook URL: `${GITEA_MQ_EXTERNAL_URL}/webhook/github`, secret = `GITEA_MQ_GITHUB_WEBHOOK_SECRET`
- Repository permissions: Checks read & write, Commit statuses read, Contents
  read & write, Pull requests read & write, Administration read & write,
  Metadata read
- Subscribed events: `pull_request`, `check_run`, `status`, `installation`,
  `installation_repositories`

Generate a private key, then set `GITEA_MQ_GITHUB_APP_ID` and
`GITEA_MQ_GITHUB_PRIVATE_KEY_FILE`. On startup gitea-mq patches the App's
webhook URL and secret to match `GITEA_MQ_EXTERNAL_URL` /
`GITEA_MQ_GITHUB_WEBHOOK_SECRET`, so you can leave those fields blank when
registering the App. Install the App on the orgs/repos you want managed;
gitea-mq picks up every installation automatically. `GITEA_MQ_GITHUB_REPOS` is
optional and additive: listed repos stay managed even if the installation is
later removed.

## Queue labels

gitea-mq keeps one state label on each PR that has merge intent (auto-merge or
the merge label), so queue state is visible and filterable in the PR list:

| Label | Meaning |
|---|---|
| `mq/waiting` | Merge intent, but not queued yet: own CI not green or blocked by an open dependency |
| `mq/queued` | In the queue, waiting for its turn |
| `mq/testing` | Being tested on a merge branch (`GITEA_MQ_BATCH_MAX=1`) |
| `mq/batch-N` | Member of batch N; the label is deleted when the batch ends |
| `mq/merging` | Queue passed, waiting for the forge to merge |

Labels are created on first use. This needs write access to issues/PRs: the
Gitea token's `issue` scope, or on GitHub an App permission that allows
managing labels (Pull requests or Issues write). If
the forge answers 403, labelling is switched off for that repo and the queue
keeps working. On Gitea/Forgejo the labels are created as exclusive scoped
labels, so each transition is a single API call.

## Auto-setup

On startup, gitea-mq configures each managed repository:

- Gitea: adds `gitea-mq` as a required status check to all existing branch
  protection rules and creates a webhook pointed at the service for commit
  statuses and pull request events (opened/closed, label, push, review). A
  hook that already exists with that URL is left untouched; add the pull
  request events to it by hand to get immediate reconciles on label changes
  and pushes instead of waiting for the next poll. Events gitea-mq does not
  use are acknowledged with `200`.
- GitHub: enables `allow_auto_merge` and creates a `gitea-mq` repository
  ruleset that requires the `gitea-mq` check on the default branch (the App and
  repo admins are bypass actors). Add further target branches to the ruleset's
  include list if you queue PRs against more than the default branch.

If the GitHub App lacks the Administration permission, auto-setup is skipped
with a warning and the queue still runs against whatever the operator
pre-configured.

Set `GITEA_MQ_AUTO_SETUP=false` to leave both untouched, e.g. when branch
protection and hooks are managed as code.

### Running without admin rights

Gitea only lets repository admins read or edit branch protection. Turning
auto-setup off removes the edits, but gitea-mq also reads branch protection on
every queue step to learn the required checks. To run with a token that only
has write access to the repositories, set both:

```bash
GITEA_MQ_AUTO_SETUP=false
GITEA_MQ_REQUIRED_CHECKS_SOURCE=config
GITEA_MQ_REQUIRED_CHECKS=ci/build,lint
```

Then do by hand what auto-setup would do: add `gitea-mq` to the branch
protection's required checks and create a webhook to
`${GITEA_MQ_EXTERNAL_URL}/webhook/gitea` for status (and pull request) events.
Topic discovery then picks up repos the token can push to instead of only
repos it administers. Keep `GITEA_MQ_REQUIRED_CHECKS` in sync with branch
protection: a check listed only in protection is not waited for on the merge
branch.

`GITEA_MQ_EXTERNAL_URL` is the externally reachable URL of gitea-mq itself
(e.g. `https://mq.example.com`), not the Gitea URL. It is used for webhook
auto-setup and as the target URL in commit statuses, which links to the
dashboard.

## Hiding merge branches from git clients

gitea-mq creates temporary branches under `gitea-mq/*` for CI testing. By
default these are visible to git clients and will be picked up by `git fetch`.
You can configure Gitea's git to hide them from the ref advertisement.

On NixOS, when `services.gitea` is enabled on the same host, the module
configures `uploadpack.hideRefs` for you. To turn that off:

```nix
services.gitea-mq.hideRefFromClients = false;
```

Elsewhere, run this as the Gitea system user (the user that owns the Gitea data
directory):

```bash
git config --global uploadpack.hideRefs refs/heads/gitea-mq/
```

This hides the branches from `git fetch` and `git ls-remote` but does not
affect the Gitea web UI, API, or webhook-driven CI. CI systems that need to
check out merge branches can still fetch them with an explicit refspec, e.g.
`git fetch origin gitea-mq/123`.

## CI configuration

Your CI must run on `gitea-mq/*` branches. For example, in a Woodpecker/Drone
pipeline:

```yaml
when:
  branch:
    - main
    - gitea-mq/*
```

gitea-mq needs to know which CI checks must pass on the merge branch before it
allows a merge. It resolves this in order:

1. Branch protection. If the target branch has protection rules with required
   status checks, those are used (excluding `gitea-mq` itself, to avoid a
   circular dependency).
2. `GITEA_MQ_REQUIRED_CHECKS`. If branch protection has no required status
   checks, or there is no branch protection at all, this comma-separated list
   is used as a fallback (e.g. `ci/woodpecker,lint`).
3. Any single success. If neither is configured, any single passing commit
   status on the merge branch is enough.

By default that one list gates two different commits: the PR head (before a
PR is queued) and the merge branch. That breaks when a required check cannot
report on both. Gitea/Forgejo Actions name statuses after the triggering
event, e.g. `ci / build (pull_request)` on the PR and `ci / build (push)` on
`gitea-mq/42`, so a merge branch never receives the `(pull_request)` context
and every run waits until `GITEA_MQ_CHECK_TIMEOUT`. Set the two lists
separately instead of relying on a glob:

```bash
GITEA_MQ_PR_REQUIRED_CHECKS="ci / build (pull_request)"
GITEA_MQ_MERGE_REQUIRED_CHECKS="ci / build (push)"
```

Each list, when set, replaces protection and `GITEA_MQ_REQUIRED_CHECKS` for
its commit. An empty `GITEA_MQ_PR_REQUIRED_CHECKS=` means the PR head is not
gated on CI and the merge-branch run is the only CI gate. On startup gitea-mq
warns about any `(pull_request…)` context that a merge branch would wait for,
whether it comes from branch protection or the configured lists.

## Dashboard

A small web dashboard shows queue status across all managed repos, lets you
drill into individual repos to see queued PRs, and inspect check results per
PR. It auto-refreshes without JavaScript.

For monitoring there are two endpoints:

- `/healthz` (liveness): `200 ok` whenever the process serves HTTP.
- `/readyz` (readiness): `200` only when PostgreSQL answers a ping and every
  configured forge accepts gitea-mq's credentials (Gitea: `GET /user` with the
  token; GitHub: `GET /app` with the App JWT), `503` otherwise. The JSON body
  names each check, e.g. `{"status":"unavailable","checks":{"database":"ok","gitea":"401 ..."}}`.
  Results are cached for 10s so frequent probes do not add forge traffic.

Repo and PR pages live under `/repo/{forge}/{owner}/{name}` (e.g.
`/repo/github/org/app/pr/42`). Paths without the forge segment resolve as Gitea
for compatibility with links posted by older versions.

## Container

A multi-arch (amd64/arm64) image is published to
`ghcr.io/mic92/gitea-mq` (`:main`, `:vX.Y.Z`). It contains gitea-mq, git and
CA certificates; configuration is via the environment variables above. It needs
a PostgreSQL database; the bare-clone cache lives in `/var/cache/gitea-mq`.

```yaml
services:
  db:
    image: postgres:17-alpine
    environment:
      POSTGRES_USER: gitea-mq
      POSTGRES_PASSWORD: changeme
      POSTGRES_DB: gitea-mq
    volumes: [db:/var/lib/postgresql/data]
  gitea-mq:
    image: ghcr.io/mic92/gitea-mq:main
    ports: ["8080:8080"]
    environment:
      GITEA_MQ_DATABASE_URL: postgres://gitea-mq:changeme@db/gitea-mq?sslmode=disable
      GITEA_MQ_EXTERNAL_URL: https://mq.example.com
      GITEA_MQ_GITEA_URL: https://gitea.example.com
      GITEA_MQ_GITEA_TOKEN_FILE: /run/secrets/gitea_token
      GITEA_MQ_WEBHOOK_SECRET_FILE: /run/secrets/webhook_secret
      GITEA_MQ_TOPIC: merge-queue
    secrets: [gitea_token, webhook_secret]
    volumes: [cache:/var/cache/gitea-mq]
secrets:
  gitea_token:
    file: ./secrets/gitea_token
  webhook_secret:
    file: ./secrets/webhook_secret
volumes:
  db:
  cache:
```

Build it yourself with `nix build .#gitea-mq-docker` (produces an OCI archive;
load with `regctl image import` or `skopeo copy oci-archive:result docker-daemon:gitea-mq:latest`).

## NixOS module

```nix
{
  inputs.gitea-mq.url = "github:Mic92/gitea-mq";

  # In your NixOS configuration:
  imports = [ inputs.gitea-mq.nixosModules.default ];

  services.gitea-mq = {
    enable = true;
    giteaUrl = "https://gitea.example.com";
    giteaTokenFile = "/run/secrets/gitea-mq-token";  # file containing the API token
    repos = [ "org/app" "org/lib" ];
    databaseUrl = "postgres:///gitea-mq?host=/run/postgresql";
    webhookSecretFile = "/run/secrets/gitea-mq-webhook-secret";
    externalUrl = "https://mq.example.com";
  };
}
```

Real-world deployments:

- Gitea backend: [clan-infra](https://git.clan.lol/clan/clan-infra/src/branch/main/modules/web01/gitea-mq.nix), [dashboard](https://mq.clan.lol)
- GitHub backend: [Mic92/dotfiles](https://github.com/Mic92/dotfiles/blob/main/machines/eve/modules/gitea-mq.nix), [dashboard](https://mq.thalheim.io)

### NixOS module options

| Option | Type | Default | Description |
|---|---|---|---|
| `enable` | bool | `false` | Enable the service |
| `package` | package | `pkgs.gitea-mq` | Package to use |
| `giteaUrl` | string or null | `null` | Gitea instance URL; enables the Gitea backend |
| `giteaTokenFile` | path | - | File containing the Gitea API token |
| `webhookSecretFile` | path | - | File containing the Gitea webhook secret |
| `github.appId` | int or null | `null` | GitHub App ID; enables the GitHub backend |
| `github.privateKeyFile` | path | - | File containing the GitHub App private key (PEM) |
| `github.webhookSecretFile` | path | - | File containing the GitHub App webhook secret |
| `github.repos` | list of strings | `[]` | GitHub repos in addition to all installations |
| `github.pollInterval` | string or null | `null` | Override poll interval for GitHub |
| `repos` | list of strings | `[]` | Repos to manage (`owner/name`); optional when `topic` is set |
| `topic` | string or null | `null` | Discover repos by Gitea topic |
| `databaseUrl` | string | `postgres:///gitea-mq?host=/run/postgresql` | PostgreSQL connection string |
| `listenAddr` | string | `:8080` | HTTP listen address |
| `webhookPath` | string | `/webhook` | Webhook endpoint path |
| `externalUrl` | string | - | URL where Gitea can reach this service (for webhook auto-setup and commit status links) |
| `pollInterval` | string | `30s` | Poll interval |
| `idlePollInterval` | string | `15m` | Poll interval for repos without queued entries |
| `priorityLabel` | string | `""` | Label that moves a PR ahead in the queue |
| `checkTimeout` | string | `1h` | Check timeout |
| `skipQueueIfUpToDate` | bool | `true` | Skip merge-branch CI for PRs already rebased onto the target tip |
| `requiredChecks` | list of strings | `[]` | Fallback required CI contexts when branch protection has none |
| `prRequiredChecks` | null or list of strings | `null` | Checks the PR head must pass before queueing; `[]` = no PR-head gate |
| `mergeRequiredChecks` | null or list of strings | `null` | Checks required on merge and batch branches |
| `requiredChecksSource` | enum | `"protection"` | `"config"` uses `requiredChecks` only and never reads branch protection |
| `autoSetup` | bool | `true` | Configure branch protection and webhooks on startup |
| `refreshInterval` | string | `10s` | Dashboard refresh interval |
| `discoveryInterval` | string | `5m` | How often to re-discover repos by topic |
| `logLevel` | enum | `info` | Log level |
| `metrics` | bool | `false` | Serve in-memory event counters at `/metrics` |
| `eventRetention` | string | `2160h` | How long queue events are kept |

## Development

```bash
# Enter dev shell
nix develop

# Run tests (requires PostgreSQL, provided by the dev shell)
go test ./...

# Build
nix build

# Run NixOS integration test
nix build .#checks.x86_64-linux.nixos-test

# Format
nix fmt
```

## License

[MIT](LICENSE)

### Restricting admission targets

Set `GITEA_MQ_TARGET_BRANCHES=main` when the downstream landing gate accepts
only PRs into main. Approval, automerge and label admission all check the PR
base and resolved queue target before reading CI or creating a merge branch.
An excluded PR keeps its merge intent and receives a deduplicated waiting
status; retarget it through the normal dependency and review workflow.
An unset or empty value preserves multi-branch behavior. Matching is exact,
not a glob. This is an admission filter, not branch protection: existing queue
entries still obey their original gates. Drain or contain incompatible entries
before tightening the configured targets. All approval, dependency and required
CI checks remain in place.
