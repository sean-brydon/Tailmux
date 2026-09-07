package tailmux

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"
)

func TestWaitForRPCSkipsNotifications(t *testing.T) {
	stream := bytes.NewBufferString("{\"method\":\"notice\"}\n{\"id\":2,\"result\":{\"rateLimits\":{}}}\n")
	var got codexRateResponse
	if err := waitForRPC(json.NewDecoder(stream), 2, &got); err != nil {
		t.Fatal(err)
	}
}

func TestLiveCollectBoxUsage(t *testing.T) {
	if os.Getenv("TAILMUX_LIVE_USAGE") != "1" {
		t.Skip("set TAILMUX_LIVE_USAGE=1 for a read-only local Codex usage check")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	usage, err := collectBoxUsage(ctx, "", Config{}, "local")
	if err != nil {
		t.Fatal(err)
	}
	if len(usage) == 0 {
		t.Fatal("no usage windows")
	}
	t.Logf("received %d local Codex usage windows", len(usage))
}

func TestLiveCollectRemoteBoxUsage(t *testing.T) {
	target, proxy := os.Getenv("TAILMUX_LIVE_USAGE_TARGET"), os.Getenv("TAILMUX_LIVE_PROXY_EXE")
	if target == "" || proxy == "" {
		t.Skip("set TAILMUX_LIVE_USAGE_TARGET and TAILMUX_LIVE_PROXY_EXE for a read-only remote check")
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
	usage, err := collectBoxUsage(ctx, dir, cfg, target)
	if err != nil {
		t.Fatal(err)
	}
	if len(usage) == 0 {
		t.Fatal("no usage windows")
	}
	t.Logf("received %d Codex usage windows from %s", len(usage), target)
}

func TestUsageFromCodexResponse(t *testing.T) {
	duration, reset := int64(300), int64(1800000000)
	used := float64(27)
	email := "dev@example.test"
	response := codexUsageResponse{Rates: codexRateResponse{RateLimitsByLimitID: map[string]codexRateSnapshot{
		"codex": {LimitName: "Codex", Primary: &codexRateWindow{UsedPercent: &used, WindowDurationMin: &duration, ResetsAt: &reset}},
	}}, Account: codexAccountResponse{Account: &struct {
		Type     string  `json:"type"`
		Email    *string `json:"email"`
		PlanType string  `json:"planType"`
	}{Type: "chatgpt", Email: &email}}}
	usage, err := usageFromCodexResponse("local", response)
	if err != nil || len(usage) != 1 {
		t.Fatalf("unexpected usage: %+v %v", usage, err)
	}
	if usage[0].Source != "local" || usage[0].Provider != "Codex" || usage[0].Account != email || usage[0].Window != "5h" || math.Abs(*usage[0].RemainingPercent-73) > 0.001 || usage[0].ResetsAt == "" {
		t.Fatalf("unexpected item: %+v", usage[0])
	}
}

func TestUsageClampsBackendPercent(t *testing.T) {
	used := float64(120)
	response := codexUsageResponse{Rates: codexRateResponse{RateLimits: codexRateSnapshot{Primary: &codexRateWindow{UsedPercent: &used}}}}
	usage, err := usageFromCodexResponse("box", response)
	if err != nil || *usage[0].UsedPercent != 100 || *usage[0].RemainingPercent != 0 {
		t.Fatalf("unexpected item: %+v %v", usage, err)
	}
}

func TestUsageSkipsUnknownPercent(t *testing.T) {
	response := codexUsageResponse{Rates: codexRateResponse{RateLimits: codexRateSnapshot{Primary: &codexRateWindow{}}}}
	if _, err := usageFromCodexResponse("box", response); err == nil {
		t.Fatal("unknown usage rendered as zero")
	}
}
