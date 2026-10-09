#!/usr/bin/env bash
# Run from the repository root. Produces local archives; never publishes them.
set -euo pipefail
version=${1:-dev}
output=${2:-dist}
if [[ ! "$version" =~ ^[A-Za-z0-9._+-]+$ ]]; then
  echo 'version must contain only letters, numbers, dots, _, + or -' >&2
  exit 1
fi
mkdir -p "$output"
output=$(cd "$output" && pwd)
scratch=$(mktemp -d "$output/.build-XXXXXXXX")
trap 'rm -rf "$scratch"' EXIT
archives=()
for target in linux/amd64 linux/arm64 linux/386 linux/arm darwin/amd64 darwin/arm64 windows/amd64 windows/arm64 windows/386 freebsd/amd64 freebsd/arm64; do
  os=${target%/*}
  arch=${target#*/}
  namearch=$arch
  binary=bubblepprof
  extension=tar.gz
  if [[ "$arch" == arm ]]; then namearch=armv7; fi
  if [[ "$os" == windows ]]; then binary=bubblepprof.exe; extension=zip; fi
  echo "Building $target"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" GOARM=7 \
    go build -trimpath -ldflags "-s -w -X main.version=$version" \
    -o "$scratch/$binary" ./cmd/bubblepprof
  cp LICENSE README.md "$scratch/"
  cp -R docs "$scratch/"
  archive="bubblepprof_${version}_${os}_${namearch}.${extension}"
  if [[ "$extension" == zip ]]; then
    (cd "$scratch" && zip -q -r "$output/$archive" "$binary" LICENSE README.md docs)
  else
    tar -czf "$output/$archive" -C "$scratch" "$binary" LICENSE README.md docs
  fi
  archives+=("$archive")
  rm "$scratch/$binary"
done
(cd "$output" && sha256sum "${archives[@]}" > SHA256SUMS)
echo "Release archives and SHA256SUMS written to $output"
