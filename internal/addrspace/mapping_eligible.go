package addrspace

// mappingEligibleForStringBody reports whether m is a mapping that
// ProcessReader may serve string literal bytes from. Only read-only
// mappings (rodata, text segments) are eligible. Writable mappings
// (heap, stack, anonymous RW) are excluded because they represent live
// mutable state rather than the stop-the-world snapshot, and heap/stack
// string bytes are already covered by heap dump object contents.
func mappingEligibleForStringBody(m Mapping) bool {
	return m.Read && !m.Write
}
