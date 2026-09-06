package tailmux

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type forwardGroup struct {
	info       ForwardInfo
	cancel     context.CancelFunc
	cleanup    func()
	transports []*http.Transport
}
type forwardListener struct {
	ln     net.Listener
	server *http.Server
	routes map[string]http.Handler
	owners map[string]bool
	raw    string
}
type forwardManager struct {
	mu        sync.Mutex
	ctx       context.Context
	dir       string
	groups    map[string]*forwardGroup
	listeners map[int]*forwardListener
}

func newForwardManager(ctx context.Context, dir string) *forwardManager {
	return &forwardManager{ctx: ctx, dir: dir, groups: map[string]*forwardGroup{}, listeners: map[int]*forwardListener{}}
}
func (m *forwardManager) list() []ForwardInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []ForwardInfo{}
	for _, g := range m.groups {
		out = append(out, g.info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (m *forwardManager) remove(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	g := m.groups[id]
	if g == nil {
		return fmt.Errorf("unknown forward %q", id)
	}
	m.release(g)
	delete(m.groups, id)
	return nil
}
func (m *forwardManager) release(g *forwardGroup) {
	g.cancel()
	g.cleanup()
	for _, t := range g.transports {
		t.CloseIdleConnections()
	}
	for _, p := range g.info.Spec.Ports {
		l := m.listeners[p.Local]
		if l == nil || !l.owners[g.info.ID] {
			continue
		}
		delete(l.routes, g.info.Spec.Name)
		delete(l.owners, g.info.ID)
		if len(l.owners) == 0 {
			l.ln.Close()
			if l.server != nil {
				l.server.Close()
			}
			delete(m.listeners, p.Local)
		}
	}
}
func (m *forwardManager) close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, g := range m.groups {
		m.release(g)
	}
	m.groups = map[string]*forwardGroup{}
}
func (m *forwardManager) add(s ForwardSpec) (ForwardInfo, error) {
	if err := s.validate(); err != nil {
		return ForwardInfo{}, err
	}
	cfg, err := loadConfig(m.dir)
	if err != nil {
		return ForwardInfo{}, err
	}
	h, err := cfg.host(s.Target)
	if err != nil {
		return ForwardInfo{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range s.Ports {
		if l := m.listeners[p.Local]; l != nil && (s.Name == "" || l.raw != "" || l.routes[s.Name] != nil) {
			return ForwardInfo{}, fmt.Errorf("local port %d already forwards this name or is reserved for TCP", p.Local)
		}
	}
	b := make([]byte, 6)
	if _, err = rand.Read(b); err != nil {
		return ForwardInfo{}, err
	}
	id := hex.EncodeToString(b)
	ctx, cancel := context.WithCancel(m.ctx)
	g := &forwardGroup{info: ForwardInfo{ID: id, Spec: s, State: "running"}, cancel: cancel, cleanup: func() {}}
	// Reserve every port before starting SSH; errors roll back only this group.
	for _, p := range s.Ports {
		l := m.listeners[p.Local]
		if l == nil {
			ln, e := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", p.Local))
			if e != nil {
				m.release(g)
				return ForwardInfo{}, e
			}
			l = &forwardListener{ln: ln, routes: map[string]http.Handler{}, owners: map[string]bool{}}
			m.listeners[p.Local] = l
		}
		l.owners[id] = true
	}
	dial, cleanup, done, err := startForwardTunnel(ctx, m.dir, s, h)
	g.cleanup = cleanup
	if err != nil {
		m.release(g)
		return ForwardInfo{}, err
	}
	for _, p := range s.Ports {
		l := m.listeners[p.Local]
		if s.Name == "" {
			l.raw = id
			go serveTCP(ctx, l.ln, p.Remote, dial)
		} else {
			groupDial := func(callCtx context.Context, port int) (net.Conn, error) {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				conn, err := dial(callCtx, port)
				if err != nil {
					return nil, err
				}
				stop := context.AfterFunc(ctx, func() { conn.Close() })
				return &forwardConn{Conn: conn, stop: stop}, nil
			}
			proxy, tr := httpForward(s, p, groupDial)
			g.transports = append(g.transports, tr)
			l.routes[s.Name] = proxy
			if l.server == nil {
				l.server = &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					name := r.Host
					if host, _, e := net.SplitHostPort(name); e == nil {
						name = host
					}
					m.mu.Lock()
					handler := l.routes[strings.ToLower(name)]
					m.mu.Unlock()
					if handler == nil {
						http.Error(w, "No Tailmux forward for this hostname", http.StatusMisdirectedRequest)
						return
					}
					handler.ServeHTTP(w, r)
				})}
				go l.server.Serve(l.ln)
			}
		}
	}
	m.groups[id] = g
	go func() {
		e := <-done
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.groups[id] != g || ctx.Err() != nil {
			return
		}
		m.release(g)
		g.info.State = "failed"
		g.info.Error = fmt.Sprintf("SSH tunnel stopped: %v", e)
	}()
	return g.info, nil
}
func serveTCP(ctx context.Context, ln net.Listener, port int, dial func(context.Context, int) (net.Conn, error)) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			up, err := dial(ctx, port)
			if err != nil {
				return
			}
			defer up.Close()
			stop := context.AfterFunc(ctx, func() { c.Close(); up.Close() })
			defer stop()
			done := make(chan struct{})
			go func() {
				io.Copy(up, c)
				if w, ok := up.(interface{ CloseWrite() error }); ok {
					w.CloseWrite()
				}
				close(done)
			}()
			io.Copy(c, up)
			c.Close()
			up.Close()
			<-done
		}()
	}
}
func startForwardTunnel(ctx context.Context, dir string, s ForwardSpec, h Host) (func(context.Context, int) (net.Conn, error), func(), <-chan error, error) {
	tmp, err := os.MkdirTemp("", "tailmux-fwd-")
	noop := func() {}
	if err != nil {
		return nil, noop, nil, err
	}
	cleanup := func() { os.RemoveAll(tmp) }
	exe, err := os.Executable()
	if err != nil {
		return nil, cleanup, nil, err
	}
	args := sshOptions(exe, dir, s.Target, h)
	args = append(args, "-N", "-o", "BatchMode=yes", "-o", "ExitOnForwardFailure=yes", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3")
	sockets := map[int]string{}
	for _, p := range s.Ports {
		if sockets[p.Remote] != "" {
			continue
		}
		path := filepath.Join(tmp, fmt.Sprint(p.Remote))
		sockets[p.Remote] = path
		args = append(args, "-L", path+":127.0.0.1:"+fmt.Sprint(p.Remote))
	}
	args = append(args, h.Address)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	logPath := filepath.Join(tmp, "ssh.log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return nil, cleanup, nil, err
	}
	cmd.Stderr = log
	if err = cmd.Start(); err != nil {
		log.Close()
		return nil, cleanup, nil, err
	}
	done := make(chan error, 1)
	go func() { e := cmd.Wait(); log.Close(); done <- e }()
	timer := time.NewTimer(45 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case e := <-done:
			data, _ := os.ReadFile(logPath)
			return nil, cleanup, nil, fmt.Errorf("SSH forward failed: %v: %s", e, strings.TrimSpace(string(data)))
		case <-ctx.Done():
			return nil, cleanup, nil, ctx.Err()
		case <-timer.C:
			return nil, cleanup, nil, fmt.Errorf("SSH forward timed out")
		case <-tick.C:
			ready := true
			for _, path := range sockets {
				if _, e := os.Stat(path); e != nil {
					ready = false
				}
			}
			if ready {
				return func(ctx context.Context, p int) (net.Conn, error) {
					return (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "unix", sockets[p])
				}, cleanup, done, nil
			}
		}
	}
}
func forwardRPC(dir string, req request) ([]ForwardInfo, error) {
	if err := ensureDaemon(dir); err != nil {
		return nil, err
	}
	c, err := connect(dir)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(60 * time.Second))
	if err = json.NewEncoder(c).Encode(req); err != nil {
		return nil, err
	}
	var res response
	if err = readLine(bufio.NewReader(c), &res); err != nil {
		return nil, err
	}
	if res.Kind != "forwards" {
		return nil, fmt.Errorf("%s", res.Message)
	}
	return res.Forwards, nil
}
func forwardCLI(dir string, args []string) error {
	req := request{Op: args[0]}
	asJSON := false
	switch args[0] {
	case "forward":
		if len(args) < 3 {
			return fmt.Errorf("usage: tailmux forward <host> <ports...> [--name NAME] [--no-rewrite] [--json]")
		}
		s := ForwardSpec{Target: args[1], Rewrite: true}
		var ports []string
		for i := 2; i < len(args); i++ {
			switch {
			case args[i] == "--name":
				i++
				if i == len(args) {
					return fmt.Errorf("--name needs a hostname")
				}
				s.Name = strings.ToLower(args[i])
			case strings.HasPrefix(args[i], "--name="):
				s.Name = strings.ToLower(strings.TrimPrefix(args[i], "--name="))
			case args[i] == "--no-rewrite":
				s.Rewrite = false
			case args[i] == "--json":
				asJSON = true
			default:
				ports = append(ports, args[i])
			}
		}
		var err error
		s.Ports, err = parsePorts(ports)
		if err != nil {
			return err
		}
		if err = s.validate(); err != nil {
			return err
		}
		req.Forward = &s
	case "forwards":
		if len(args) == 2 && args[1] == "--json" {
			asJSON = true
		} else if len(args) != 1 {
			return fmt.Errorf("usage: tailmux forwards [--json]")
		}
	case "unforward":
		if len(args) != 2 {
			return fmt.Errorf("usage: tailmux unforward <id>")
		}
		req.Target = args[1]
	}
	infos, err := forwardRPC(dir, req)
	if err != nil {
		return err
	}
	if asJSON {
		if infos == nil {
			infos = []ForwardInfo{}
		}
		return json.NewEncoder(os.Stdout).Encode(infos)
	}
	if args[0] == "unforward" {
		fmt.Println("Forward stopped:", req.Target)
		return nil
	}
	if len(infos) == 0 {
		fmt.Println("No active forwards")
	}
	for _, g := range infos {
		for _, p := range g.Spec.Ports {
			name := g.Spec.Name
			if name == "" {
				name = "localhost"
			}
			fmt.Printf("%s  %s  %s:%d → %s localhost:%d\n", g.ID, g.State, name, p.Local, g.Spec.Target, p.Remote)
		}
		if g.Error != "" {
			fmt.Println(g.Error)
		}
		if args[0] == "forward" && g.Spec.Name != "" && !strings.HasSuffix(g.Spec.Name, ".localhost") && g.Spec.Name != "localhost" {
			fmt.Printf("DNS required: map %s to 127.0.0.1 (for example in /etc/hosts).\n", g.Spec.Name)
		}
	}
	return nil
}

type forwardConn struct {
	net.Conn
	stop func() bool
}

func (c *forwardConn) Close() error { c.stop(); return c.Conn.Close() }
