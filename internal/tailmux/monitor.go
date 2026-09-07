package tailmux

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

type BoxMonitor struct {
	Claude         *ClaudeMonitorSample `json:"claude,omitempty"`
	ClaudeError    string               `json:"claude_error,omitempty"`
	Usage          []MonitorUsage       `json:"usage"`
	UsageError     string               `json:"usage_error,omitempty"`
	UsageCheckedAt time.Time            `json:"usage_checked_at"`
	Target         string               `json:"target"`
	Memory         *BoxMemory           `json:"memory,omitempty"`
	MemoryError    string               `json:"memory_error,omitempty"`
	Sessions       MonitorSessions      `json:"sessions"`
	CheckedAt      time.Time            `json:"checked_at"`
}
type MonitorSnapshot struct {
	Boxes     []BoxMonitor `json:"boxes"`
	CheckedAt time.Time    `json:"checked_at"`
}

func monitorTargets(cfg Config) []string {
	targets := []string{"local"}
	seen := map[string]bool{"local": true}
	for _, name := range hostNames(cfg) {
		target, err := canonicalTarget(cfg, name)
		if err == nil && !seen[target] {
			seen[target] = true
			targets = append(targets, target)
		}
	}
	sort.Strings(targets[1:])
	return targets
}
func collectMonitor(ctx context.Context, dir string, cfg Config, previous ...MonitorSnapshot) MonitorSnapshot {
	snapshot := MonitorSnapshot{CheckedAt: time.Now(), Boxes: []BoxMonitor{}}
	cached := map[string]BoxMonitor{}
	if len(previous) > 0 {
		for _, box := range previous[0].Boxes {
			cached[box.Target] = box
		}
	}
	targets := monitorTargets(cfg)
	snapshot.Boxes = make([]BoxMonitor, len(targets))
	daemonRunning := false
	if c, err := connect(dir); err == nil {
		daemonRunning = true
		c.Close()
	}
	var wg sync.WaitGroup
	slots := make(chan struct{}, 4)
	for i, target := range targets {
		wg.Add(1)
		go func(i int, target string) {
			defer wg.Done()
			box := BoxMonitor{Target: target, CheckedAt: time.Now(), Sessions: MonitorSessions{Target: target}}
			defer func() { snapshot.Boxes[i] = box }()
			if target != "local" && !daemonRunning {
				box.MemoryError = "Networking stopped · n starts it"
				box.UsageError = "Networking stopped"
				box.Sessions.Errors = []MonitorSourceError{{Source: "runtimes", Message: "Networking stopped"}}
				return
			}
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
				box.MemoryError = "Monitor refresh timed out"
				box.Sessions.Errors = []MonitorSourceError{{Source: "runtimes", Message: "Monitor refresh timed out"}}
				return
			}
			boxCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
			defer cancel()
			var parts sync.WaitGroup
			parts.Add(4)
			go func() {
				defer parts.Done()
				memory, err := collectBoxMemory(boxCtx, dir, cfg, target)
				if err != nil {
					box.MemoryError = err.Error()
				} else {
					box.Memory = &memory
				}
			}()
			go func() { defer parts.Done(); box.Sessions = collectBoxSessions(boxCtx, dir, cfg, target) }()
			go func() {
				defer parts.Done()
				if old, ok := cached[target]; ok && time.Since(old.UsageCheckedAt) < time.Minute {
					box.Usage, box.UsageError, box.UsageCheckedAt = old.Usage, old.UsageError, old.UsageCheckedAt
					return
				}
				var err error
				box.Usage, err = collectBoxUsage(boxCtx, dir, cfg, target)
				if err != nil {
					box.UsageError = err.Error()
				}
				box.UsageCheckedAt = time.Now()
			}()
			go func() {
				defer parts.Done()
				sample, err := collectClaudeMonitor(boxCtx, dir, cfg, target)
				if err != nil {
					box.ClaudeError = err.Error()
				}
				if !sample.UpdatedAt.IsZero() {
					box.Claude = &sample
				}
			}()
			parts.Wait()
			sort.SliceStable(box.Sessions.Rows, func(i, j int) bool {
				return box.Sessions.Rows[i].NeedsAttention && !box.Sessions.Rows[j].NeedsAttention
			})
			box.CheckedAt = time.Now()
		}(i, target)
	}
	wg.Wait()
	return snapshot
}
func memoryMeter(memory *BoxMemory, width int) string {
	if memory == nil {
		return "RAM unavailable"
	}
	pct := max(0.0, min(100.0, memory.Percent))
	filled := min(width, int(pct*float64(width)/100+0.5))
	return fmt.Sprintf("[%s%s] %.0f%% · %.1f / %.1f GiB", strings.Repeat("#", filled), strings.Repeat("-", width-filled), pct, float64(memory.UsedBytes)/(1<<30), float64(memory.TotalBytes)/(1<<30))
}
func attentionSummary(s MonitorSessions) string {
	attention, unknown := 0, 0
	for _, row := range s.Rows {
		if row.NeedsAttention {
			attention++
		} else if !row.AttentionKnown {
			unknown++
		}
	}
	if attention > 0 {
		return fmt.Sprintf("! %d alerts/updates", attention)
	}
	if len(s.Errors) > 0 {
		return "Some runtime status unavailable"
	}
	if unknown > 0 {
		return "No reported alerts · some states unknown"
	}
	if len(s.Rows) == 0 {
		return "No reported sessions"
	}
	return "No reported alerts"
}
func monitorCLI(dir string, cfg Config, args []string) error {
	if len(args) > 1 || len(args) == 1 && args[0] != "--json" {
		return fmt.Errorf("usage: tailmux monitor [--json]")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	snapshot := collectMonitor(ctx, dir, cfg)
	if len(args) == 1 {
		return json.NewEncoder(os.Stdout).Encode(snapshot)
	}
	for _, box := range snapshot.Boxes {
		fmt.Printf("\n%s  %s\n", box.Target, memoryMeter(box.Memory, 12))
		if box.MemoryError != "" {
			fmt.Println("  " + box.MemoryError)
		}
		fmt.Println("  " + attentionSummary(box.Sessions))
		for _, row := range box.Sessions.Rows {
			flag := " "
			if row.NeedsAttention {
				flag = "!"
			}
			fmt.Printf("  %s %-8s %-24s %s\n", flag, row.Source, cleanDashboardText(row.Title), cleanDashboardText(row.State))
			info := []string{}
			for _, value := range []string{row.Project, row.Branch, row.AgentType, row.TaskTitle, row.CWD} {
				if value != "" {
					info = append(info, cleanDashboardText(value))
				}
			}
			if len(info) > 0 {
				fmt.Println("    " + strings.Join(info, " · "))
			}
		}
		for _, usage := range box.Usage {
			fmt.Println("  " + usageSummary(usage))
		}
		if box.UsageError != "" {
			fmt.Println("  Usage: " + box.UsageError)
		}
		if box.Claude != nil {
			fmt.Println("  Claude observed " + box.Claude.UpdatedAt.Local().Format(time.RFC3339))
			for _, usage := range box.Claude.Usage {
				fmt.Println("  " + usageSummary(usage))
			}
		} else if box.ClaudeError != "" {
			fmt.Println("  Claude: " + box.ClaudeError)
		}
		for _, err := range box.Sessions.Errors {
			fmt.Printf("  %s: %s\n", err.Source, err.Message)
		}
	}
	return nil
}

func usageSummary(usage MonitorUsage) string {
	value := "unavailable"
	if usage.UsedPercent != nil {
		value = fmt.Sprintf("%.0f%% used", *usage.UsedPercent)
	}
	if usage.RemainingPercent != nil {
		value += fmt.Sprintf(" · %.0f%% remaining", *usage.RemainingPercent)
	}
	result := usage.Provider + " " + usage.Window + ": " + value
	if usage.Account != "" {
		result += " · " + usage.Account
	}
	if usage.ResetsAt != "" {
		result += " · resets " + usage.ResetsAt
	}
	return result
}
