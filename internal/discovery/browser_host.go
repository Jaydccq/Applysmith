package discovery

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Memory pressure verdicts.
const (
	PressureNormal   = "normal"
	PressureWarn     = "warn"
	PressureCritical = "critical"
)

// Available memory below these bounds is critical and warn when the operating
// system gives no verdict of its own.
const (
	criticalFreeMiB = 200
	warnFreeMiB     = 512
	loadPerCoreWarn = 4.0
)

// HostSample is what a browser scan measures before it opens a tab, and what
// its run record keeps.
type HostSample struct {
	Verdict string  `json:"verdict"`
	FreeMiB int     `json:"free_mib"`
	Load1   float64 `json:"load1"`
	CPUs    int     `json:"cpus"`
	// OSVerdict is true when the verdict is the system's own and false when it
	// was derived from available memory.
	OSVerdict bool `json:"os_verdict"`
}

// HostPressure decides the verdict from the system's level (1 normal, 2 warn,
// 4 critical; 0 when none could be read) and the available memory.
func HostPressure(osLevel, freeMiB int) (verdict string, fromOS bool) {
	switch osLevel {
	case 1:
		return PressureNormal, true
	case 2:
		return PressureWarn, true
	case 4:
		return PressureCritical, true
	}
	switch {
	case freeMiB < criticalFreeMiB:
		return PressureCritical, false
	case freeMiB < warnFreeMiB:
		return PressureWarn, false
	}
	return PressureNormal, false
}

// Degrade reports whether a scan on this host runs with detail concurrency 1:
// on a warning, or on a one-minute load of four or more per core. Load never
// refuses a run; only a critical verdict does.
func (h HostSample) Degrade() bool {
	return h.Verdict == PressureWarn || (h.CPUs > 0 && h.Load1/float64(h.CPUs) >= loadPerCoreWarn)
}

// MeasureHost reads the host. Anything it cannot read is left at its zero
// value, which reads as plenty of memory and no load.
func MeasureHost(ctx context.Context) HostSample {
	h := HostSample{CPUs: runtime.NumCPU()}
	level, free := 0, -1
	switch runtime.GOOS {
	case "darwin":
		level = sysctlInt(ctx, "kern.memorystatus_vm_pressure_level")
		free = darwinFreeMiB(ctx)
		h.Load1 = firstFloat(sysctlText(ctx, "vm.loadavg"))
	case "linux":
		free = linuxAvailableMiB()
		if b, err := os.ReadFile("/proc/loadavg"); err == nil {
			h.Load1 = firstFloat(string(b))
		}
	}
	if free < 0 {
		free = 1 << 20
	}
	h.FreeMiB = free
	h.Verdict, h.OSVerdict = HostPressure(level, free)
	return h
}

func sysctlText(ctx context.Context, name string) string {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sysctl", "-n", name).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func sysctlInt(ctx context.Context, name string) int {
	n, _ := strconv.Atoi(sysctlText(ctx, name))
	return n
}

// firstFloat reads the first number of "{ 1.23 1.50 1.60 }" or "1.23 1.50".
func firstFloat(s string) float64 {
	for _, f := range strings.Fields(strings.Trim(s, "{} \n")) {
		if v, err := strconv.ParseFloat(f, 64); err == nil {
			return v
		}
	}
	return 0
}

// darwinFreeMiB is free plus inactive, speculative and purgeable pages, which
// the system hands back on demand.
func darwinFreeMiB(ctx context.Context) int {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "vm_stat").Output()
	if err != nil {
		return -1
	}
	page, pages := 16384, 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "Mach Virtual Memory Statistics") {
			if i := strings.Index(line, "page size of "); i >= 0 {
				if n, err := strconv.Atoi(strings.Fields(line[i+len("page size of "):])[0]); err == nil {
					page = n
				}
			}
			continue
		}
		name, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(name) {
		case "Pages free", "Pages inactive", "Pages speculative", "Pages purgeable":
			n, _ := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(val), ".")))
			pages += n
		}
	}
	return pages * page >> 20
}

func linuxAvailableMiB() int {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return -1
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "MemAvailable:"); ok {
			kb, _ := strconv.Atoi(strings.Fields(rest)[0])
			return kb >> 10
		}
	}
	return -1
}

func (h HostSample) String() string {
	basis := "available memory"
	if h.OSVerdict {
		basis = "system level"
	}
	return fmt.Sprintf("%s (%s): %d MiB free, load %.1f on %d cores", h.Verdict, basis, h.FreeMiB, h.Load1, h.CPUs)
}
