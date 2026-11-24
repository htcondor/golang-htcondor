//go:build !darwin && !freebsd

package config

// sysctlMemoryBytes reports no answer on platforms with no sysctl to ask;
// detectMemorySyscall only consults it on the BSD-like ones anyway.
func sysctlMemoryBytes() (uint64, bool) { return 0, false }
