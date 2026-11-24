package config

import (
	"runtime"
	"testing"
)

// The value has to be real, not merely non-zero: the previous implementation
// shelled out to sysctl(8) with no deadline, and a wrong or absent answer was
// indistinguishable from a hang that had been given up on.
func TestSysctlMemoryBytesOnBSD(t *testing.T) {
	n, ok := sysctlMemoryBytes()

	if runtime.GOOS != "darwin" && runtime.GOOS != "freebsd" {
		if ok {
			t.Errorf("sysctlMemoryBytes answered on %s, where there is no sysctl to ask", runtime.GOOS)
		}
		return
	}

	if !ok {
		t.Fatalf("sysctlMemoryBytes found no physical memory on %s", runtime.GOOS)
	}
	// A machine running this test has at least 256 MiB and less than 1 PiB.
	// The point is to catch a unit mix-up (KiB read as bytes, or the
	// reverse), which a bare non-zero check would pass.
	const (
		lowest  = 256 << 20
		highest = 1 << 50
	)
	if n < lowest || n > highest {
		t.Errorf("physical memory = %d bytes, outside [%d, %d]; a unit error reads like this", n, lowest, highest)
	}
}

// detectMemorySysctl converts to MiB, and the conversion is where a factor of
// 1024 goes missing.
func TestDetectMemorySysctlReportsMiB(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "freebsd" {
		t.Skipf("no sysctl on %s", runtime.GOOS)
	}
	bytes, ok := sysctlMemoryBytes()
	if !ok {
		t.Fatal("sysctlMemoryBytes found no physical memory")
	}
	want := bytes / (1024 * 1024)
	if got := detectMemorySysctl(); uint64(got) != want { //nolint:gosec // G115: a test machine's MiB count fits an int
		t.Errorf("detectMemorySysctl() = %d MiB, want %d", got, want)
	}
}
