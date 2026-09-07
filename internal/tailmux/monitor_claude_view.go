package tailmux

import (
	"fmt"
	"strings"
	"time"
)

func claudeMonitorLines(sample *ClaudeMonitorSample, unavailable string, width int) []string {
	lines := []string{dashSection("Claude usage", width)}
	if sample == nil {
		message := unavailable
		if strings.Contains(message, "no status-line sample") {
			message = "Waiting for a Claude session sample"
		}
		lines = append(lines, dashMuted.Render(dashFit(message, width)))
		return append(lines, dashMuted.Render("Enable on the box: tailmux monitor claude-setup"))
	}
	state := "last observed"
	if sample.Stale {
		state = "stale observation"
	}
	lines = append(lines, dashBadge(state+" "+sample.UpdatedAt.Local().Format("Mon 15:04"), sample.Stale))
	for _, u := range sample.Usage {
		value := "unavailable"
		if u.UsedPercent != nil {
			value = fmt.Sprintf("%s %.0f%% used", dashMeter(*u.UsedPercent, 10), *u.UsedPercent)
		}
		lines = append(lines, dashFit(u.Account+" · "+u.Window, max(10, width-26))+"  "+value)
		if reset, err := time.Parse(time.RFC3339, u.ResetsAt); err == nil {
			lines = append(lines, dashMuted.Render("Resets "+reset.Local().Format("Mon 02 Jan, 15:04")))
		}
	}
	if len(sample.Usage) == 0 {
		lines = append(lines, dashMuted.Render("No unexpired subscription limits reported"))
	}
	for _, s := range sample.CachedSessions {
		label := s.SessionName
		if label == "" {
			label = s.SessionID
		}
		lines = append(lines, dashFit(s.Project+" / "+label, width))
		info := []string{s.Model}
		if s.ContextPercent != nil {
			info = append(info, fmt.Sprintf("%.0f%% context", *s.ContextPercent))
		}
		if s.CostUSD != nil {
			info = append(info, fmt.Sprintf("$%.2f session cost", *s.CostUSD))
		}
		if s.LinesAdded != nil && s.LinesRemoved != nil {
			info = append(info, fmt.Sprintf("+%d / -%d lines", *s.LinesAdded, *s.LinesRemoved))
		}
		lines = append(lines, dashMuted.Render(dashFit(strings.Join(info, " · "), width)))
		if captured, err := time.Parse(time.RFC3339, s.CapturedAt); err == nil {
			lines = append(lines, dashMuted.Render("Observed "+captured.Local().Format("Mon 15:04")))
		}
	}
	return append(lines, dashMuted.Render("Cached sessions may be closed; account identity is unavailable."))
}
