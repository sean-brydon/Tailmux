package tailmux

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func TestDashboardPublicFormRequiresExplicitProviderAndURL(t *testing.T) {
	m := dashboardModel{}
	m.startForm("forward")
	m.form.fields[0].value = "lab/worker"
	m.form.fields[4].value = "ngrok"
	if _, err := dashboardFormArgs(*m.form); err == nil {
		t.Fatal("public forward without explicit URL accepted")
	}
	m.form.fields[5].value = "https://preview.example.com"
	args, err := dashboardFormArgs(*m.form)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(args, " ") != "forward lab/worker 3000 --ngrok --url https://preview.example.com" {
		t.Fatal(args)
	}
	m.form.fields[4].value = "cloudflare"
	if _, err := dashboardFormArgs(*m.form); err == nil {
		t.Fatal("missing Cloudflare tunnel accepted")
	}
	m.form.fields[6].value = "preview"
	if _, err := dashboardFormArgs(*m.form); err != nil {
		t.Fatal(err)
	}
}
func TestDashboardEscapeCancelsWithoutAction(t *testing.T) {
	m := dashboardModel{}
	m.startForm("account")
	m.form.fields[0].value = "work"
	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd != nil || next.(dashboardModel).form != nil {
		t.Fatal("cancel triggered work or retained form")
	}
}
func TestDashboardRendersWithinTerminal(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {80, 24}, {40, 12}, {24, 8}, {1, 1}} {
		m := dashboardModel{width: size[0], height: size[1], snapshot: statusSnapshot{Boxes: []statusBox{{Target: "local", State: "this machine"}}}}
		view := m.View()
		if !view.AltScreen || lipgloss.Width(view.Content) > size[0] || lipgloss.Height(view.Content) > size[1] {
			t.Fatalf("view exceeds %v: %dx%d", size, lipgloss.Width(view.Content), lipgloss.Height(view.Content))
		}
	}
}

func TestDashboardHostFormPrefillsSavedSettings(t *testing.T) {
	cfg := Config{Profiles: []string{"lab"}, Hosts: map[string]Host{
		"lab/build": {Profile: "lab", Address: "bastion.internal", User: "builder", Port: 2207},
	}}
	m := dashboardModel{cfg: cfg, section: 0, snapshot: statusSnapshot{Boxes: []statusBox{{Target: "lab/build", Saved: true}}}}
	m.startForm("host")
	got := []string{}
	for _, field := range m.form.fields {
		got = append(got, field.value)
	}
	if strings.Join(got, "|") != "lab/build|builder|bastion.internal|2207" {
		t.Fatalf("saved SSH settings were not preserved: %v", got)
	}
}

func TestDashboardFormKeepsFocusedFieldVisible(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {40, 12}} {
		m := dashboardModel{width: size[0], height: size[1]}
		m.startForm("forward")
		for focus, field := range m.form.fields {
			m.form.focus = focus
			view := m.View()
			for _, word := range strings.Fields(field.label) {
				if !strings.Contains(view.Content, word) {
					t.Fatalf("focused field %q hidden at %dx%d", field.label, size[0], size[1])
				}
			}
			if lipgloss.Width(view.Content) > size[0] || lipgloss.Height(view.Content) > size[1] {
				t.Fatalf("form exceeds %dx%d", size[0], size[1])
			}
		}
	}
}

func TestDashboardRefreshPreservesSelectionByIdentity(t *testing.T) {
	tests := []struct {
		name     string
		model    dashboardModel
		snapshot statusSnapshot
		want     string
	}{
		{
			name:     "box",
			model:    dashboardModel{section: 0, cursor: 1, snapshot: statusSnapshot{Boxes: []statusBox{{Target: "local"}, {Target: "lab/b"}}}},
			snapshot: statusSnapshot{Boxes: []statusBox{{Target: "lab/a"}, {Target: "local"}, {Target: "lab/b"}}},
			want:     "lab/b",
		},
		{
			name:     "forward",
			model:    dashboardModel{section: 1, cursor: 1, snapshot: statusSnapshot{Forwards: []ForwardInfo{{ID: "a"}, {ID: "b"}}}},
			snapshot: statusSnapshot{Forwards: []ForwardInfo{{ID: "b"}, {ID: "a"}}},
			want:     "b",
		},
		{
			name:     "runtime",
			model:    dashboardModel{section: 2, cursor: 1, snapshot: statusSnapshot{Runtimes: []statusRuntime{{Target: "lab/a"}, {Target: "lab/b"}}}},
			snapshot: statusSnapshot{Runtimes: []statusRuntime{{Target: "lab/b"}, {Target: "lab/a"}}},
			want:     "lab/b",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next, _ := tt.model.Update(dashboardLoaded{snapshot: tt.snapshot, cfg: tt.model.cfg})
			got := next.(dashboardModel).selectedID()
			if got != tt.want {
				t.Fatalf("selection changed after refresh: got %q want %q", got, tt.want)
			}
		})
	}
}
func TestStatusWithoutDaemonDoesNotStartNetworking(t *testing.T) {
	dir := t.TempDir()
	s := collectStatus(dir, defaultConfig())
	if s.Daemon || len(s.Boxes) != 1 || s.Boxes[0].Target != "local" {
		t.Fatalf("unexpected offline status: %+v", s)
	}
	if c, err := connect(dir); err == nil {
		c.Close()
		t.Fatal("status started a daemon")
	}
}

