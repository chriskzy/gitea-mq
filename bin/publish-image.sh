#!/usr/bin/env bash
set -euo pipefail

verify_revision() {
  local image=$1 revision=$2 platform actual
  for platform in linux/amd64 linux/arm64; do
    actual=$(regctl image inspect "$image" --platform "$platform" \
      --format '{{.OS}}/{{.Architecture}} {{index .Config.Labels "org.opencontainers.image.revision"}}')
    if [[ "$actual" != "$platform $revision" ]]; then
      echo "Expected $platform image revision $revision, got '$actual' from $image" >&2
      return 1
    fi
    echo "$platform image revision $revision"
  done
}

image=$1
revision=$2
expected_digest=$(regctl image digest --require-list "$image")
verify_revision "$image" "$revision"

# Omitting the repository verifies the prepared image without publishing.
if [[ $# -eq 2 ]]; then
  exit 0
fi

repository=$3
sha_tag="$repository:sha-${revision:0:7}"
regctl image copy "$image" "$sha_tag"
registry_digest=$(regctl image digest --require-list "$sha_tag")
if [[ "$registry_digest" != "$expected_digest" ]]; then
  echo "Expected image digest $expected_digest, got $registry_digest from $sha_tag" >&2
  exit 1
fi

verified_image="$repository@$registry_digest"
verify_revision "$verified_image" "$revision"
while IFS= read -r tag; do
  if [[ -n "$tag" && "$tag" != "$sha_tag" ]]; then
    regctl image copy "$verified_image" "$tag"
  fi
done
