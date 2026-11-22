#!/usr/bin/env python3
"""Exercise the real publisher with every combination of registry outcomes."""

import itertools
import json
import os
from pathlib import Path
import shlex
import subprocess
import sys
import tempfile


SOURCE = "ocidir://local:gitea-mq"
REPO = "registry.example/gitea-mq"
REVISION = "a" * 40
DIGEST = "sha256:" + "b" * 64
SHA_TAG = f"{REPO}:sha-{REVISION[:7]}"
ALIASES = [f"{REPO}:main", f"{REPO}:v1"]


def registry():
    upload, readback, matching_digest, amd64, arm64 = json.loads(os.environ["PROBE"])
    args = sys.argv[1:]
    with open(os.environ["REGCTL_LOG"], "a", encoding="utf-8") as log:
        print(json.dumps(args), file=log)
    if args[:2] == ["image", "digest"]:
        if args[-1] == SOURCE:
            print(DIGEST)
        elif args[-1] == SHA_TAG and readback:
            print(DIGEST if matching_digest else "sha256:" + "c" * 64)
        else:
            sys.exit(1)
    elif args[:2] == ["image", "inspect"]:
        image = args[2]
        platform = args[args.index("--platform") + 1]
        valid_revision = image == SOURCE or (amd64 if platform == "linux/amd64" else arm64)
        print(platform, REVISION if valid_revision else "stale")
    elif args[:2] == ["image", "copy"]:
        if args[-1] == SHA_TAG and not upload:
            sys.exit(1)
    else:
        raise ValueError(args)


def verify(publisher):
    with tempfile.TemporaryDirectory() as directory:
        path = Path(directory)
        executable = path / "regctl"
        executable.write_text(
            f"#!/bin/bash\nexec {shlex.quote(sys.executable)} "
            f"{shlex.quote(str(Path(__file__).resolve()))} --registry \"$@\"\n",
            encoding="utf-8",
        )
        executable.chmod(0o755)
        log = path / "commands"
        tags = "\n".join([ALIASES[0], SHA_TAG, ALIASES[1], ""])
        for states in itertools.product((False, True), repeat=5):
            log.write_text("", encoding="utf-8")
            env = {
                **os.environ,
                "PATH": f"{path}{os.pathsep}{os.environ['PATH']}",
                "PROBE": json.dumps(states),
                "REGCTL_LOG": str(log),
            }
            result = subprocess.run(
                ["bash", publisher, SOURCE, REVISION, REPO],
                input=tags, env=env, text=True, capture_output=True,
            )
            commands = [json.loads(line) for line in log.read_text().splitlines()]
            copies = [args[2:] for args in commands if args[:2] == ["image", "copy"]]
            expected = [[SOURCE, SHA_TAG]]
            if all(states):
                expected.extend([f"{REPO}@{DIGEST}", tag] for tag in ALIASES)
            if (result.returncode == 0) != all(states) or copies != expected:
                raise AssertionError(f"Outcomes {states}: exit {result.returncode}, copies {copies}")
        print("Verified archive publication across 32 registry outcome combinations")


if __name__ == "__main__":
    if len(sys.argv) > 1 and sys.argv[1] == "--registry":
        sys.argv.pop(1)
        registry()
    else:
        verify(sys.argv[1] if len(sys.argv) > 1 else "bin/publish-image.sh")
