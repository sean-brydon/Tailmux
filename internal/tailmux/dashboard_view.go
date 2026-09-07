package tailmux

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

var dashSurface = lipgloss.Color("#202830")
var dashLine = lipgloss.Color("#36434B")
var dashGood = lipgloss.Color("#A3CEAC")
var dashWarn = lipgloss.Color("#E6BA79")
var dashText = lipgloss.NewStyle().Foreground(lipgloss.Color("#DCE5E7"))

func dashFit(s string, width int) string {
	return ansi.Truncate(strings.ReplaceAll(cleanDashboardText(s), "\n", " "), max(1, width), "…")
}
func dashBadge(s string, warning bool) string {
	color := dashGood
	if warning {
		color = dashWarn
	}
	return lipgloss.NewStyle().Foreground(color).Render(s)
}
func dashMeter(percent float64, width int) string {
	percent = max(0, min(100, percent))
	filled := int(percent*float64(width)/100 + .5)
	color := dashGood
	if percent >= 80 {
		color = dashWarn
	}
	return lipgloss.NewStyle().Foreground(color).Render(strings.Repeat("━", filled)) +
		lipgloss.NewStyle().Foreground(dashLine).Render(strings.Repeat("─", width-filled))
}
func dashPanel(title, body string, width, height int, active bool) string {
	color := dashLine
	if active {
		color = dashAccent
	}
	inner := max(1, width-4)
	lines := strings.Split(body, "\n")
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], inner, "…")
	}
	content := dashTitle.Render(dashFit(title, inner)) + "\n\n" + strings.Join(lines, "\n")
	content = lipgloss.NewStyle().Width(inner).Height(max(1, height-2)).MaxHeight(max(1, height-2)).Render(content)
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(color).Padding(0, 1).Render(content)
}
func (m dashboardModel) monitorDetail(width int) string {
	if m.cursor >= len(m.monitor.Boxes) {
		return "Waiting for box telemetry…\n\nSave a remote host in 1 Boxes with e."
	}
	b := m.monitor.Boxes[m.cursor]
	lines := []string{dashTitle.Render(dashFit(b.Target, width))}
	if !b.CheckedAt.IsZero() {
		lines = append(lines, dashMuted.Render("Updated "+b.CheckedAt.Local().Format("15:04:05")+"  ·  refresh 15s"))
	}
	lines = append(lines, "", dashMuted.Render("MEMORY"))
	if b.Memory != nil {
		lines = append(lines, fmt.Sprintf("%s  %3.0f%%", dashMeter(b.Memory.Percent, min(28, max(8, width-12))), b.Memory.Percent),
			dashMuted.Render(fmt.Sprintf("%.1f GiB used / %.1f GiB total", float64(b.Memory.UsedBytes)/(1<<30), float64(b.Memory.TotalBytes)/(1<<30))))
	} else {
		lines = append(lines, dashBadge("Unavailable", true), dashMuted.Render(dashFit(b.MemoryError, width)))
	}
	lines = append(lines, "")
	lines = append(lines, m.boxForwardLines(b.Target)...)
	lines = append(lines, "", dashMuted.Render("ACCOUNT LIMITS"))
	if len(b.Usage) == 0 {
		lines = append(lines, dashMuted.Render(dashFit(b.UsageError, width)))
	}
	account := ""
	for _, u := range b.Usage {
		if u.Account != account {
			lines = append(lines, dashMuted.Render(dashFit(u.Account, width)))
			account = u.Account
		}
		label := dashFit(u.Provider+" · "+u.Window, max(8, width-24))
		meter := "unavailable"
		if u.UsedPercent != nil {
			meter = fmt.Sprintf("%s %3.0f%% used", dashMeter(*u.UsedPercent, 10), *u.UsedPercent)
		}
		lines = append(lines, label+"  "+meter)
		if reset, err := time.Parse(time.RFC3339, u.ResetsAt); err == nil {
			lines = append(lines, dashMuted.Render("Resets "+reset.Local().Format("Mon 02 Jan, 15:04")))
		}
	}
	lines = append(lines, dashMuted.Render("Default CLI account · limits may be shared"), "")
	lines = append(lines, claudeMonitorLines(b.Claude, b.ClaudeError, width)...)
	lines = append(lines, "", dashMuted.Render(fmt.Sprintf("SESSIONS & AGENTS  ·  %d", len(b.Sessions.Rows))))
	if len(b.Sessions.Rows) == 0 {
		lines = append(lines, dashMuted.Render("No sessions reported"))
	}
	for _, row := range b.Sessions.Rows {
		flag := " "
		if row.NeedsAttention {
			flag = "!"
		}
		label := row.Source + " / " + row.Title
		if row.Project != "" && row.Project != row.Title {
			label = row.Project + " / " + row.Title
		}
		state := row.State
		if row.AttentionReason != "" {
			state = row.AttentionReason
		}
		if width >= 55 {
			nameWidth := width - 24
			name := lipgloss.NewStyle().Width(nameWidth).Render(dashFit(flag+" "+label, nameWidth))
			lines = append(lines, name+" "+dashBadge(dashFit(state, 22), row.NeedsAttention))
		} else {
			lines = append(lines, dashFit(flag+" "+label, width), dashBadge("  "+state, row.NeedsAttention))
		}
		info := []string{}
		if row.LastActivityAt > 0 {
			info = append(info, "active "+time.UnixMilli(row.LastActivityAt).Local().Format("Mon 15:04"))
		}
		if row.AgentType != "" {
			info = append(info, row.AgentType)
		}
		if row.Branch != "" {
			info = append(info, "branch "+row.Branch)
		}
		if row.TaskTitle != "" && row.TaskTitle != row.Title {
			info = append(info, row.TaskTitle)
		}
		if row.CWD != "" {
			info = append(info, row.CWD)
		}
		if len(info) > 0 {
			lines = append(lines, dashMuted.Render("  "+dashFit(strings.Join(info, " · "), width-2)))
		}
	}
	lines = append(lines, "", dashMuted.Render("Unread updates are alerts; unknown states are not idle."))
	for _, e := range b.Sessions.Errors {
		lines = append(lines, dashBadge(dashFit(e.Source+" · "+e.Message, width), true))
	}
	return strings.Join(lines, "\n")
}
func (m dashboardModel) polishedView() tea.View {
	width, height := max(40, m.width), max(12, m.height)
	names := []string{"Boxes", "Forwards", "Orca", "Setup", "Monitor"}
	header := dashSelected.Padding(0, 1).Render("TAILMUX") + "  " + dashTitle.Render(names[m.section])
	status := "○ Network stopped"
	if m.snapshot.Daemon {
		status = "● Network running"
	}
	if m.busy {
		status = "Working…"
	} else if m.loading || (m.section == 4 && m.monitorLoading) {
		status += " · refreshing"
	}
	gap := max(1, width-lipgloss.Width(header)-lipgloss.Width(status)-2)
	header += "" + strings.Repeat(" ", gap) + dashMuted.Render(status)
	tabs := []string{}
	for _, i := range []int{4, 0, 1, 2, 3} {
		number := i + 1
		if i == 4 {
			number = 0
		}
		label := fmt.Sprintf("%d %s", number, names[i])
		style := dashMuted.Padding(0, 1)
		if i == m.section {
			style = lipgloss.NewStyle().Foreground(dashAccent).Background(dashSurface).Bold(true).Padding(0, 1)
		}
		tabs = append(tabs, style.Render(label))
	}
	bodyHeight := height - 7
	leftWidth := min(36, max(26, width/3))
	detailWidth := width - leftWidth - 1
	compact := width < 85
	if compact {
		leftWidth = width
		detailWidth = width
	}
	items := m.items()
	indices := m.matchingItems()
	pos := 0
	for n, i := range indices {
		if i == m.cursor {
			pos = n
		}
	}
	rowHeight := 3
	visible := max(1, (bodyHeight-4)/rowHeight)
	if compact {
		visible = 1
	}
	start := max(0, pos-visible+1)
	rows := []string{}
	for _, i := range indices[start:min(len(indices), start+visible)] {
		parts := strings.SplitN(items[i], "\n", 2)
		title := parts[0]
		subtitle := ""
		if len(parts) > 1 {
			subtitle = strings.Split(parts[1], "\n")[0]
		}
		if m.section == 4 {
			b := m.monitor.Boxes[i]
			subtitle = "RAM unavailable"
			if b.Memory != nil {
				subtitle = fmt.Sprintf("%s %2.0f%% RAM", dashMeter(b.Memory.Percent, 10), b.Memory.Percent)
			}
			alerts := 0
			for _, row := range b.Sessions.Rows {
				if row.NeedsAttention {
					alerts++
				}
			}
			if alerts > 0 {
				subtitle += dashBadge(fmt.Sprintf("  !%d", alerts), true)
			}
			if m.cfg.HiddenBoxes[b.Target] {
				title += " · hidden"
			}
		}
		mark := "  "
		style := dashText.Width(leftWidth - 4)
		if i == m.cursor {
			mark = "▸ "
			style = style.Background(dashSurface).Foreground(dashAccent).Bold(true)
		}
		rows = append(rows, style.Render(dashFit(mark+title, leftWidth-4))+"\n  "+ansi.Truncate(subtitle, leftWidth-6, "…"))
	}
	if len(rows) == 0 {
		rows = append(rows, dashMuted.Render("No matching items\n/ search · H show hidden"))
	}
	listTitle := fmt.Sprintf("%s · %d", names[m.section], len(indices))
	if m.section == 4 {
		listTitle = fmt.Sprintf("Machines · %d", len(indices))
	}
	hidden := m.count() - len(indices)
	if m.filter != "" || m.searching {
		listTitle = "Search: " + m.filter
	} else if hidden > 0 {
		listTitle += fmt.Sprintf(" / %d hidden", hidden)
	}
	details := m.details()
	if m.section == 0 && m.output == "" && m.target() != "" {
		details = m.boxForwardDetails(m.target()) + "\n\n" + details
	}
	if m.section == 4 && m.output == "" {
		details = m.monitorDetail(detailWidth - 4)
	}
	// Wrap before scrolling so long paths and provider labels remain accessible.
	details = lipgloss.NewStyle().Width(max(1, detailWidth-4)).Render(details)
	lines := strings.Split(details, "\n")
	detailHeight := max(1, bodyHeight-5)
	if compact {
		detailHeight = max(1, detailHeight-4)
	}
	scroll := min(m.scroll, max(0, len(lines)-detailHeight))
	// Monitor owns its complete content; legacy output has already been paginated.
	if (m.section != 4 && m.section != 0) || m.output != "" {
		scroll = 0
	}
	end := min(len(lines), scroll+detailHeight)
	detailBody := strings.Join(lines[scroll:end], "\n")
	detailTitle := "Overview"
	if m.output != "" {
		detailTitle = "Command output"
	}
	if len(lines) > detailHeight {
		detailTitle += fmt.Sprintf(" · %d–%d/%d · PgUp/PgDn", scroll+1, end, len(lines))
	}
	body := ""
	if compact {
		selected := "No selection"
		if len(rows) > 0 {
			selected = rows[min(pos-start, len(rows)-1)]
		}
		body = dashPanel(listTitle, selected+"\n\n"+detailBody, width, bodyHeight, true)
	} else {
		body = lipgloss.JoinHorizontal(lipgloss.Top, dashPanel(listTitle, strings.Join(rows, "\n\n"), leftWidth, bodyHeight, true), " ", dashPanel(detailTitle, detailBody, detailWidth, bodyHeight, false))
	}
	message := m.message
	if message == "" || strings.HasPrefix(message, "Welcome.") {
		message = "Select a machine to explore your workspace"
	}
	keys := "↑↓ select   enter open   / filter   h hide   H hidden   r refresh   q quit"
	if m.section == 1 {
		keys = "↑↓ select   f new forward   v resume   x remove   r refresh   q quit"
	}
	if m.section == 2 {
		keys = "↑↓ select   s verify   o connect   enter terminal   q quit"
	}
	if m.section == 3 {
		keys = "↑↓ select   enter apply   a account   e host   q quit"
	}
	content := header + "\n\n" + strings.Join(tabs, " ") + "\n" + body + "\n" + dashMuted.Render(dashFit(" "+message, width)) + "\n" + dashMuted.Render(dashFit(" "+keys, width))
	v := tea.NewView(lipgloss.NewStyle().MaxWidth(m.width).MaxHeight(m.height).Render(content))
	v.AltScreen = true
	v.WindowTitle = "Tailmux"
	return v
}
