package tailmux

import (
	"context"
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseLinuxMemoryUsesMemAvailable(t *testing.T) {
	data := []byte("MemTotal:       1000000 kB\nMemFree:         100000 kB\nMemAvailable:    400000 kB\nCached:          250000 kB\n")
	got, err := parseLinuxMemory(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalBytes != 1024000000 || got.UsedBytes != 614400000 || got.Percent != 60 {
		t.Fatalf("unexpected memory: %+v", got)
	}
	if _, err := parseLinuxMemory([]byte("MemTotal: 10 kB\nMemFree: 5 kB\n")); err == nil || !strings.Contains(err.Error(), "MemAvailable") {
		t.Fatalf("missing MemAvailable accepted: %v", err)
	}
}

func TestLiveCollectRemoteMemory(t *testing.T) {
	target, proxy := os.Getenv("TAILMUX_LIVE_MEMORY_TARGET"), os.Getenv("TAILMUX_LIVE_PROXY_EXE")
	if target == "" || proxy == "" {
		t.Skip("set TAILMUX_LIVE_MEMORY_TARGET and TAILMUX_LIVE_PROXY_EXE for a read-only remote check")
	}
	original := monitorExecutable
	monitorExecutable = func() (string, error) { return proxy, nil }
	t.Cleanup(func() { monitorExecutable = original })
	dir, err := configDir()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	memory, err := collectBoxMemory(ctx, dir, cfg, target)
	if err != nil {
		t.Fatal(err)
	}
	if memory.TotalBytes == 0 || memory.Percent < 0 || memory.Percent > 100 {
		t.Fatalf("invalid memory result for %s", target)
	}
	t.Logf("received memory sample from %s", target)
}

func TestParseMacMemory(t *testing.T) {
	vm := []byte("Mach Virtual Memory Statistics: (page size of 4096 bytes)\nPages free: 10.\nPages inactive: 20.\nPages speculative: 5.\nPages purgeable: 5.\nPages active: 100.\n")
	got, err := parseMacMemory([]byte("409600"), vm)
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalBytes != 409600 || got.UsedBytes != 266240 || math.Abs(got.Percent-65) > 0.001 {
		t.Fatalf("unexpected memory: %+v", got)
	}
	if _, err := parseMacMemory([]byte("409600"), []byte("Mach Virtual Memory Statistics: (page size of 4096 bytes)\nPages active: 100.\n")); err == nil {
		t.Fatal("missing available counters accepted")
	}
}

func TestMemoryResultClampsAvailable(t *testing.T) {
	got, err := memoryResult(100, 120)
	if err != nil || got.UsedBytes != 0 || got.Percent != 0 {
		t.Fatalf("unexpected clamped memory: %+v %v", got, err)
	}
	if _, err := memoryResult(0, 0); err == nil {
		t.Fatal("zero total accepted")
	}
}

func TestCollectRemoteMemoryUsesBatchSSH(t *testing.T) {
	original := memoryCommandOutput
	t.Cleanup(func() { memoryCommandOutput = original })
	memoryCommandOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "ssh" || !strings.Contains(strings.Join(args, " "), "BatchMode=yes") || args[len(args)-1] != "cat /proc/meminfo" {
			t.Fatalf("unexpected command: %s %v", name, args)
		}
		return []byte("MemTotal: 100 kB\nMemAvailable: 25 kB\n"), nil
	}
	cfg := fixtureConfig()
	got, err := collectBoxMemory(context.Background(), t.TempDir(), cfg, "worker-a")
	if err != nil || got.Percent != 75 {
		t.Fatalf("unexpected sample: %+v %v", got, err)
	}
}
