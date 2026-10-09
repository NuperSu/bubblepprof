# Phases 0 and 1 validation

The existing Linux amd64 suite passed before implementation. The sandbox refused
32-bit executables with `signal: bad system call`, and refused local HTTP listeners
in uncached CLI tests. Tests were therefore run outside the sandbox with the normal
Go environment. Linux amd64 and Linux 386 full suites pass after implementation,
including real heap captures, label recovery, bundle analysis, and batch CLI tests.

The primary Windows amd64/386 and macOS amd64/arm64 runtime test jobs remain in
GitHub Actions. They cannot execute on this Linux host; no claim of a local runtime
pass on those systems is made. Their selected Go patch versions and gotip coverage
are unchanged. No OS-specific implementation was added.

Regression coverage includes exact object sets through independent roots, sharing,
cycles, duplicate roots, invalid IDs (including values overflowing signed 32-bit
integers), and existing interior-pointer/malformed-layout tests. Session tests cover
identical labels, AND selectors, unlabeled and system goroutines, no matches, decode
failures, invariant cached sets, cancellation, and repeated Close. Integration tests
compare repeated offline queries with the existing analyzer over the same captured
bundle, query after extracted files are removed, and compare CLI batch responses
with a separately opened session over the same snapshot.
