package tailmux

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	claudeStatuslineMaxBytes = 2 << 20
	claudeCacheMaxFiles      = 64
	claudeCacheStaleAfter    = 15 * time.Minute
)

type claudeLimit struct {
	UsedPercent *float64 `json:"used_percentage"`
	ResetsAt    *int64   `json:"resets_at"`
}

type claudeStatusInput struct {
	SessionID   string `json:"session_id"`
	SessionName string `json:"session_name"`
	Model       struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"model"`
	Workspace struct {
		ProjectDir string `json:"project_dir"`
	} `json:"workspace"`
	Context struct {
		UsedPercent *float64 `json:"used_percentage"`
	} `json:"context_window"`
	Cost struct {
		TotalUSD     *float64 `json:"total_cost_usd"`
		LinesAdded   *int64   `json:"total_lines_added"`
		LinesRemoved *int64   `json:"total_lines_removed"`
	} `json:"cost"`
	RateLimits struct {
		FiveHour   *claudeLimit `json:"five_hour"`
		SevenDay   *claudeLimit `json:"seven_day"`
		SpendLimit *claudeLimit `json:"spend_limit"`
	} `json:"rate_limits"`
}

type claudeCacheRecord struct {
	Version        int          `json:"version"`
	CapturedAt     time.Time    `json:"captured_at"`
	SessionID      string       `json:"session_id"`
	SessionName    string       `json:"session_name,omitempty"`
	Model          string       `json:"model,omitempty"`
	Project        string       `json:"project,omitempty"`
	ContextPercent *float64     `json:"context_percent,omitempty"`
	CostUSD        *float64     `json:"cost_usd,omitempty"`
	LinesAdded     *int64       `json:"lines_added,omitempty"`
	LinesRemoved   *int64       `json:"lines_removed,omitempty"`
	FiveHour       *claudeLimit `json:"five_hour,omitempty"`
	SevenDay       *claudeLimit `json:"seven_day,omitempty"`
	SpendLimit     *claudeLimit `json:"spend_limit,omitempty"`
}

type ClaudeTelemetry struct {
	Source         string   `json:"source"`
	SessionID      string   `json:"session_id"`
	SessionName    string   `json:"session_name,omitempty"`
	Model          string   `json:"model,omitempty"`
	Project        string   `json:"project,omitempty"`
	ContextPercent *float64 `json:"context_percent,omitempty"`
	CostUSD        *float64 `json:"cost_usd,omitempty"`
	LinesAdded     *int64   `json:"lines_added,omitempty"`
	LinesRemoved   *int64   `json:"lines_removed,omitempty"`
	CapturedAt     string   `json:"captured_at"`
}

type ClaudeMonitorSample struct {
	Usage          []MonitorUsage    `json:"usage"`
	CachedSessions []ClaudeTelemetry `json:"cached_sessions"`
	UpdatedAt      time.Time         `json:"updated_at"`
	Stale          bool              `json:"stale"`
}

var claudeNow = time.Now

func claudeStateDir() (string, error) {
	if state := os.Getenv("XDG_STATE_HOME"); state != "" {
		return filepath.Join(state, "tailmux", "claude-monitor"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "tailmux", "claude-monitor"), nil
}

func boundedFloat(value *float64, max float64) *float64 {
	if value == nil {
		return nil
	}
	v := *value
	if v < 0 {
		v = 0
	} else if v > max {
		v = max
	}
	return &v
}

func clipDisplay(s string) string {
	s = cleanDisplay(s)
	if len(s) > 256 {
		s = s[:256]
	}
	return s
}

func renderClaudeStatusline(status claudeStatusInput) string {
	model := status.Model.DisplayName
	if model == "" {
		model = status.Model.ID
	}
	parts := []string{}
	if model != "" {
		parts = append(parts, clipDisplay(model))
	}
	if status.Context.UsedPercent != nil {
		parts = append(parts, fmt.Sprintf("context %.0f%%", *boundedFloat(status.Context.UsedPercent, 100)))
	}
	if status.RateLimits.FiveHour != nil && status.RateLimits.FiveHour.UsedPercent != nil {
		parts = append(parts, fmt.Sprintf("5h %.0f%%", *boundedFloat(status.RateLimits.FiveHour.UsedPercent, 100)))
	}
	if status.RateLimits.SevenDay != nil && status.RateLimits.SevenDay.UsedPercent != nil {
		parts = append(parts, fmt.Sprintf("7d %.0f%%", *boundedFloat(status.RateLimits.SevenDay.UsedPercent, 100)))
	}
	if len(parts) == 0 {
		return "Tailmux · Claude"
	}
	return strings.Join(parts, " · ")
}

// captureClaudeStatusline stores only allowlisted monitor fields from Claude's
// status-line JSON. It does not retain cwd, transcripts, prompts, or credentials.
func captureClaudeStatusline(input io.Reader) error {
	data, err := io.ReadAll(io.LimitReader(input, claudeStatuslineMaxBytes+1))
	if err != nil {
		return fmt.Errorf("read Claude status line: %w", err)
	}
	if len(data) > claudeStatuslineMaxBytes {
		return fmt.Errorf("Claude status-line payload exceeds %d bytes", claudeStatuslineMaxBytes)
	}
	var status claudeStatusInput
	if err = json.Unmarshal(data, &status); err != nil {
		return fmt.Errorf("invalid Claude status-line payload")
	}
	if status.SessionID == "" || len(status.SessionID) > 512 {
		return fmt.Errorf("Claude status-line payload has no valid session ID")
	}
	model := status.Model.DisplayName
	if model == "" {
		model = status.Model.ID
	}
	record := claudeCacheRecord{
		Version: 1, CapturedAt: claudeNow().UTC(), SessionID: status.SessionID,
		SessionName: clipDisplay(status.SessionName), Model: clipDisplay(model),
		Project:        clipDisplay(filepath.Base(status.Workspace.ProjectDir)),
		ContextPercent: boundedFloat(status.Context.UsedPercent, 100),
		CostUSD:        status.Cost.TotalUSD, LinesAdded: status.Cost.LinesAdded, LinesRemoved: status.Cost.LinesRemoved,
		FiveHour: status.RateLimits.FiveHour, SevenDay: status.RateLimits.SevenDay, SpendLimit: status.RateLimits.SpendLimit,
	}
	dir, err := claudeStateDir()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err = os.Chmod(dir, 0700); err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(status.SessionID))
	destination := filepath.Join(dir, hex.EncodeToString(digest[:])+".json")
	tmp, err := os.CreateTemp(dir, ".claude-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err = tmp.Chmod(0600); err == nil {
		err = json.NewEncoder(tmp).Encode(record)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmpName, destination); err != nil {
		return err
	}
	_, err = fmt.Fprintln(os.Stdout, renderClaudeStatusline(status))
	return err
}

func readLocalClaudeRecords() ([]claudeCacheRecord, error) {
	dir, err := claudeStateDir()
	if err != nil {
		return nil, err
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	type item struct {
		path string
		mod  time.Time
	}
	items := make([]item, 0, len(paths))
	for _, path := range paths {
		if info, e := os.Stat(path); e == nil && info.Mode().IsRegular() && info.Size() <= 32<<10 {
			items = append(items, item{path, info.ModTime()})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mod.After(items[j].mod) })
	if len(items) > claudeCacheMaxFiles {
		items = items[:claudeCacheMaxFiles]
	}
	var records []claudeCacheRecord
	for _, item := range items {
		file, e := os.Open(item.path)
		if e != nil {
			continue
		}
		data, e := io.ReadAll(io.LimitReader(file, 32<<10))
		_ = file.Close()
		if e != nil {
			continue
		}
		var record claudeCacheRecord
		if json.Unmarshal(data, &record) == nil && record.Version == 1 && record.SessionID != "" && !record.CapturedAt.IsZero() {
			records = append(records, record)
		}
	}
	return records, nil
}

func decodeClaudeRecords(data []byte) []claudeCacheRecord {
	dec := json.NewDecoder(bytes.NewReader(data))
	var records []claudeCacheRecord
	for len(records) < claudeCacheMaxFiles {
		var record claudeCacheRecord
		if err := dec.Decode(&record); err != nil {
			break
		}
		if record.Version == 1 && record.SessionID != "" && !record.CapturedAt.IsZero() {
			records = append(records, record)
		}
	}
	return records
}

func readRemoteClaudeRecords(ctx context.Context, dir string, cfg Config, target string) ([]claudeCacheRecord, error) {
	h, err := cfg.host(target)
	if err != nil {
		return nil, err
	}
	exe, err := monitorExecutable()
	if err != nil {
		return nil, err
	}
	remote := `d=${XDG_STATE_HOME:-$HOME/.local/state}/tailmux/claude-monitor; [ -d "$d" ] || exit 0; cd "$d"; find . -maxdepth 1 -type f -name '*.json' -size -32k -printf '%T@ %f\n' | sort -rn | head -64 | cut -d' ' -f2- | while IFS= read -r f; do head -c 32768 -- "$f"; printf '\n'; done`
	args := append(sshOptions(exe, dir, target, h), "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", h.Address, remote)
	data, err := memoryCommandOutput(ctx, "ssh", args...)
	if err != nil {
		return nil, fmt.Errorf("read Claude monitor on %s", target)
	}
	return decodeClaudeRecords(data), nil
}

func claudeUsage(source string, record claudeCacheRecord) []MonitorUsage {
	type window struct {
		name  string
		limit *claudeLimit
	}
	var result []MonitorUsage
	for _, current := range []window{{"5h", record.FiveHour}, {"7d", record.SevenDay}, {"spend", record.SpendLimit}} {
		if current.limit == nil || current.limit.UsedPercent == nil {
			continue
		}
		if current.limit.ResetsAt != nil && *current.limit.ResetsAt <= claudeNow().Unix() {
			continue
		}
		used := *current.limit.UsedPercent
		if used < 0 {
			used = 0
		}
		if current.name != "spend" && used > 100 {
			used = 100
		}
		remaining := 100 - used
		if remaining < 0 {
			remaining = 0
		}
		item := MonitorUsage{Source: source, Provider: "Claude", Account: "Latest observed Claude session on " + source, Window: current.name, UsedPercent: &used, RemainingPercent: &remaining}
		if current.limit.ResetsAt != nil {
			item.ResetsAt = time.Unix(*current.limit.ResetsAt, 0).UTC().Format(time.RFC3339)
		}
		result = append(result, item)
	}
	return result
}

func collectClaudeMonitor(ctx context.Context, dir string, cfg Config, target string) (ClaudeMonitorSample, error) {
	ctx, cancel := boundedMemoryContext(ctx)
	defer cancel()
	var records []claudeCacheRecord
	var err error
	if target == "local" {
		records, err = readLocalClaudeRecords()
	} else {
		records, err = readRemoteClaudeRecords(ctx, dir, cfg, target)
	}
	if err != nil {
		return ClaudeMonitorSample{}, err
	}
	if len(records) == 0 {
		return ClaudeMonitorSample{}, fmt.Errorf("Claude usage unavailable on %s: no status-line sample", target)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].CapturedAt.After(records[j].CapturedAt) })
	latest := records[0]
	sample := ClaudeMonitorSample{Usage: claudeUsage(target, latest), UpdatedAt: latest.CapturedAt, Stale: claudeNow().Sub(latest.CapturedAt) > claudeCacheStaleAfter}
	for _, record := range records {
		sample.CachedSessions = append(sample.CachedSessions, ClaudeTelemetry{
			Source: target, SessionID: record.SessionID, SessionName: record.SessionName,
			Model: record.Model, Project: record.Project, ContextPercent: record.ContextPercent,
			CostUSD: record.CostUSD, LinesAdded: record.LinesAdded, LinesRemoved: record.LinesRemoved,
			CapturedAt: record.CapturedAt.Format(time.RFC3339),
		})
	}
	if len(sample.Usage) == 0 {
		return sample, fmt.Errorf("Claude usage unavailable on %s: rate limits have not appeared after an API response", target)
	}
	return sample, nil
}

func collectClaudeUsage(ctx context.Context, dir string, cfg Config, target string) ([]MonitorUsage, error) {
	sample, err := collectClaudeMonitor(ctx, dir, cfg, target)
	return sample.Usage, err
}

// claudeStatuslineInstallHint describes a wrapper that preserves the existing
// status line by replaying the same JSON to both commands. It does not edit settings.
func claudeStatuslineInstallHint(existingCommand string) string {
	if strings.TrimSpace(existingCommand) == "" {
		return "Configure Claude statusLine.command to pipe its JSON into: tailmux monitor claude-statusline"
	}
	return "Preserve the existing status line with a wrapper that reads stdin once, sends the same JSON to tailmux monitor claude-statusline, then sends it to the previous command: " + existingCommand
}
