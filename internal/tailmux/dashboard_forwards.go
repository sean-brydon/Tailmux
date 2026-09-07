package tailmux

import (
	"fmt"
	"net"
	"sort"
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
	lines := []string{dashTitle.Render("▎ Box addresses")}
	targets := []string{}
	for box := range m.snapshot.Loopbacks {
		if target == "local" || target == box {
			targets = append(targets, box)
		}
	}
	sort.Strings(targets)
	for _, box := range targets {
		binding := m.snapshot.Loopbacks[box]
		lines = append(lines, dashTitle.Render(box)+" · "+binding.Address,
			"  "+strings.Join(binding.Names, ", ")+" · configured")
	}
	if len(targets) == 0 {
		lines = append(lines, dashMuted.Render("No isolated address configured · L Setup loopback"))
	}
	lines = append(lines, "", dashTitle.Render(fmt.Sprintf("▎ Ports & URLs · %d groups", len(forwards))))
	if len(forwards) == 0 {
		return append(lines, dashMuted.Render("No Tailmux forwards assigned · f creates one · L Setup loopback"))
	}
	for _, f := range forwards {
		name := f.Spec.Save
		if name == "" {
			name = f.Spec.Name
		}
		if name == "" {
			name = f.Spec.Target
		}
		state := f.State
		if state == "" {
			state = "unknown"
		}
		lines = append(lines, dashTitle.Render(cleanDashboardText(name))+" · "+dashBadge(state, state != "running"))
		if target == "local" {
			lines = append(lines, dashMuted.Render("To "+cleanDashboardText(f.Spec.Target)))
		}
		ports := append([]PortMap(nil), f.Spec.Ports...)
		sort.Slice(ports, func(i, j int) bool { return ports[i].Local < ports[j].Local })
		for i := 0; i < len(ports); {
			first, last := ports[i], ports[i]
			j := i + 1
			for j < len(ports) && ports[j].Local == last.Local+1 && ports[j].Remote == last.Remote+1 {
				last = ports[j]
				j++
			}
			local, remote := strconv.Itoa(first.Local), strconv.Itoa(first.Remote)
			if last.Local != first.Local {
				local += "-" + strconv.Itoa(last.Local)
				remote += "-" + strconv.Itoa(last.Remote)
			}
			address := f.Spec.listenerAddress()
			lines = append(lines, fmt.Sprintf("  %s:%s → remote localhost:%s", address, local, remote))
			if f.Spec.Name != "" {
				url := "http://" + net.JoinHostPort(cleanDashboardText(f.Spec.Name), strconv.Itoa(first.Local))
				if last.Local != first.Local {
					url += " … :" + strconv.Itoa(last.Local)
				}
				lines = append(lines, "  Local  "+url)
			} else {
				lines = append(lines, dashMuted.Render("  TCP endpoint · protocol passed through"))
			}
			i = j
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
	return append(lines, dashMuted.Render("2 Forwards · manage routes"))
}

func (m dashboardModel) boxForwardDetails(target string) string {
	return strings.Join(m.boxForwardLines(target), "\n")
}
