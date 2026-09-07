package tailmux

import (
	"strings"
	"testing"
)

func TestBoxForwardDetailsScopeAndEndpoints(t *testing.T) {
	m := dashboardModel{snapshot: statusSnapshot{Forwards: []ForwardInfo{
		{ID: "web", State: "running", Spec: ForwardSpec{Target: "personal/dev", Name: "dev.localhost", Ports: []PortMap{{Local: 13000, Remote: 3000}}, Public: &PublicForward{Provider: "cloudflare", URL: "https://demo.example.com"}}, PublicState: "failed", PublicError: "connector stopped"},
		{ID: "db", State: "stopped", Spec: ForwardSpec{Target: "work/box", Ports: []PortMap{{Local: 5432, Remote: 5432}}}},
	}}}
	view := m.boxForwardDetails("personal/dev")
	for _, want := range []string{"127.0.0.1:13000 → remote localhost:3000", "http://dev.localhost:13000", "https://demo.example.com", "connector stopped"} {
		if !strings.Contains(view, want) {
			t.Fatal("missing", want)
		}
	}
	if strings.Contains(view, "5432") {
		t.Fatal("another box's forward leaked")
	}
	local := m.boxForwardDetails("local")
	if !strings.Contains(local, "5432") || !strings.Contains(local, "stopped") || strings.Contains(local, "http://127.0.0.1:5432") {
		t.Fatal("incorrect raw TCP display")
	}
}
