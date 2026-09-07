package tailmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/term"
)

var dashAccent = lipgloss.Color("#8BD5CA")
var dashMuted = lipgloss.NewStyle().Foreground(lipgloss.Color("#9399B2"))
var dashTitle = lipgloss.NewStyle().Bold(true).Foreground(dashAccent)
var dashSelected = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#181926")).Background(dashAccent)

type dashboardLoaded struct {
	snapshot statusSnapshot
	cfg      Config
	err      error
}
type dashboardMonitorLoaded struct {
	snapshot MonitorSnapshot
	err      error
}
type dashboardTick struct{}
type dashboardResult struct {
	output string
	err    error
}
type dashboardField struct{ label, value string }
type dashboardForm struct {
	kind   string
	fields []dashboardField
	focus  int
}
type dashboardModel struct {
	monitor                        MonitorSnapshot
	monitorLoading                 bool
	monitorContext                 context.Context
	showHidden                     bool
	filter                         string
	searching                      bool
	dir                            string
	cfg                            Config
	snapshot                       statusSnapshot
	section, cursor, width, height int
	loading, busy                  bool
	message, output                string
	scroll                         int
	form                           *dashboardForm
}

func interactiveTerminal() bool {
	return term.IsTerminal(os.Stdin.Fd()) && term.IsTerminal(os.Stdout.Fd())
}
func dashboardCLI(dir string, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: tailmux dashboard")
	}
	if !interactiveTerminal() {
		return fmt.Errorf("dashboard needs an interactive terminal; use tailmux status --json or tailmux help")
	}
	cfg, err := loadConfig(dir)
	if errors.Is(err, os.ErrNotExist) {
		if err = initConfig(dir); err == nil {
			cfg, err = loadConfig(dir)
		}
	}
	if err != nil {
		return err
	}
	monitorCtx, cancelMonitor := context.WithCancel(context.Background())
	defer cancelMonitor()
	m := dashboardModel{section: 4, monitorLoading: true, monitorContext: monitorCtx, dir: dir, cfg: cfg, width: 100, height: 30, loading: true, snapshot: statusSnapshot{Boxes: []statusBox{{Target: "local", State: "this machine"}}}, message: "Welcome. Choose a box, or add an account with a."}
	_, err = tea.NewProgram(m).Run()
	return err
}
func dashboardRefresh(dir string) tea.Cmd {
	return func() tea.Msg {
		cfg, err := loadConfig(dir)
		if err != nil {
			return dashboardLoaded{err: err}
		}
		return dashboardLoaded{snapshot: collectStatus(dir, cfg), cfg: cfg}
	}
}
func dashboardTimer() tea.Cmd {
	return tea.Tick(15*time.Second, func(time.Time) tea.Msg { return dashboardTick{} })
}
func dashboardMonitorRefresh(parent context.Context, dir string, previous MonitorSnapshot) tea.Cmd {
	return func() tea.Msg {
		if parent == nil {
			parent = context.Background()
		}
		ctx, cancel := context.WithTimeout(parent, 25*time.Second)
		defer cancel()
		cfg, err := loadConfig(dir)
		if err != nil {
			return dashboardMonitorLoaded{err: err}
		}
		return dashboardMonitorLoaded{snapshot: collectMonitor(ctx, dir, cfg, previous)}
	}
}
func (m dashboardModel) Init() tea.Cmd {
	return tea.Batch(dashboardRefresh(m.dir), dashboardMonitorRefresh(m.monitorContext, m.dir, m.monitor), dashboardTimer())
}
func (m dashboardModel) run(args []string, interactive bool) tea.Cmd {
	exe, err := os.Executable()
	if err != nil {
		return func() tea.Msg { return dashboardResult{err: err} }
	}
	if interactive {
		cmd := exec.Command(exe, args...)
		cmd.Env = append(os.Environ(), "TAILMUX_HOME="+m.dir)
		return tea.ExecProcess(cmd, func(err error) tea.Msg {
			return dashboardResult{err: err, output: "Returned from tailmux " + strings.Join(args, " ")}
		})
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, exe, args...)
		cmd.Env = append(os.Environ(), "TAILMUX_HOME="+m.dir)
		out, err := cmd.CombinedOutput()
		if ctx.Err() != nil {
			err = fmt.Errorf("command timed out; check tailmux status before retrying")
		}
		return dashboardResult{output: string(out), err: err}
	}
}
func (m dashboardModel) target() string {
	if m.section == 4 && m.cursor < len(m.monitor.Boxes) {
		return m.monitor.Boxes[m.cursor].Target
	}
	if m.section == 2 && m.cursor < len(m.snapshot.Runtimes) {
		return m.snapshot.Runtimes[m.cursor].Target
	}
	if m.section == 0 && m.cursor < len(m.snapshot.Boxes) {
		return m.snapshot.Boxes[m.cursor].Target
	}
	return ""
}
func (m dashboardModel) selectedID() string {
	if target := m.target(); target != "" {
		return target
	}
	if m.section == 1 && m.cursor < len(m.snapshot.Forwards) {
		return m.snapshot.Forwards[m.cursor].ID
	}
	return strconv.Itoa(m.cursor)
}
func (m dashboardModel) matchingItems() []int {
	indices := []int{}
	for i := 0; i < m.count(); i++ {
		candidate := m
		candidate.cursor = i
		if (m.section == 0 || m.section == 4) && !m.showHidden && m.cfg.HiddenBoxes[candidate.target()] {
			continue
		}
		if m.filter == "" || strings.Contains(strings.ToLower(candidate.selectedID()), strings.ToLower(m.filter)) {
			indices = append(indices, i)
		}
	}
	return indices
}
func (m *dashboardModel) matchFirst() {
	indices := m.matchingItems()
	if len(indices) > 0 {
		m.cursor = indices[0]
	} else {
		m.cursor = m.count()
	}
}

