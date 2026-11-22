---
name: gitea-mq-verify-oci-publish
description: Verify moving-tag updates when changing the OCI publisher, digest checks, or platform revision checks.
---

Run from the fork root with Python 3.14.7.

```sh
rtk bypass uv run --python 3.14.7 python verification/oci-publish/check.py
```

The check runs `bin/publish-image.sh` across all 32 combinations of
SHA-tag upload, digest readback, digest match, and amd64 and arm64 revision
validity. Only the all-success case may copy to a moving tag, and every
such copy must use the verified digest.

Coverage is the Bash publisher at Git blob `81101033561e83eba60ef3fc4634177da3f9aa9b`.
The fixture supplies registry outcomes at the regctl boundary. Registry
transport, concurrent publishers, and partial success during alias updates
require separate integration checks.

If the check fails, use the printed outcome and copy list to locate the
first incorrect promotion. For an intentional contract change, update the
cases, verify that removing the relevant guard fails, and refresh the blob
record after the corrected run passes.
