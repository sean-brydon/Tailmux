package tailmux

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestDashboardLayoutAcrossPanels(t *testing.T) {
	used := 42.0
	for _, size := range [][2]int{{40, 12}, {80, 24}, {100, 30}, {160, 50}} {
		for section := 0; section < 5; section++ {
			t.Run(fmt.Sprintf("%dx%d/%d", size[0], size[1], section), func(t *testing.T) {
				m := dashboardModel{width: size[0], height: size[1], section: section,
					monitor: MonitorSnapshot{Boxes: []BoxMonitor{{Target: "personal/box", Memory: &BoxMemory{Percent: 42, UsedBytes: 27 << 30, TotalBytes: 64 << 30}, Usage: []MonitorUsage{{Provider: "Codex", Window: "5h", UsedPercent: &used}}}}}}
				for i := 0; i < 40; i++ {
					m.monitor.Boxes[0].Sessions.Rows = append(m.monitor.Boxes[0].Sessions.Rows, MonitorSession{Source: "orca", Title: strings.Repeat("long project ", 10), State: "in-progress"})
				}
				v := m.View().Content
				if lipgloss.Width(v) > size[0] || lipgloss.Height(v) > size[1] {
					t.Fatal("layout exceeds terminal")
				}
				if section == 4 && size[0] >= 100 {
					if !strings.Contains(v, "ACCOUNT LIMITS") || !strings.Contains(v, "42%") {
						t.Fatal("quotas lost below long sessions")
					}
					if !strings.Contains(v, "q quit") {
						t.Fatal("footer clipped")
					}
				}
			})
		}
	}
}
