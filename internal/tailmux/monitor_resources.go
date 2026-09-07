package tailmux

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

// BoxMemory is a point-in-time view of physical memory. UsedBytes excludes
// memory the operating system reports as readily available for applications.
type BoxMemory struct {
	TotalBytes uint64  `json:"total_bytes"`
	UsedBytes  uint64  `json:"used_bytes"`
	Percent    float64 `json:"percent"`
}

var memoryCommandOutput = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

var monitorExecutable = os.Executable

func memoryResult(total, available uint64) (BoxMemory, error) {
	if total == 0 {
		return BoxMemory{}, fmt.Errorf("memory total is zero")
	}
	if available > total {
		available = total
	}
	used := total - available
	return BoxMemory{TotalBytes: total, UsedBytes: used, Percent: float64(used) * 100 / float64(total)}, nil
}

func parseLinuxMemory(data []byte) (BoxMemory, error) {
	values := map[string]uint64{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		if key != "MemTotal" && key != "MemAvailable" {
			continue
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil || kb > ^uint64(0)/1024 {
			return BoxMemory{}, fmt.Errorf("invalid %s in /proc/meminfo", key)
		}
		values[key] = kb * 1024
	}
	if values["MemTotal"] == 0 {
		return BoxMemory{}, fmt.Errorf("/proc/meminfo has no MemTotal")
	}
	available, ok := values["MemAvailable"]
	if !ok {
		return BoxMemory{}, fmt.Errorf("/proc/meminfo has no MemAvailable")
	}
	return memoryResult(values["MemTotal"], available)
}

func parseMacMemory(totalData, vmData []byte) (BoxMemory, error) {
	total, err := strconv.ParseUint(strings.TrimSpace(string(totalData)), 10, 64)
	if err != nil || total == 0 {
		return BoxMemory{}, fmt.Errorf("invalid hw.memsize")
	}
	pageSize := uint64(4096)
	lines := strings.Split(string(vmData), "\n")
	if len(lines) > 0 {
		const marker = "page size of "
		if i := strings.Index(lines[0], marker); i >= 0 {
			rest := lines[0][i+len(marker):]
			if end := strings.IndexByte(rest, ' '); end >= 0 {
				rest = rest[:end]
			}
			if parsed, e := strconv.ParseUint(rest, 10, 64); e == nil && parsed > 0 {
				pageSize = parsed
			}
		}
	}
	var availablePages uint64
	foundAvailable := false
	for _, line := range lines[1:] {
		key, raw, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		// Purgeable pages can overlap inactive pages, so do not add that
		// counter separately.
		if key != "Pages free" && key != "Pages inactive" && key != "Pages speculative" {
			continue
		}
		raw = strings.TrimSuffix(strings.TrimSpace(raw), ".")
		pages, e := strconv.ParseUint(raw, 10, 64)
		if e != nil || pages > ^uint64(0)-availablePages {
			return BoxMemory{}, fmt.Errorf("invalid %s from vm_stat", key)
		}
		availablePages += pages
		foundAvailable = true
	}
	if !foundAvailable {
		return BoxMemory{}, fmt.Errorf("vm_stat has no available-page counters")
	}
	if availablePages > ^uint64(0)/pageSize {
		return BoxMemory{}, fmt.Errorf("vm_stat available memory overflow")
	}
	return memoryResult(total, availablePages*pageSize)
}

func boundedMemoryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, 15*time.Second)
}

// collectBoxMemory samples the local machine when target is "local", or a
// configured remote Linux box over Tailmux SSH. It never elevates privileges.
func collectBoxMemory(ctx context.Context, dir string, cfg Config, target string) (BoxMemory, error) {
	ctx, cancel := boundedMemoryContext(ctx)
	defer cancel()
	if target == "local" {
		switch runtime.GOOS {
		case "linux":
			data, err := os.ReadFile("/proc/meminfo")
			if err != nil {
				return BoxMemory{}, fmt.Errorf("read local memory: %w", err)
			}
			return parseLinuxMemory(data)
		case "darwin":
			total, err := memoryCommandOutput(ctx, "sysctl", "-n", "hw.memsize")
			if err != nil {
				return BoxMemory{}, fmt.Errorf("read local memory total: %w", err)
			}
			vm, err := memoryCommandOutput(ctx, "vm_stat")
			if err != nil {
				return BoxMemory{}, fmt.Errorf("read local memory use: %w", err)
			}
			return parseMacMemory(total, vm)
		default:
			return BoxMemory{}, fmt.Errorf("local memory sampling is unsupported on %s", runtime.GOOS)
		}
	}

	h, err := cfg.host(target)
	if err != nil {
		return BoxMemory{}, err
	}
	exe, err := monitorExecutable()
	if err != nil {
		return BoxMemory{}, err
	}
	args := append(sshOptions(exe, dir, target, h), "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", h.Address, "cat /proc/meminfo")
	data, err := memoryCommandOutput(ctx, "ssh", args...)
	if err != nil {
		if ctx.Err() != nil {
			return BoxMemory{}, fmt.Errorf("sample memory on %s: %w", target, ctx.Err())
		}
		return BoxMemory{}, fmt.Errorf("sample memory on %s: %w", target, err)
	}
	result, err := parseLinuxMemory(data)
	if err != nil {
		return BoxMemory{}, fmt.Errorf("sample memory on %s: %w", target, err)
	}
	return result, nil
}
