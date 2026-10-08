#!/bin/bash

set -euo pipefail

mkdir -p "${ENVTEST_ASSETS}"
export KUBEBUILDER_ASSETS="${ENVTEST_ASSETS}"

if [ -x "${ENVTEST_ASSETS}/kube-apiserver" ]; then
	return 0 2>/dev/null || exit 0
fi

tmp="$(mktemp)"
trap 'rm -f "${tmp}"' EXIT

for attempt in 1 2 3; do
	echo "Downloading ${ENVTEST_ASSET_URL} (attempt ${attempt})"
	if curl -fsSL --retry 3 --retry-delay 2 "${ENVTEST_ASSET_URL}" -o "${tmp}" \
		&& gzip -t "${tmp}" \
		&& tar -xzf "${tmp}" --strip-components=2 -C "${ENVTEST_ASSETS}"; then
		return 0 2>/dev/null || exit 0
	fi
	echo "Downloaded envtest archive is invalid" >&2
	sleep $((attempt * 2))
done

echo "Failed to download a valid envtest archive from ${ENVTEST_ASSET_URL}" >&2
exit 1