// keepNearbySelection retains a visible selection, otherwise selects the next
// visible box (or the previous one when hiding the last box).
func (m *dashboardModel) keepNearbySelection() {
	previous := m.cursor
	indices := m.matchingItems()
	m.cursor = m.count()
	if len(indices) > 0 {
		m.cursor = indices[len(indices)-1]
		for _, index := range indices {
			if index >= previous {
				m.cursor = index
				break
			}
		}
	}
	if m.cursor != previous {
		m.scroll = 0
	}
}
func (m *dashboardModel) moveSelection(delta int) {
	m.scroll = 0
	indices := m.matchingItems()
	if len(indices) == 0 {
		return
	}
	pos := 0
	for i, v := range indices {
		if v == m.cursor {
			pos = i
			break
		}
	}
	pos = max(0, min(len(indices)-1, pos+delta))
	m.cursor = indices[pos]
}
func (m dashboardModel) count() int {
	switch m.section {
	case 4:
		return len(m.monitor.Boxes)
	case 0:
		return len(m.snapshot.Boxes)
	case 1:
		return len(m.snapshot.Forwards)
	case 2:
		return len(m.snapshot.Runtimes)
	default:
		return 4
	}
}
func (m *dashboardModel) startForm(kind string) {
	target := m.target()
	if target == "local" {
		target = ""
	}
	f := &dashboardForm{kind: kind}
	switch kind {
	case "account":
		f.fields = []dashboardField{{"Profile name", ""}}
	case "host":
		f.fields = []dashboardField{{"Target (profile/host)", target}, {"SSH user", ""}, {"Address (optional)", ""}, {"SSH port", "22"}}
		if target != "" {
			if h, err := m.cfg.host(target); err == nil {
				f.fields[1].value = h.User
				f.fields[2].value = h.Address
				f.fields[3].value = strconv.Itoa(h.Port)
			}
		}
	case "orca":
		localPort, remotePort := 16768, 6768
		routes, _ := readOrcaRoutes(m.dir)
		if route, ok := routes[target]; ok {
			localPort, remotePort = route.LocalPort, route.RemotePort
		} else {
			used := map[int]bool{}
			for _, route := range routes {
				used[route.LocalPort] = true
			}
			for _, forward := range m.snapshot.Forwards {
				for _, p := range forward.Spec.Ports {
					used[p.Local] = true
				}
			}
			for used[localPort] && localPort < 65535 {
				localPort++
			}
		}
		f.fields = []dashboardField{{"Target (profile/host)", target}, {"Unique local port", strconv.Itoa(localPort)}, {"Remote runtime port", strconv.Itoa(remotePort)}, {"Mode (plan/apply)", "plan"}}
	case "forward":
		f.fields = []dashboardField{{"Target (profile/host)", target}, {"Ports (local:remote or range)", "3000"}, {"Local HTTP name (optional)", ""}, {"Save as (optional)", ""}, {"Provider (blank/cloudflare/ngrok)", ""}, {"Public HTTPS URL (optional)", ""}, {"Cloudflare tunnel name (optional)", ""}}
	}
	m.form = f
	m.output = ""
	m.scroll = 0
}

