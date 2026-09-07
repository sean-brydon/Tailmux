package tailmux

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func TestMonitorOnlyTargetsSavedBoxes(t *testing.T) {
	cfg := Config{Profiles: []string{"work", "personal"}, Hosts: map[string]Host{"work/worker": {Profile: "work", Address: "worker", Port: 22}}}
	got := monitorTargets(cfg)
	if strings.Join(got, ",") != "local,work/worker" {
		t.Fatal(got)
	}
}
func TestMonitorMemoryMeter(t *testing.T) {
	m := BoxMemory{TotalBytes: 64 << 30, UsedBytes: 32 << 30, Percent: 50}
	if got := memoryMeter(&m, 10); got != "[#####-----] 50% · 32.0 / 64.0 GiB" {
		t.Fatal(got)
	}
	if memoryMeter(nil, 10) != "RAM unavailable" {
		t.Fatal("unknown memory shown as zero")
	}
}
func TestMonitorDoesNotTreatIdleAsAttention(t *testing.T) {
	s := MonitorSessions{Rows: []MonitorSession{{State: "idle", AttentionKnown: false}}}
	if got := attentionSummary(s); strings.Contains(got, "need attention") || !strings.Contains(got, "unknown") {
		t.Fatal(got)
	}
	s.Rows = append(s.Rows, MonitorSession{State: "waiting_for_input", NeedsAttention: true, AttentionKnown: true})
	if got := attentionSummary(s); got != "! 1 alerts/updates" {
		t.Fatal(got)
	}
}
func TestMonitorPanelNavigationAndHiddenBoxes(t *testing.T) {
	m := dashboardModel{width: 100, height: 30, cfg: Config{HiddenBoxes: map[string]bool{"work/hidden": true}}, monitor: MonitorSnapshot{Boxes: []BoxMonitor{{Target: "local"}, {Target: "work/hidden"}, {Target: "work/visible"}}}}
	next, _ := m.Update(tea.KeyPressMsg{Code: '0', Text: "0"})
	m = next.(dashboardModel)
	if m.section != 4 || m.target() != "local" || len(m.matchingItems()) != 2 {
		t.Fatal("monitor panel did not honor hidden boxes")
	}
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = next.(dashboardModel)
	if m.target() != "work/visible" {
		t.Fatal(m.target())
	}
	if view := m.View(); lipgloss.Width(view.Content) > 100 || lipgloss.Height(view.Content) > 30 {
		t.Fatal("monitor exceeds terminal")
	}
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = next.(dashboardModel)
	if m.section != 0 {
		t.Fatal("tab must navigate from monitor to boxes")
	}
}
