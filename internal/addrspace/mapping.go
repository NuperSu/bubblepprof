package addrspace

// Mapping describes a contiguous virtual address range with its
// permission flags and optional backing path. On Linux this maps
// directly to a /proc/<pid>/maps entry; on other platforms it is
// used as a generic segment descriptor where applicable.
type Mapping struct {
	Start uint64
	End   uint64
	Read  bool
	Write bool
	Exec  bool
	Path  string
}
