---
name: gitea-mq-verify-protected-stack
description: Verify tested-SHA landing and retriable review lookup failures for batched GitHub stacks.
---

With Go 1.27.3 and local PostgreSQL tools on `PATH`, run this from the repository root:

```sh
rtk bypass go test ./internal/batch -run '^TestStackLandingProtectedGates$' -count=1
```

The test exercises `Engine.FormAndBuild`, `Engine.usesNativeStackMerge`,
`Engine.HandleCheck`, and `Engine.HandlePass` in `internal/batch/` with real
PostgreSQL and a mock forge. It checks the default setting and enumerates
every nonempty combination of required approval, explicit merge-branch checks,
and config-only check resolution with native merging opted in. It sends a
stale success and then a success for the tested SHA. Where approval is
required, it checks approval, requested changes, and a failed lookup followed
by recovery. The target may advance only to the tested SHA, and the native
merge API may never be called in these cases. A failed review lookup must
preserve the successful check and the original build for later reconciliation.

Coverage applies to the landing code at `de56109`. For negative controls,
use a Go overlay to return just `supportsStacks` from `usesNativeStackMerge`,
then run a separate overlay that continues on review lookup errors in
`holdUnapproved`. The first must fail the tested-SHA assertion and the second
must fail the review lookup assertion. The check covers one stack entry and
sequential events. GitHub's asynchronous implementation, network behavior,
and concurrent requests are outside its scope. Update the enumerated cases
when protected settings change, then rerun the test and negative controls.
