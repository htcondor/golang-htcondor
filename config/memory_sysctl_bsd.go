//go:build darwin || freebsd

package config

import (
	"runtime"

	"golang.org/x/sys/unix"
)

// sysctlMemoryBytes reads physical memory from sysctl directly, rather than
// running the sysctl(8) binary. Forking from a library on the config path is
// the problem: the fork was unbounded (exec.CommandContext with
// context.Background() carries no deadline), so a slow or blocked sysctl
// stalled config load with nothing to time it out.
//
// The key differs by platform. hw.memsize is Darwin's; FreeBSD spells it
// hw.physmem, and asking FreeBSD for hw.memsize simply failed, which is why
// it has always reported no memory there.
func sysctlMemoryBytes() (uint64, bool) {
	key := "hw.memsize"
	if runtime.GOOS == "freebsd" {
		key = "hw.physmem"
	}
	n, err := unix.SysctlUint64(key)
	if err != nil || n == 0 {
		return 0, false
	}
	return n, true
}