// Examples are presentation only: an empty optional field stays empty on submit.
func dashboardFieldGuide(f dashboardForm, index int) (placeholder, hint string) {
	if f.kind != "forward" {
		return "", ""
	}
	host := "devbox"
	if target := strings.TrimSpace(f.fields[0].value); target != "" {
		parts := strings.Split(target, "/")
		candidate := strings.ReplaceAll(parts[len(parts)-1], "_", "-")
		if safeName.MatchString(candidate) {
			host = candidate
		}
	}
	switch index {
	case 0:
		return "personal/devbox", "Use a box from a connected profile, for example personal/devbox."
	case 1:
		return "3000, 3000-3010, or 13000:3000", "Ranges work locally. Public URLs publish exactly one port; use 13000:3000 if local port 3000 is busy."
	case 2:
		if strings.TrimSpace(f.fields[4].value) != "" {
			return "leave blank for the public hostname", "With Cloudflare/ngrok, leave this blank: the public URL supplies the hostname."
		}
		return host + ".test", "Private named routes need `tailmux loopback setup <target> --name <name>` first. Each box gets its own 127.77.x.y address; .localhost cannot provide that isolation. Blank means raw TCP on localhost."
	case 3:
		return host + "-web", "Optional saved name. Restores when networking starts; unforward removes it."
	case 4:
		return "leave blank, cloudflare, or ngrok", "Blank keeps the forward private. Cloudflare needs a locally managed named tunnel and DNS route; ngrok needs an authenticated account/domain."
	case 5:
		if strings.TrimSpace(f.fields[4].value) == "ngrok" {
			return "https://your-domain.ngrok-free.app", "Use the actual domain assigned to your ngrok account. Required only when a provider is selected."
		}
		return "https://preview.example.com", "Use a domain you control and have configured at your provider. This is the HTTPS origin used for redirects; no path is allowed."
	case 6:
		return "tailmux-preview", "Cloudflare setup (run once in a separate terminal):\ncloudflared tunnel login\ncloudflared tunnel create tailmux-preview\ncloudflared tunnel route dns tailmux-preview preview.example.com\nUse that tunnel name and hostname above. Leave blank for ngrok or private forwarding."
	}
	return "", ""
}

