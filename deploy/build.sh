#!/usr/bin/env bash
#
# Build the three service images here and push them to the registry: the same
# result as the GitHub Actions workflow, from a laptop. For the VPS, which is
# x86, whatever this machine is; Go cross-compiles inside the Dockerfile.
#
#   ./deploy/build.sh              build and push latest + the current commit
#   PUSH=0 ./deploy/build.sh       build only, to check it compiles
#
# Pushing needs a login with a token that can write packages:
#   echo "<token>" | docker login ghcr.io -u michal-pielka --password-stdin

set -euo pipefail

cd "$(dirname "$0")/../backend"

registry=ghcr.io/michal-pielka
sha=$(git rev-parse HEAD)
platform=${PLATFORM:-linux/amd64}

if [[ "${PUSH:-1}" == 1 ]]; then
	out=--push
else
	out=--load
fi

for svc in gateway renderer dispatcher; do
	echo "== $svc"
	docker buildx build \
		--platform "$platform" \
		--file "cmd/$svc/Dockerfile" \
		--tag "$registry/fax-$svc:latest" \
		--tag "$registry/fax-$svc:$sha" \
		$out .
done
