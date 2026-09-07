package tailmux

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func floatPointer(v float64) *float64 { return &v }

func TestCaptureClaudeStatuslineStoresAllowlist(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	originalNow := claudeNow
	claudeNow = func() time.Time { return now }
	t.Cleanup(func() { claudeNow = originalNow })
	input := `{"session_id":"session-1","session_name":"Build","transcript_path":"/secret/transcript","model":{"display_name":"Opus"},"workspace":{"project_dir":"/home/me/project"},"context_window":{"used_percentage":25},"cost":{"total_cost_usd":1.5,"total_lines_added":10,"total_lines_removed":2},"rate_limits":{"five_hour":{"used_percentage":30,"resets_at":1800000000}}}`
	if err := captureClaudeStatusline(strings.NewReader(input)); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(state, "tailmux", "claude-monitor", "*.json"))
	if len(files) != 1 {
		t.Fatalf("expected one cache file, got %v", files)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "transcript") || strings.Contains(string(data), "/home/me") {
		t.Fatalf("cache retained private input: %s", data)
	}
	var record claudeCacheRecord
	if json.Unmarshal(data, &record) != nil || record.Project != "project" || record.Model != "Opus" || record.ContextPercent == nil || *record.ContextPercent != 25 {
		t.Fatalf("unexpected record: %+v", record)
	}
	if info, _ := os.Stat(files[0]); info.Mode().Perm() != 0600 {
		t.Fatalf("cache permissions are %o", info.Mode().Perm())
	}
}

func TestCaptureClaudeStatuslineRejectsMissingAndLargeInput(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := captureClaudeStatusline(strings.NewReader(`{"rate_limits":{}}`)); err == nil {
		t.Fatal("missing session accepted")
	}
	if err := captureClaudeStatusline(strings.NewReader(strings.Repeat("x", claudeStatuslineMaxBytes+1))); err == nil {
		t.Fatal("oversize input accepted")
	}
}

func TestCollectClaudeMonitorFreshness(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	originalNow := claudeNow
	claudeNow = func() time.Time { return now }
	t.Cleanup(func() { claudeNow = originalNow })
	used := 40.0
	record := claudeCacheRecord{Version: 1, CapturedAt: now.Add(-time.Minute), SessionID: "s", FiveHour: &claudeLimit{UsedPercent: &used}}
	dir := filepath.Join(state, "tailmux", "claude-monitor")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(record)
	if err := os.WriteFile(filepath.Join(dir, "record.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	sample, err := collectClaudeMonitor(context.Background(), "", Config{}, "local")
	if err != nil || sample.Stale || len(sample.Usage) != 1 || *sample.Usage[0].RemainingPercent != 60 {
		t.Fatalf("unexpected sample: %+v %v", sample, err)
	}
	claudeNow = func() time.Time { return now.Add(time.Hour) }
	sample, err = collectClaudeMonitor(context.Background(), "", Config{}, "local")
	if err != nil || !sample.Stale {
		t.Fatalf("stale sample not marked: %+v %v", sample, err)
	}
}

func TestClaudeUsageMissingIsNotZero(t *testing.T) {
	record := claudeCacheRecord{FiveHour: &claudeLimit{}}
	if got := claudeUsage("local", record); len(got) != 0 {
		t.Fatalf("missing percentage rendered: %+v", got)
	}
}

func TestClaudeUsageFiltersExpiredAndPreservesOverspend(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	originalNow := claudeNow
	claudeNow = func() time.Time { return now }
	t.Cleanup(func() { claudeNow = originalNow })
	expired, future := now.Add(-time.Second).Unix(), now.Add(time.Hour).Unix()
	ordinary, overspend := 120.0, 125.0
	record := claudeCacheRecord{
		FiveHour:   &claudeLimit{UsedPercent: &ordinary, ResetsAt: &expired},
		SevenDay:   &claudeLimit{UsedPercent: &ordinary, ResetsAt: &future},
		SpendLimit: &claudeLimit{UsedPercent: &overspend, ResetsAt: &future},
	}
	usage := claudeUsage("box", record)
	if len(usage) != 2 || usage[0].Window != "7d" || *usage[0].UsedPercent != 100 || usage[1].Window != "spend" || *usage[1].UsedPercent != 125 || *usage[1].RemainingPercent != 0 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
	if usage[0].Account != "Latest observed Claude session on box" {
		t.Fatalf("unexpected account label: %q", usage[0].Account)
	}
}
