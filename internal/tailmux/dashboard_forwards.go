package tailmux

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// The local machine owns every listener. Remote box views show only their routes.
func (m dashboardModel) boxForwardLines(target string) []string {
	forwards := []ForwardInfo{}
	for _, f := range m.snapshot.Forwards {
		if target == "local" || f.Spec.Target == target {
			forwards = append(forwards, f)
		}
	}
	lines := []string{dashMuted.Render(fmt.Sprintf("PORTS & URLS · %d forward groups", len(forwards)))}
	if len(forwards) == 0 {
		return append(lines, dashMuted.Render("No Tailmux forwards assigned · f creates one"))
	}
	for _, f := range forwards {
		name := f.Spec.Save
		if name == "" {
			name = f.ID
		}
		state := f.State
		if state == "" {
			state = "unknown"
		}
		lines = append(lines, dashTitle.Render(cleanDashboardText(name))+" · "+dashBadge(state, state != "running"))
		if target == "local" {
			lines = append(lines, dashMuted.Render("To "+cleanDashboardText(f.Spec.Target)))
		}
		for _, p := range f.Spec.Ports {
			address := f.Spec.BindAddress
			if address == "" {
				address = "127.0.0.1"
			}
			bind := net.JoinHostPort(address, strconv.Itoa(p.Local))
			lines = append(lines, fmt.Sprintf("  %s → remote localhost:%d", bind, p.Remote))
			if f.Spec.Name != "" {
				lines = append(lines, "  Local  http://"+net.JoinHostPort(cleanDashboardText(f.Spec.Name), strconv.Itoa(p.Local)))
			} else {
				lines = append(lines, dashMuted.Render("  TCP endpoint · protocol passed through"))
			}
		}
		if f.Spec.Public != nil {
			publicState := f.PublicState
			if publicState == "" {
				publicState = "unknown"
			}
			lines = append(lines, "  Public "+cleanDashboardText(f.Spec.Public.URL),
				dashMuted.Render("  "+cleanDashboardText(f.Spec.Public.Provider)+" · "+publicState))
		}
		for _, message := range []string{f.Error, f.PublicError} {
			if message != "" {
				lines = append(lines, dashBadge("  "+cleanDashboardText(message), true))
			}
		}
	}
	return append(lines, dashMuted.Render("Configured endpoints; stopped/error routes may not be bound."),
		dashMuted.Render("f create · 2 Forwards manages routes · p inspects remote listeners"))
}

func (m dashboardModel) boxForwardDetails(target string) string {
	return strings.Join(m.boxForwardLines(target), "\n")
}