func dashboardFormArgs(f dashboardForm) ([]string, error) {
	values := make([]string, len(f.fields))
	for i, v := range f.fields {
		values[i] = strings.TrimSpace(v.value)
	}
	switch f.kind {
	case "account":
		if !safeName.MatchString(values[0]) {
			return nil, fmt.Errorf("enter a profile name using letters, numbers, dots, underscores or hyphens")
		}
		return []string{"login", values[0]}, nil
	case "host":
		if !strings.Contains(values[0], "/") {
			return nil, fmt.Errorf("use profile/host for the target")
		}
		args := []string{"hosts", "add", values[0], "--port", values[3]}
		if values[1] != "" {
			args = append(args, "--user", values[1])
		}
		if values[2] != "" {
			args = append(args, "--address", values[2])
		}
		return args, nil
	case "orca":
		if values[0] == "" {
			return nil, fmt.Errorf("select a remote host")
		}
		for _, v := range values[1:3] {
			p, err := strconv.Atoi(v)
			if err != nil || p < 1 || p > 65535 {
				return nil, fmt.Errorf("ports must be between 1 and 65535")
			}
		}
		args := []string{"setup", "orca", values[0], "--local-port", values[1], "--remote-port", values[2]}
		if values[3] == "apply" {
			args = append(args, "--apply")
		} else if values[3] != "plan" {
			return nil, fmt.Errorf("mode must be plan or apply")
		}
		return args, nil

	case "forward":
		if values[0] == "" {
			return nil, fmt.Errorf("choose a remote target")
		}
		if _, err := parsePorts(strings.Fields(values[1])); err != nil {
			return nil, err
		}
		args := append([]string{"forward", values[0]}, strings.Fields(values[1])...)
		if values[2] != "" {
			args = append(args, "--name", values[2])
		}
		if values[3] != "" {
			args = append(args, "--save", values[3])
		}
		switch values[4] {
		case "":
			if values[5] != "" || values[6] != "" {
				return nil, fmt.Errorf("select cloudflare or ngrok for a public URL")
			}
		case "cloudflare":
			if values[6] == "" {
				return nil, fmt.Errorf("enter your configured Cloudflare tunnel name")
			}
			args = append(args, "--cloudflare", values[6])
		case "ngrok":
			args = append(args, "--ngrok")
		default:
			return nil, fmt.Errorf("provider must be blank, cloudflare or ngrok")
		}
		if values[4] != "" {
			if values[5] == "" {
				return nil, fmt.Errorf("enter the HTTPS URL configured in your provider account")
			}
			args = append(args, "--url", values[5])
		}
		return args, nil
	}
	return nil, fmt.Errorf("unknown form")
}
func (m dashboardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case dashboardMonitorLoaded:
		m.monitorLoading = false
		if msg.err != nil {
			m.message = msg.err.Error()
		} else {
			previous := m.selectedID()
			m.monitor = msg.snapshot
			if m.section == 4 {
				for i, box := range m.monitor.Boxes {
					if box.Target == previous {
						m.cursor = i
						break
					}
				}
			}
		}
	case dashboardLoaded:
		previous := m.selectedID()
		m.loading = false
		if msg.err != nil {
			m.message = msg.err.Error()
		} else {
			m.snapshot = msg.snapshot
			m.cfg = msg.cfg
		}
		for i := 0; i < m.count(); i++ {
			candidate := m
			candidate.cursor = i
			if candidate.selectedID() == previous {
				m.cursor = i
				break
			}
		}
		if m.cursor >= m.count() {
			m.cursor = max(0, m.count()-1)
		}
		if m.filter != "" || m.section == 0 {
			matches := m.matchingItems()
			selected := false
			for _, i := range matches {
				if i == m.cursor {
					selected = true
				}
			}
			if !selected {
				m.matchFirst()
			}
		}
	case dashboardTick:
		if !m.loading && !m.busy {
			m.loading = true
			cmds := []tea.Cmd{dashboardRefresh(m.dir), dashboardTimer()}
			if !m.monitorLoading {
				m.monitorLoading = true
				cmds = append(cmds, dashboardMonitorRefresh(m.monitorContext, m.dir, m.monitor))
			}
			return m, tea.Batch(cmds...)
		}
		return m, dashboardTimer()
	case dashboardResult:
		m.busy = false
		m.output = cleanDashboardText(msg.output)
		m.scroll = 0
		if msg.err != nil {
			m.message = "Action failed: " + msg.err.Error() + " · See details below"
		} else {
			m.message = "Done · r refreshes · Esc closes output"
		}
		m.loading = true
		return m, dashboardRefresh(m.dir)
	case tea.PasteMsg:
		if m.searching {
			m.filter += strings.Join(strings.Fields(cleanDashboardText(msg.Content)), " ")
			m.matchFirst()
		}
		if m.form != nil {
			m.form.fields[m.form.focus].value += strings.Join(strings.Fields(cleanDashboardText(msg.Content)), " ")
		}
	case tea.KeyPressMsg:
		key := msg.String()
		if key == "ctrl+c" {
			return m, tea.Quit
		}
		if m.searching {
			switch key {
			case "esc":
				m.searching = false
				m.filter = ""
				m.cursor = 0
			case "enter":
				m.searching = false
			case "backspace":
				r := []rune(m.filter)
				if len(r) > 0 {
					m.filter = string(r[:len(r)-1])
				}
				m.matchFirst()
			default:
				if msg.Text != "" {
					m.filter += cleanDashboardText(msg.Text)
					m.matchFirst()
				}
			}
			return m, nil
		}
		if m.form != nil {
			switch key {
			case "esc":
				m.form = nil
			case "tab", "down":
				m.form.focus = (m.form.focus + 1) % len(m.form.fields)
			case "shift+tab", "up":
				m.form.focus = (m.form.focus + len(m.form.fields) - 1) % len(m.form.fields)
			case "backspace":
				r := []rune(m.form.fields[m.form.focus].value)
				if len(r) > 0 {
					m.form.fields[m.form.focus].value = string(r[:len(r)-1])
				}
			case "ctrl+u":
				m.form.fields[m.form.focus].value = ""
			case "enter":
				args, err := dashboardFormArgs(*m.form)
				if err != nil {
					m.message = err.Error()
					return m, nil
				}
				interactive := m.form.kind == "account"
				m.form = nil
				m.busy = true
				m.message = "Running tailmux " + strings.Join(args, " ")
				return m, m.run(args, interactive)
			default:
				if msg.Text != "" {
					m.form.fields[m.form.focus].value += cleanDashboardText(msg.Text)
				}
			}
			return m, nil
		}
		if key == "q" {
			return m, tea.Quit
		}
		if m.busy {
			return m, nil
		}
		switch key {
		case "h":
			target := m.target()
			if (m.section != 0 && m.section != 4) || target == "" {
				m.message = "Select a box to hide or restore"
				return m, nil
			}
			hiding := !m.cfg.HiddenBoxes[target]
			if err := updateConfig(m.dir, func(c *Config) error {
				if c.HiddenBoxes == nil {
					c.HiddenBoxes = map[string]bool{}
				}
				if hiding {
					c.HiddenBoxes[target] = true
				} else {
					delete(c.HiddenBoxes, target)
				}
				return nil
			}); err != nil {
				m.message = "Could not save hidden boxes: " + err.Error()
				return m, nil
			}
			cfg, err := loadConfig(m.dir)
			if err != nil {
				m.message = err.Error()
				return m, nil
			}
			m.cfg = cfg
			if hiding {
				m.message = "Hidden " + target + " · H shows hidden boxes"
			} else {
				m.message = "Restored " + target
			}
			m.keepNearbySelection()
		case "H", "shift+h":
			m.showHidden = !m.showHidden
			m.keepNearbySelection()
			if m.showHidden {
				m.message = "Showing hidden boxes · h restores the selected box"
			} else {
				m.message = "Hidden boxes concealed · H shows them again"
			}
		case "/":
			m.searching = true
			m.filter = ""
			m.output = ""
		case "esc":
			m.filter = ""
			m.output = ""
			m.scroll = 0
		case "pgdown":
			m.scroll += max(1, m.height-12)
		case "pgup":
			m.scroll = max(0, m.scroll-max(1, m.height-12))
		case "tab", "right", "l":
			m.section = (m.section + 1) % 5
			m.cursor = 0
			m.filter = ""
			m.output = ""
		case "shift+tab", "left":
			m.section = (m.section + 4) % 5
			m.cursor = 0
			m.filter = ""
			m.output = ""
		case "0":
			m.section = 4
			m.cursor = 0
			m.filter = ""
			m.output = ""
		case "1", "2", "3", "4":
			m.section = int(key[0] - '1')
			m.cursor = 0
			m.filter = ""
			m.output = ""
		case "down", "j":
			m.moveSelection(1)
		case "up", "k":
			m.moveSelection(-1)
		case "r":
			if !m.loading {
				m.loading = true
				cmds := []tea.Cmd{dashboardRefresh(m.dir)}
				if !m.monitorLoading {
					m.monitorLoading = true
					cmds = append(cmds, dashboardMonitorRefresh(m.monitorContext, m.dir, m.monitor))
				}
				return m, tea.Batch(cmds...)
			}
		case "a":
			m.startForm("account")
		case "e":
			m.startForm("host")
		case "f":
			m.startForm("forward")
		case "u":
			m.startForm("orca")
		case "n":
			m.busy = true
			m.message = "Starting networking and restoring saved forwards"
			return m, func() tea.Msg { return dashboardResult{err: ensureDaemon(m.dir)} }
		case "enter", "t":
			if m.section == 3 {
				switch m.cursor {
				case 0:
					m.startForm("account")
				case 1:
					m.startForm("host")
				case 2:
					m.busy = true
					return m, m.run([]string{"terminal", "--default", "tmux"}, false)
				case 3:
					m.busy = true
					return m, m.run([]string{"terminal", "--default", "zellij"}, false)
				}
			} else if target := m.target(); target != "" {
				m.busy = true
				return m, m.run([]string{"terminal", target}, true)
			}
		case "p", "c", "i", "o", "s":
			target := m.target()
			if target == "" || target == "local" {
				m.message = "Select a remote box first"
				return m, nil
			}
			args := []string{}
			interactive := false
			switch key {
			case "p":
				args = []string{"ports", target, "--forward"}
				interactive = true
			case "c":
				args = []string{"setup", "check", target}
			case "i":
				args = []string{"setup", "install", target, "--tmux", "--ports"}
				interactive = true
			case "o":
				args = []string{"orca", "connect", target}
			case "s":
				args = []string{"orca", "status", target}
			}
			m.busy = true
			m.message = "Running tailmux " + strings.Join(args, " ")
			return m, m.run(args, interactive)
		case "v":
			if m.section == 1 && m.cursor < len(m.snapshot.Forwards) {
				name := m.snapshot.Forwards[m.cursor].Spec.Save
				if name == "" {
					m.message = "This forward is temporary; recreate it with f"
					return m, nil
				}
				m.busy = true
				return m, m.run([]string{"forward", "--resume", name}, false)
			}
		case "x":
			if m.section == 1 && m.cursor < len(m.snapshot.Forwards) {
				m.busy = true
				return m, m.run([]string{"unforward", m.snapshot.Forwards[m.cursor].ID}, false)
			}
		}
	}
	matches := m.matchingItems()
	found := false
	for _, i := range matches {
		if i == m.cursor {
			found = true
			break
		}
	}
	if !found {
		m.matchFirst()
	}
	return m, nil
}
func cleanDashboardText(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			return r
		}
		return -1
	}, s)
}
func dashBox(title, body string, width, height int, active bool) string {
	color := lipgloss.Color("#494D64")
	if active {
		color = dashAccent
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(color).Width(width).Height(height).Padding(0, 1).Render(dashTitle.Render(title) + "\n\n" + body)
}
func (m dashboardModel) items() []string {
	items := []string{}
	switch m.section {
	case 4:
		for _, box := range m.monitor.Boxes {
			state := memoryMeter(box.Memory, 10)
			if m.cfg.HiddenBoxes[box.Target] {
				state += " · hidden"
			}
			items = append(items, box.Target+"\n"+dashMuted.Render("  "+state)+"\n"+dashMuted.Render("  "+attentionSummary(box.Sessions)))
		}

	case 0:
		for _, b := range m.snapshot.Boxes {
			state := b.State
			if m.cfg.HiddenBoxes[b.Target] {
				state += " · hidden"
			}
			items = append(items, b.Target+"\n"+dashMuted.Render("  "+state))
		}
	case 1:
		for _, f := range m.snapshot.Forwards {
			name := f.ID
			if f.Spec.Save != "" {
				name = f.Spec.Save
			}
			items = append(items, name+"\n"+dashMuted.Render("  "+f.State+" · "+f.Spec.Target))
		}
	case 2:
		for _, r := range m.snapshot.Runtimes {
			items = append(items, r.Target+"\n"+dashMuted.Render("  "+r.State))
		}
	default:
		items = []string{"Add Tailscale account", "Save SSH host settings", "Use tmux by default", "Use Zellij by default"}
	}
	return items
}
func (m dashboardModel) details() string {
	if m.output != "" {
		lines := strings.Split(m.output, "\n")
		start := min(m.scroll, max(0, len(lines)-1))
		end := min(len(lines), start+max(3, m.height-13))
		return strings.Join(lines[start:end], "\n") + "\n\n" + dashMuted.Render("PgUp/PgDn scroll · Esc dismiss")
	}
	switch m.section {
	case 4:
		if m.cursor >= len(m.monitor.Boxes) {
			return "Monitoring saved boxes and this computer…\n\nAdd remote boxes with e in the Boxes panel."
		}
		box := m.monitor.Boxes[m.cursor]
		body := dashTitle.Render(box.Target) + "\n\n" + memoryMeter(box.Memory, 16)
		if box.MemoryError != "" {
			body += "\n" + box.MemoryError
		}
		body += "\n\n" + attentionSummary(box.Sessions) + "\n"
		for _, row := range box.Sessions.Rows {
			flag := " "
			if row.NeedsAttention {
				flag = "!"
			}
			body += fmt.Sprintf("\n%s %s · %s\n  %s", flag, row.Source+" "+row.Kind, cleanDashboardText(row.Title), cleanDashboardText(row.State))
			if row.AttentionReason != "" {
				body += " · " + cleanDashboardText(row.AttentionReason)
			}
		}
		for _, err := range box.Sessions.Errors {
			body += "\n\n" + err.Source + ": " + cleanDashboardText(err.Message)
		}
		body += "\n\n" + dashTitle.Render("Account usage")
		for _, usage := range box.Usage {
			body += "\n" + usageSummary(usage)
		}
		if box.UsageError != "" {
			body += "\n" + cleanDashboardText(box.UsageError)
		}
		body += "\n" + dashMuted.Render("Default Codex CLI login; accounts may be shared across boxes.\nClaude/other provider quotas are not exposed here.")
		body += "\n\n" + dashMuted.Render("Checked "+box.CheckedAt.Format("15:04:05")+" · r refreshes\nEnter terminal · p ports · h hide · H show hidden")
		lines := strings.Split(body, "\n")
		start := min(m.scroll, max(0, len(lines)-1))
		end := min(len(lines), start+max(3, m.height-13))
		return strings.Join(lines[start:end], "\n") + "\n" + dashMuted.Render("PgUp/PgDn scroll details")

	case 0:
		if m.cursor < len(m.snapshot.Boxes) {
			b := m.snapshot.Boxes[m.cursor]
			if b.Target == "local" {
				return dashTitle.Render("Your computer") + "\n\nOpen a local shell beside your remote boxes.\nNew splits stay on this computer.\n\n" + dashMuted.Render("Enter  Open terminal\nh      Hide / restore box\nH      Show hidden boxes\na      Add a Tailscale account\nn      Start networking / saved forwards\ne      Save a remote host\nf      Create a forward")
			}
			return dashTitle.Render(b.Target) + "\n\n" + b.State + "\n" + b.Address + "\n\n" + dashMuted.Render("Enter  Open terminal\nh      Hide / restore box\nH      Show hidden boxes\np      Pick a listening port\nf      Create a forward / public URL\nc      Check host prerequisites\ni      Install host prerequisites\ne      Edit SSH host settings\nu      Configure Orca service\no      Connect Orca runtime\ns      Verify Orca runtime")
		}
	case 1:
		if m.cursor < len(m.snapshot.Forwards) {
			f := m.snapshot.Forwards[m.cursor]
			body := dashTitle.Render(f.Spec.Target) + "\n\n" + forwardSummary(f) + "\n\nState: " + f.State
			if f.Spec.Public != nil {
				body += "\nPublic: " + f.Spec.Public.URL + "\nConnector: " + f.PublicState
			}
			if f.Error != "" {
				body += "\n\n" + f.Error
			}
			if f.PublicError != "" {
				body += "\n\n" + f.PublicError
			}
			return body + "\n\n" + dashMuted.Render("f  Create another forward\nv  Resume / retry a saved forward\nx  Stop and remove this forward")
		}
		return "No running forwards.\n\nPress f to map a remote port to this computer.\nAdd a local hostname, save it for later, or\nattach an existing public tunnel."
	case 2:
		if m.cursor < len(m.snapshot.Runtimes) {
			r := m.snapshot.Runtimes[m.cursor]
			return dashTitle.Render(r.Target) + fmt.Sprintf("\n\n%s\nLocal port: %d\n\n", r.State, r.LocalPort) + dashMuted.Render("s      Verify runtime and restore tunnel\no      Reconnect / pair runtime\nEnter  Open host terminal")
		}
		return "No paired Orca runtimes.\n\nSelect a remote box and press c to check setup,\nthen o to pair its running Orca service."
	case 3:
		backend := m.cfg.TerminalBackend
		if backend == "" {
			backend = "tmux"
		}
		body := dashTitle.Render("Workspace settings") + "\n\nTerminal: " + backend + "\nProfiles: " + strings.Join(m.cfg.Profiles, ", ") + "\n\nLocal tools\n"
		names := []string{}
		for name := range m.snapshot.Dependencies {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			state := "missing"
			if m.snapshot.Dependencies[name] {
				state = "installed"
			}
			body += fmt.Sprintf("  %-16s %s\n", name, state)
		}
		return body + "\n" + dashMuted.Render("Enter applies the selected setting.\nOptional tools are only needed for their features.")
	}
	return "Choose an item to see its details."
}
func (m dashboardModel) View() tea.View {
	width := max(1, m.width)
	height := max(1, m.height)
	if width < 40 || height < 12 {
		v := tea.NewView(lipgloss.NewStyle().MaxWidth(width).MaxHeight(height).Render("Tailmux needs at least 40 columns and 12 rows. Resize the terminal; q quits."))
		v.AltScreen = true
		return v
	}
	if m.form == nil {
		return m.polishedView()
	}
	tabs := []string{"1 Boxes", "2 Forwards", "3 Orca", "4 Setup", "0 Monitor"}
	for i := range tabs {
		if i == m.section {
			tabs[i] = dashSelected.Padding(0, 1).Render(tabs[i])
		} else {
			tabs[i] = dashMuted.Padding(0, 1).Render(tabs[i])
		}
	}
	daemon := "network stopped"
	if m.snapshot.Daemon {
		daemon = "network running"
	}
	if m.loading {
		daemon += " · refreshing"
	}
	if m.busy {
		daemon = "working…"
	}
	header := dashTitle.Render(" TAILMUX ") + dashMuted.Render(" / your machines, one workspace")
	stats := fmt.Sprintf(" %d boxes  ·  %d forwards  ·  %d Orca routes  ·  %s", len(m.snapshot.Boxes), len(m.snapshot.Forwards), len(m.snapshot.Runtimes), daemon)
	if m.searching || m.filter != "" {
		stats = " Search: " + m.filter + " · Enter keep filter · Esc clear"
	}
	if m.section == 4 {
		alerts := 0
		for _, box := range m.monitor.Boxes {
			for _, row := range box.Sessions.Rows {
				if row.NeedsAttention {
					alerts++
				}
			}
		}
		stats = fmt.Sprintf(" Saved-box monitor · %d boxes · %d alerts/updates · refresh every 15s", len(m.monitor.Boxes), alerts)
		if m.monitorLoading {
			stats += " · refreshing"
		}
	}
	if m.section == 0 || m.section == 4 {
		hidden := 0
		for _, b := range m.snapshot.Boxes {
			if m.cfg.HiddenBoxes[b.Target] {
				hidden++
			}
		}
		if hidden > 0 {
			stats += fmt.Sprintf(" · %d hidden (H toggles)", hidden)
		}
	}
	var body string
	if m.form != nil {
		lines := []string{}
		_, hint := dashboardFieldGuide(*m.form, m.form.focus)
		hintView := lipgloss.NewStyle().Width(max(1, width-10)).Render(hint)
		hintLines := 0
		if hint != "" && height >= 20 {
			hintLines = lipgloss.Height(hintView) + 1
		} else {
			hintView = ""
		}
		visible := max(1, (height-15-hintLines)/2)
		start := max(0, m.form.focus-visible+1)
		for i := start; i < min(len(m.form.fields), start+visible); i++ {
			f := m.form.fields[i]
			label := dashMuted.Render(f.label)
			value := f.value
			placeholder, _ := dashboardFieldGuide(*m.form, i)
			if value == "" && placeholder != "" {
				value = dashMuted.Italic(true).Render("e.g. " + placeholder)
			}
			if i == m.form.focus {
				label = dashTitle.Render(f.label)
				if f.value == "" {
					value = "▏" + value
				} else {
					value += "▏"
				}
			}
			lines = append(lines, label+"\n  "+value)
		}
		footer := "Tab next field · Ctrl+U clear · Enter submit · Esc cancel"
		if hintView != "" {
			footer = hintView + "\n\n" + footer
		}
		if m.form.kind == "orca" {
			footer += "\nPlan shows setup steps. Apply writes a disabled user service; firewall setup is separate."
		}
		body = dashBox(fmt.Sprintf("Configure %s · field %d/%d", m.form.kind, m.form.focus+1, len(m.form.fields)), strings.Join(lines, "\n")+"\n\n"+dashMuted.Render(footer), width-6, max(4, height-10), true)
	} else {
		items := m.items()
		list := []string{}
		visible := max(1, (height-12)/3)
		if m.section == 4 {
			visible = max(1, (height-12)/4)
		}
		indices := m.matchingItems()
		position := 0
		for pos, index := range indices {
			if index == m.cursor {
				position = pos
				break
			}
		}
		start := max(0, position-visible+1)
		for pos := start; pos < min(len(indices), start+visible); pos++ {
			i := indices[pos]
			item := items[i]
			if i == m.cursor {
				parts := strings.SplitN(item, "\n", 2)
				item = dashSelected.Render(" " + parts[0] + " ")
				if len(parts) > 1 {
					item += "\n" + parts[1]
				}
			}
			list = append(list, item)
		}
		if len(list) == 0 {
			list = []string{dashMuted.Render("Nothing here yet")}
		}
		if width < 85 {
			body = dashBox(tabs[m.section], strings.Join(list, "\n\n")+"\n\n"+m.details(), width-6, height-10, true)
		} else {
			left := min(38, width/3)
			body = lipgloss.JoinHorizontal(lipgloss.Top, dashBox("Navigate", strings.Join(list, "\n\n"), left, height-10, true), dashBox("Details", m.details(), width-left-10, height-10, false))
		}
	}
	message := m.message
	if len(m.snapshot.Errors) > 0 && m.output == "" && m.form == nil && !m.busy {
		if message != "" {
			message += " · "
		}
		message += "Status: " + m.snapshot.Errors[0]
	}
	content := header + "\n" + dashMuted.Render(stats) + "\n\n" + strings.Join([]string{tabs[4], tabs[0], tabs[1], tabs[2], tabs[3]}, " ") + "\n" + body + "\n" + dashMuted.Render(" "+message) + "\n" + dashMuted.Render(" 0–4 panels · / search · ↑↓ select · h hide · H show hidden · Enter open · q quit")
	// Bound every render to the terminal; long paths/errors must not scroll the screen.
	content = lipgloss.NewStyle().MaxWidth(width).MaxHeight(height).Render(content)
	v := tea.NewView(content)
	v.AltScreen = true
	v.WindowTitle = "Tailmux"
	return v
}
