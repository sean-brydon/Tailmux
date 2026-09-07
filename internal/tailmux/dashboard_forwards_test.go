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

func TestBoxAddressesAndPortRangesAreCompact(t *testing.T) {
	m := dashboardModel{snapshot: statusSnapshot{Loopbacks: loopbackConfig{"personal/dev": {Address: "127.77.1.2", Names: []string{"dev.test"}}}}}
	if !strings.Contains(m.boxForwardDetails("local"), "dev.test") {
		t.Fatal("configured address hidden without forwards")
	}
	ports := []PortMap{}
	for p := 3000; p <= 3015; p++ {
		ports = append(ports, PortMap{Local: p, Remote: p})
	}
	m.snapshot.Forwards = []ForwardInfo{{Spec: ForwardSpec{Target: "personal/dev", Name: "dev.test", BindAddress: "127.77.1.2", Ports: ports}, State: "running"}}
	view := m.boxForwardDetails("personal/dev")
	if !strings.Contains(view, "3000-3015") || strings.Count(view, "Local  http://") != 1 || len(strings.Split(view, "\n")) > 12 {
		t.Fatal("range not summarized", view)
	}
}