func TestDashboardSearchCannotActOnHiddenBox(t *testing.T) {
	m := dashboardModel{snapshot: statusSnapshot{Boxes: []statusBox{{Target: "local"}, {Target: "work/server"}}}}
	next, _ := m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	m = next.(dashboardModel)
	next, _ = m.Update(tea.PasteMsg{Content: "no-such-host"})
	m = next.(dashboardModel)
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(dashboardModel)
	if m.target() != "" || len(m.matchingItems()) != 0 {
		t.Fatal("hidden box remained selected")
	}
	next, _ = m.Update(dashboardLoaded{snapshot: m.snapshot})
	m = next.(dashboardModel)
	if m.target() != "" {
		t.Fatal("refresh selected a hidden box")
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("no-match search opened terminal")
	}
}

func TestDashboardHideBoxPersistsAndRestores(t *testing.T) {
	dir := t.TempDir()
	if err := initConfig(dir); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := dashboardModel{dir: dir, cfg: cfg, cursor: 1, snapshot: statusSnapshot{Boxes: []statusBox{{Target: "local"}, {Target: "work/server"}}}}
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'h', Text: "h"})
	m = next.(dashboardModel)
	if cmd != nil || m.section != 0 || len(m.matchingItems()) != 1 || m.target() != "local" {
		t.Fatal("hide changed panel or left hidden box selected")
	}
	cfg, err = loadConfig(dir)
	if err != nil || !cfg.HiddenBoxes["work/server"] {
		t.Fatalf("hidden preference lost: %+v %v", cfg, err)
	}
	reopened := dashboardModel{cfg: cfg, snapshot: m.snapshot}
	if len(reopened.matchingItems()) != 1 {
		t.Fatal("reopened dashboard showed hidden box")
	}
	next, _ = m.Update(tea.KeyPressMsg{Code: 'H', Text: "H"})
	m = next.(dashboardModel)
	if len(m.matchingItems()) != 2 {
		t.Fatal("show hidden failed")
	}
	m.cursor = 1
	next, _ = m.Update(tea.KeyPressMsg{Code: 'h', Text: "h"})
	m = next.(dashboardModel)
	cfg, err = loadConfig(dir)
	if err != nil || cfg.HiddenBoxes["work/server"] {
		t.Fatalf("restore failed: %+v %v", cfg, err)
	}
	if len(m.snapshot.Boxes) != 2 {
		t.Fatal("hide deleted inventory")
	}
}

func TestDashboardHideKeepsNearbySelection(t *testing.T) {
	for _, section := range []int{0, 4} {
		t.Run([]string{"Boxes", "", "", "", "Monitor"}[section], func(t *testing.T) {
			dir := t.TempDir()
			if err := initConfig(dir); err != nil {
				t.Fatal(err)
			}
			cfg, err := loadConfig(dir)
			if err != nil {
				t.Fatal(err)
			}
			m := dashboardModel{dir: dir, cfg: cfg, section: section, cursor: 2,
				snapshot: statusSnapshot{Boxes: []statusBox{{Target: "local"}, {Target: "work/a"}, {Target: "work/b"}, {Target: "work/c"}}},
				monitor:  MonitorSnapshot{Boxes: []BoxMonitor{{Target: "local"}, {Target: "work/a"}, {Target: "work/b"}, {Target: "work/c"}}},
			}
			press := func(key string) {
				next, _ := m.Update(tea.KeyPressMsg{Code: rune(key[0]), Text: key})
				m = next.(dashboardModel)
			}
			press("h")
			if m.target() != "work/c" {
				t.Fatalf("hide middle: selected %q", m.target())
			}
			press("h")
			if m.target() != "work/a" {
				t.Fatalf("hide last: selected %q", m.target())
			}
			press("H")
			if m.target() != "work/a" {
				t.Fatalf("show hidden moved selection: %q", m.target())
			}
			m.cursor = 2
			press("h")
			if m.target() != "work/b" {
				t.Fatalf("restore moved selection: %q", m.target())
			}
			press("H")
			if m.target() != "work/b" {
				t.Fatalf("conceal moved visible selection: %q", m.target())
			}
			press("h")
			if m.target() != "work/a" {
				t.Fatalf("hidden successors selected: %q", m.target())
			}
		})
	}
}
