# PR-head failure diagnosis

Enable `GITEA_MQ_FAILURE_DETAILS=true` to collect read-only CI diagnoses before
queue admission, including open PRs without merge intent. Existing
`GITEA_MQ_FAILURE_LOG_LINES` and secret redaction apply. The repository dashboard
shows matching failures together even when the queue is empty.

`GET /api/v1/repos/{forge}/{owner}/{name}/head-failures` returns `groups`. Each
group contains a stable `fingerprint`, `classification`, failed check, job, test
name and evidence when available, a representative redacted log tail, affected
PR `count`, and `members` with exact `head_sha`, observation time and freshness.
The result is bounded to 1,000 failed heads; `head_limit_reached` reports when
that bound is reached. Use the repository-scoped events endpoint with
`kind=pr_head_failed` for history and cursor-based consumption.

Classifications are advisory observations, not confirmed root causes:

- `source`: explicit assertion evidence appears in the retained output.
- `infrastructure`: output matches an existing
  `GITEA_MQ_INFRA_RETRY_PATTERNS` expression and has no explicit assertion evidence.
- `unknown`: the available output does not establish either category. A test
  failure, nonzero exit code or generic error alone remains unknown.

Explicit assertion evidence wins when infrastructure text also appears.
Fingerprinting keeps check/job/test identifiers and assertion content, while
removing common timestamps, workspace prefixes and parenthesized durations.
Matching fingerprints are evidence for grouping, not proof of a shared cause.
One representative test/assertion is extracted per failing check. Gitea Actions
supports job logs; GitHub currently supplies check descriptions only. Missing
job logs are reported by `log_error`; unavailable evidence remains unknown.

Observation adds at most ten head-state reads per reconcile, rotating through
all open PRs, and at most three job-log fetches per changed head. Each head
retains at most 30 checks and 200 trailing lines per job, with bounded strings.
The existing Gitea log downloader bounds retained log bytes. Reads needed to
resolve required checks and Actions jobs use the existing forge adapters.
With diagnostics enabled and open PRs present, regular polling continues even
when the merge queue is empty. External status/check completion webhooks also
wake the coalesced poller; own queue statuses are ignored.

State persists across restarts. Unchanged failures do not repeat log reads or
events until a 15-minute evidence refresh. That refresh catches same-head
reruns even if polling missed a brief pending state. Unchanged refreshed
evidence does not emit another event. Pushed, closed, retargeted and newly
excluded PRs lose their old snapshots when the next successful open-PR list
arrives; pending or successful checks clear a snapshot when that head is next
observed. The ten-head budget means large repositories can require multiple
polls. Observation times remain visible, and evidence older than 15 minutes or
retained after a failed check read is explicitly marked stale. An unavailable
open-PR list leaves the previous observations visible until polling recovers.

By default this feature sends no PR comments, commit statuses, retries, fixer dispatches
or messages. It changes no approval, queue admission or merge rule. Existing
merge-branch infrastructure retry behavior remains separately configured.
The outbound notifier excludes `pr_head_failed` from its legacy all-events
subscription. An operator must explicitly name this event in
`GITEA_MQ_NOTIFY_EVENTS` to opt into outbound delivery.
