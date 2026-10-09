# Releases and supported platforms

## Install

Build from a tagged module with Go:

```sh
go install github.com/NuperSu/bubblepprof/cmd/bubblepprof@latest
bubblepprof version
```

The `Portable release` GitHub Actions workflow also produces CLI archives,
documentation, the license, and `SHA256SUMS`. A manual workflow run builds
`dev` artifacts; a `v*` tag builds versioned artifacts and prepares a **draft**
GitHub release after tests pass. Review the draft and notes before publishing.
This implementation does not create a version tag or publish a release.

Download the archive for the analysis machine, verify its checksum, and extract
it. The CLI's architecture need not match the captured application's architecture:
the offline parser handles supported 32-bit and 64-bit dump formats.

```sh
sha256sum -c SHA256SUMS --ignore-missing
tar -xzf bubblepprof_VERSION_linux_amd64.tar.gz
./bubblepprof version
```

On Windows, use `Get-FileHash -Algorithm SHA256` to compare the archive against
the checksum file, then `Expand-Archive` for the zip. The extracted executable
is `bubblepprof.exe`. No cgo runtime or external profiling server is required.

## Build local archives

From the repository root, with Go, Bash, tar, zip, and sha256sum available:

```sh
bash scripts/build-releases.sh v0.1.0
# Or use a development identifier without creating a release:
bash scripts/build-releases.sh phase6-dev
```

The script cross-compiles with `CGO_ENABLED=0` and `-trimpath`, embeds the CLI
version, and writes archives/checksums to ignored `dist/`. An optional second
argument changes the output directory. The script never uploads artifacts.
Version identifiers are restricted to letters, digits, dots, _, +, and -.

## Supported platforms

| OS | Release architectures | Runtime validation |
|---|---|---|
| Linux | amd64, arm64, 386, armv7 | existing CI runtime/parser/graph suites; routine local amd64 and 386 |
| macOS | amd64, arm64 | existing CI, including PIE/ASLR runtime integration |
| Windows | amd64, arm64, 386 | existing CI on amd64 and 386; arm64 archive is cross-compiled |
| FreeBSD | amd64, arm64 | best effort; existing FreeBSD CI |

Stable runtime layout families are Go 1.24, 1.25, 1.26, and 1.27,
little-endian pointer widths of 4 or 8 bytes. The CI version matrix selects
`1.24.13`, `1.25.14`, `1.26.9`, and `1.27.2`, plus experimental `gotip`.
Go 1.27.2 has passed local Linux amd64/386 integration suites. Gotip is
experimental; supported devel layouts are revision-sensitive and local gotip
validation is skipped unless requested. Other runtime versions/layouts fail
explicitly rather than silently returning a measurement.

Literal labels need captured read-only memory. Linux, macOS, Windows, and
FreeBSD have capture-side readers. On FreeBSD, literal label recovery needs
procfs at `/proc` or a non-PIE executable for the on-disk ELF fallback. Graph
analysis itself is platform independent. See [limitations](limitations.md).

## CI and release gates

Existing per-OS workflows retain their complete selected Go-version and
architecture matrices. Their full suites include parser/graph tests, runtime
label recovery, real capture/CLI workflows, and platform-specific reader tests.
The release workflow adds native Linux 64/32-bit, macOS Intel/ARM, and Windows
64-bit tests and CLI builds before packaging all targets. It does not replace
or weaken existing CI. Local cross-compilation validates buildability; only
native CI/runtime tests establish behavior on those platforms.
