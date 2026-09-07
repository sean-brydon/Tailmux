package tailmux

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

type forwardGroup struct {
	info       ForwardInfo
	cancel     context.CancelFunc
	cleanup    func()
	transports []*http.Transport
}

const savedForwardsFile = "forwards.json"

var errForwardRemoved = errors.New("saved forward was removed")

type forwardListener struct {
	ln     net.Listener
	server *http.Server
	routes map[string]http.Handler
	owners map[string]bool
	raw    string
	public bool
}
type forwardListenerKey struct {
	Address string
	Port    int
}
type forwardManager struct {
	mu           sync.Mutex
	ctx          context.Context
	cancel       context.CancelFunc
	dir          string
	groups       map[string]*forwardGroup
	listeners    map[forwardListenerKey]*forwardListener
	restored     chan struct{}
	restoreError string
}
type liveTunnel struct {
	mu      sync.RWMutex
	dial    func(context.Context, int) (net.Conn, error)
	cleanup func()
}
type liveProcess struct {
	mu      sync.Mutex
	cleanup func()
}

func forwardListenerConflicts(l *forwardListener, s ForwardSpec) bool {
	return l != nil && (s.Public != nil || l.public || s.Name == "" || l.raw != "" || l.routes[s.Name] != nil)
}

func (p *liveProcess) set(cleanup func()) { p.mu.Lock(); p.cleanup = cleanup; p.mu.Unlock() }
func (p *liveProcess) clear() {
	p.mu.Lock()
	cleanup := p.cleanup
	p.cleanup = nil
	p.mu.Unlock()
	if cleanup != nil {
		cleanup()
	}
}

func (t *liveTunnel) set(dial func(context.Context, int) (net.Conn, error), cleanup func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.dial, t.cleanup = dial, cleanup
}
func (t *liveTunnel) connect(ctx context.Context, port int) (net.Conn, error) {
	t.mu.RLock()
	dial := t.dial
	t.mu.RUnlock()
	if dial == nil {
		return nil, fmt.Errorf("SSH tunnel is reconnecting")
	}
	return dial(ctx, port)
}
func (t *liveTunnel) clear() {
	t.mu.Lock()
	cleanup := t.cleanup
	t.dial, t.cleanup = nil, nil
	t.mu.Unlock()
	if cleanup != nil {
		cleanup()
	}
}

func newForwardManager(ctx context.Context, dir string) *forwardManager {
	managerCtx, cancel := context.WithCancel(ctx)
	return &forwardManager{ctx: managerCtx, cancel: cancel, dir: dir, groups: map[string]*forwardGroup{}, listeners: map[forwardListenerKey]*forwardListener{}, restored: make(chan struct{})}
}
func (m *forwardManager) restore() {
	b, err := os.ReadFile(filepath.Join(m.dir, savedForwardsFile))
	if err != nil {
		if !os.IsNotExist(err) {
			m.mu.Lock()
			m.restoreError = err.Error()
			m.mu.Unlock()
		}
		close(m.restored)
		return
	}
	var specs []ForwardSpec
	if err = json.Unmarshal(b, &specs); err != nil {
		m.mu.Lock()
		m.restoreError = err.Error()
		m.mu.Unlock()
		close(m.restored)
		return
	}
	m.mu.Lock()
	type pendingRestore struct {
		spec  ForwardSpec
		group *forwardGroup
	}
	pending := make([]pendingRestore, 0, len(specs))
	for _, spec := range specs {
		_, cancel := context.WithCancel(m.ctx)
		id := "saved-" + spec.Save
		g := &forwardGroup{info: ForwardInfo{ID: id, Spec: spec, State: "restoring"}, cancel: cancel, cleanup: func() {}}
		m.groups[id] = g
		pending = append(pending, pendingRestore{spec, g})
	}
	m.mu.Unlock()
	close(m.restored)
	for _, item := range pending {
		go m.restoreSaved(item.spec, item.group)
	}
}
func (m *forwardManager) restoreSaved(spec ForwardSpec, placeholder *forwardGroup) {
	id := "saved-" + spec.Save
	var restoreErr error
	for attempt := 0; attempt < 8; attempt++ {
		if _, restoreErr = m.addSpec(spec, false, id, placeholder); restoreErr == nil {
			return
		}
		if errors.Is(restoreErr, errForwardRemoved) {
			return
		}
		m.mu.Lock()
		if g := m.groups[id]; g == placeholder {
			g.info.State, g.info.Error = "reconnecting", "restore failed: "+restoreErr.Error()
		}
		m.mu.Unlock()
		delay := time.Second << min(attempt, 4)
		select {
		case <-m.ctx.Done():
			return
		case <-time.After(delay):
		}
	}
	m.mu.Lock()
	if g := m.groups[id]; g == placeholder {
		g.info.State, g.info.Error = "failed", "restore failed after 8 attempts: "+restoreErr.Error()
	}
	m.mu.Unlock()
}
func (m *forwardManager) saveLocked() error {
	if m.restoreError != "" {
		return fmt.Errorf("saved forwards manifest is unreadable; repair or move %s: %s", filepath.Join(m.dir, savedForwardsFile), m.restoreError)
	}
	specs := make([]ForwardSpec, 0)
	for _, g := range m.groups {
		if g.info.Spec.Save != "" {
			specs = append(specs, g.info.Spec)
		}
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Save < specs[j].Save })
	b, err := json.MarshalIndent(specs, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(m.dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(m.dir, ".forwards-*")
	if err != nil {
		return err
	}
	path := f.Name()
	defer os.Remove(path)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(append(b, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(path, filepath.Join(m.dir, savedForwardsFile))
}
func (m *forwardManager) list() []ForwardInfo {
	<-m.restored
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []ForwardInfo{}
	for _, g := range m.groups {
		out = append(out, g.info)
	}
	if m.restoreError != "" {
		out = append(out, ForwardInfo{ID: "manifest", State: "failed", Error: "saved forwards manifest: " + m.restoreError})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (m *forwardManager) remove(id string) error {
	<-m.restored
	m.mu.Lock()
	defer m.mu.Unlock()
	g := m.groups[id]
	if g == nil {
		for _, candidate := range m.groups {
			if candidate.info.Spec.Save == id {
				g = candidate
				id = candidate.info.ID
				break
			}
		}
	}
	if g == nil {
		return fmt.Errorf("unknown forward %q", id)
	}
	m.release(g)
	delete(m.groups, id)
	return m.saveLocked()
}
func (m *forwardManager) resume(name string) (ForwardInfo, error) {
	<-m.restored
	m.mu.Lock()
	var id string
	var spec ForwardSpec
	for candidateID, g := range m.groups {
		if g.info.Spec.Save == name {
			id, spec = candidateID, g.info.Spec
			break
		}
	}
	if id == "" {
		m.mu.Unlock()
		return ForwardInfo{}, fmt.Errorf("unknown saved forward %q", name)
	}
	placeholder := m.groups[id]
	m.release(placeholder)
	_, placeholder.cancel = context.WithCancel(m.ctx)
	placeholder.cleanup = func() {}
	placeholder.info.State, placeholder.info.Error = "restoring", ""
	m.mu.Unlock()
	info, err := m.addSpec(spec, true, id, placeholder)
	if err != nil {
		m.mu.Lock()
		if m.groups[id] == placeholder {
			placeholder.info.State, placeholder.info.Error = "failed", "resume failed: "+err.Error()
		}
		m.mu.Unlock()
	}
	return info, err
}
func (m *forwardManager) release(g *forwardGroup) {
	g.cancel()
	g.cleanup()
	for _, t := range g.transports {
		t.CloseIdleConnections()
	}
	for _, p := range g.info.Spec.Ports {
		key := forwardListenerKey{g.info.Spec.listenerAddress(), p.Local}
		l := m.listeners[key]
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
			delete(m.listeners, key)
		}
	}
}
func (m *forwardManager) close() {
	<-m.restored
	m.cancel()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, g := range m.groups {
		m.release(g)
	}
	m.groups = map[string]*forwardGroup{}
}
func (m *forwardManager) add(s ForwardSpec) (ForwardInfo, error) {
	<-m.restored
	return m.addSpec(s, true, "", nil)
}
func persistentForwardSpec(s ForwardSpec) ForwardSpec {
	if s.Save == "" {
		data, _ := json.Marshal(s)
		hash := sha256.Sum256(data)
		s.Save = fmt.Sprintf("forward-%x", hash[:6])
	}
	return s
}

func (m *forwardManager) addSpec(s ForwardSpec, persist bool, replaceID string, replace *forwardGroup) (ForwardInfo, error) {
	s = persistentForwardSpec(s)
	if s.Name != "" && s.Public == nil {
		expected, err := namedLoopbackAddress(m.dir, s.Target, s.Name)
		if err != nil {
			return ForwardInfo{}, err
		}
		if s.BindAddress != "" && s.BindAddress != expected {
			return ForwardInfo{}, fmt.Errorf("forward bind address does not match the configured loopback for %s", s.Target)
		}
		s.BindAddress = expected
	}
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
	if replaceID != "" && m.groups[replaceID] != replace {
		return ForwardInfo{}, errForwardRemoved
	}
	if s.Save != "" {
		for _, existing := range m.groups {
			if existing.info.ID != replaceID && existing.info.Spec.Save == s.Save {
				return ForwardInfo{}, fmt.Errorf("saved forward %q already exists", s.Save)
			}
		}
	}
	for _, p := range s.Ports {
		key := forwardListenerKey{s.listenerAddress(), p.Local}
		if l := m.listeners[key]; forwardListenerConflicts(l, s) {
			return ForwardInfo{}, fmt.Errorf("local port %d already forwards this name or is reserved for TCP", p.Local)
		}
	}
	b := make([]byte, 6)
	if _, err = rand.Read(b); err != nil {
		return ForwardInfo{}, err
	}
	id := hex.EncodeToString(b)
	ctx, cancel := context.WithCancel(m.ctx)
	g := &forwardGroup{info: ForwardInfo{ID: id, Spec: s, State: "starting"}, cancel: cancel, cleanup: func() {}}
	// Reserve every port before starting SSH; errors roll back only this group.
	for _, p := range s.Ports {
		key := forwardListenerKey{s.listenerAddress(), p.Local}
		l := m.listeners[key]
		if l == nil {
			ln, e := net.Listen("tcp4", net.JoinHostPort(s.listenerAddress(), fmt.Sprint(p.Local)))
			if e != nil {
				m.release(g)
				if s.Name != "" && s.Public == nil {
					return ForwardInfo{}, fmt.Errorf("bind %s:%d failed: %w; run `tailmux loopback setup %s --name %s` to restore the loopback address", s.listenerAddress(), p.Local, e, s.Target, s.Name)
				}
				return ForwardInfo{}, e
			}
			l = &forwardListener{ln: ln, routes: map[string]http.Handler{}, owners: map[string]bool{}, public: s.Public != nil}
			m.listeners[key] = l
		}
		l.owners[id] = true
		if s.Name == "" {
			l.raw = id
		} else {
			l.routes[s.Name] = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "Tailmux forward is starting", http.StatusServiceUnavailable)
			})
		}
	}
	if replaceID != "" {
		m.release(replace)
		delete(m.groups, replaceID)
	}
	m.groups[id] = g
	m.mu.Unlock()
	dial, cleanup, done, err := startForwardTunnel(ctx, m.dir, s, h)
	m.mu.Lock()
	if m.groups[id] != g {
		cancel()
		if cleanup != nil {
			cleanup()
		}
		return ForwardInfo{}, errForwardRemoved
	}
	if err != nil {
		m.release(g)
		delete(m.groups, id)
		if replace != nil {
			_, replace.cancel = context.WithCancel(m.ctx)
			replace.cleanup = func() {}
			replace.info.State, replace.info.Error = "reconnecting", err.Error()
			m.groups[replaceID] = replace
		} else if persist && s.Save != "" {
			_ = m.saveLocked()
		}
		return ForwardInfo{}, err
	}
	live := &liveTunnel{}
	live.set(dial, cleanup)
	g.cleanup = live.clear
	for _, p := range s.Ports {
		l := m.listeners[forwardListenerKey{s.listenerAddress(), p.Local}]
		if s.Name == "" {
			l.raw = id
			go serveTCP(ctx, l.ln, p.Remote, live.connect)
		} else {
			groupDial := func(callCtx context.Context, port int) (net.Conn, error) {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				conn, err := live.connect(callCtx, port)
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
						serveNoForward(w, r)
						return
					}
					handler.ServeHTTP(w, r)
				})}
				go l.server.Serve(l.ln)
			}
		}
	}
	g.info.State = "running"
	if s.Public != nil {
		m.mu.Unlock()
		publicCleanup, publicDone, err := startPublicTunnel(ctx, m.dir, s)
		m.mu.Lock()
		if m.groups[id] != g {
			if publicCleanup != nil {
				publicCleanup()
			}
			return ForwardInfo{}, errForwardRemoved
		}
		if err != nil {
			if publicCleanup != nil {
				publicCleanup()
			}
			m.release(g)
			delete(m.groups, id)
			if replace != nil {
				_, replace.cancel = context.WithCancel(m.ctx)
				replace.cleanup = func() {}
				replace.info.State, replace.info.Error = "reconnecting", err.Error()
				m.groups[replaceID] = replace
			} else if persist && s.Save != "" {
				_ = m.saveLocked()
			}
			return ForwardInfo{}, err
		}
		publicLive := &liveProcess{}
		publicLive.set(publicCleanup)
		sshCleanup := g.cleanup
		g.cleanup = func() { publicLive.clear(); sshCleanup() }
		g.info.PublicState = "running"
		go m.keepPublicTunnel(ctx, id, g, publicLive, publicDone)
	}
	if persist && s.Save != "" {
		if err := m.saveLocked(); err != nil {
			m.release(g)
			delete(m.groups, id)
			return ForwardInfo{}, fmt.Errorf("save forward: %w", err)
		}
	}
	go m.keepTunnel(ctx, id, g, live, h, done)
	return g.info, nil
}

func (m *forwardManager) keepPublicTunnel(ctx context.Context, id string, g *forwardGroup, live *liveProcess, done <-chan error) {
	for {
		err := <-done
		if ctx.Err() != nil {
			return
		}
		live.clear()
		m.mu.Lock()
		if m.groups[id] != g {
			m.mu.Unlock()
			return
		}
		g.info.PublicState = "reconnecting"
		g.info.PublicError = fmt.Sprintf("%s tunnel stopped: %v", g.info.Spec.Public.Provider, err)
		m.mu.Unlock()
		var last error
		for attempt := 0; attempt < 5; attempt++ {
			delay := time.Second << min(attempt, 4)
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			cleanup, nextDone, e := startPublicTunnel(ctx, m.dir, g.info.Spec)
			if e != nil {
				last = e
				continue
			}
			live.set(cleanup)
			m.mu.Lock()
			if m.groups[id] != g {
				m.mu.Unlock()
				live.clear()
				return
			}
			g.info.PublicState, g.info.PublicError = "running", ""
			m.mu.Unlock()
			done, last = nextDone, nil
			break
		}
		if last != nil {
			m.mu.Lock()
			if m.groups[id] == g {
				g.info.PublicState = "failed"
				g.info.PublicError = fmt.Sprintf("%s reconnect failed: %v", g.info.Spec.Public.Provider, last)
			}
			m.mu.Unlock()
			return
		}
	}
}

func startPublicTunnel(ctx context.Context, dir string, s ForwardSpec) (func(), <-chan error, error) {
	provider := s.Public.Provider
	bin := "ngrok"
	if provider == "cloudflare" {
		bin = "cloudflared"
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return func() {}, nil, fmt.Errorf("%s is required for --%s", bin, provider)
	}
	configPath := ""
	if provider == "cloudflare" {
		configDir := filepath.Join(dir, "public-tunnels")
		if err := os.MkdirAll(configDir, 0700); err != nil {
			return func() {}, nil, err
		}
		configPath = filepath.Join(configDir, "cloudflare-"+s.Save+".yml")
		if s.Save == "" {
			configPath = filepath.Join(configDir, "cloudflare-"+fmt.Sprint(s.Ports[0].Local)+".yml")
		}
		origin := fmt.Sprintf("http://%s:%d", s.listenerAddress(), s.Ports[0].Local)
		contents := fmt.Sprintf("ingress:\n  - hostname: %s\n    service: %s\n  - service: http_status:404\n", s.Name, origin)
		if err := os.WriteFile(configPath, []byte(contents), 0600); err != nil {
			return func() {}, nil, err
		}
	}
	cleanupFiles := func() {
		if configPath != "" {
			_ = os.Remove(configPath)
		}
	}
	_, args := publicTunnelCommand(s, configPath)
	logDir := filepath.Join(dir, "public-tunnels")
	if err = os.MkdirAll(logDir, 0700); err != nil {
		cleanupFiles()
		return func() {}, nil, err
	}
	logPath := filepath.Join(logDir, s.Save+"-"+provider+".log")
	if s.Save == "" {
		logPath = filepath.Join(logDir, provider+"-"+fmt.Sprint(s.Ports[0].Local)+".log")
	}
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		cleanupFiles()
		return func() {}, nil, err
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdout, cmd.Stderr = log, log
	if err = cmd.Start(); err != nil {
		log.Close()
		cleanupFiles()
		return func() {}, nil, fmt.Errorf("start %s tunnel: %w", provider, err)
	}
	done := make(chan error, 1)
	go func() { err := cmd.Wait(); log.Close(); done <- err }()
	cleanup := func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		cleanupFiles()
	}
	// Catch immediate configuration/authentication failures while keeping startup responsive.
	timer := time.NewTimer(750 * time.Millisecond)
	defer timer.Stop()
	select {
	case err = <-done:
		return cleanup, nil, fmt.Errorf("%s tunnel failed: %v (details in %s)", provider, err, logPath)
	case <-timer.C:
		return cleanup, done, nil
	case <-ctx.Done():
		cleanup()
		return cleanup, nil, ctx.Err()
	}
}

func publicTunnelCommand(s ForwardSpec, configPath string) (string, []string) {
	origin := fmt.Sprintf("http://%s:%d", s.listenerAddress(), s.Ports[0].Local)
	if s.Public.Provider == "cloudflare" {
		return "cloudflared", []string{"tunnel", "--config", configPath, "run", s.Public.Tunnel}
	}
	return "ngrok", []string{"http", origin, "--url", s.Public.URL}
}

func (m *forwardManager) keepTunnel(ctx context.Context, id string, g *forwardGroup, live *liveTunnel, h Host, done <-chan error) {
	for {
		err := <-done
		if ctx.Err() != nil {
			return
		}
		live.clear()
		m.mu.Lock()
		if m.groups[id] != g {
			m.mu.Unlock()
			return
		}
		g.info.State, g.info.Error = "reconnecting", fmt.Sprintf("SSH tunnel stopped: %v", err)
		m.mu.Unlock()
		var last error
		for attempt := 0; attempt < 8; attempt++ {
			delay := time.Second << min(attempt, 4)
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			dial, cleanup, nextDone, e := startForwardTunnel(ctx, m.dir, g.info.Spec, h)
			if e != nil {
				last = e
				continue
			}
			live.set(dial, cleanup)
			m.mu.Lock()
			if m.groups[id] != g {
				m.mu.Unlock()
				live.clear()
				return
			}
			g.info.State, g.info.Error = "running", ""
			m.mu.Unlock()
			done = nextDone
			last = nil
			break
		}
		if last != nil {
			m.mu.Lock()
			if m.groups[id] == g {
				g.info.State, g.info.Error = "failed", fmt.Sprintf("SSH reconnect failed: %v", last)
			}
			m.mu.Unlock()
			return
		}
	}
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
	cleanup = func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = os.RemoveAll(tmp)
	}
	timer := time.NewTimer(45 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case e := <-done:
			data, _ := os.ReadFile(logPath)
			cleanup()
			return nil, cleanup, nil, fmt.Errorf("SSH forward failed: %v: %s", e, strings.TrimSpace(string(data)))
		case <-ctx.Done():
			cleanup()
			return nil, cleanup, nil, ctx.Err()
		case <-timer.C:
			cleanup()
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
func requireForwardCapability(dir, capability string) error {
	if err := ensureDaemon(dir); err != nil {
		return err
	}
	c, err := connect(dir)
	if err != nil {
		return err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if err = json.NewEncoder(c).Encode(request{Op: "capabilities"}); err != nil {
		return err
	}
	var res response
	if err = readLine(bufio.NewReader(c), &res); err != nil {
		return err
	}
	if res.Kind == "capabilities" {
		for _, available := range res.Capabilities {
			if available == capability {
				return nil
			}
		}
	}
	return fmt.Errorf("the running Tailmux daemon does not support %s; run `tailmux stop`, then repeat this command to start the updated daemon", capability)
}
func removeSavedForwardOffline(dir, name string) (bool, error) {
	if forwardDaemonRunning(dir) {
		return false, nil
	}
	lock, err := os.OpenFile(filepath.Join(dir, "daemon.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return false, err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return false, fmt.Errorf("Tailmux daemon is starting; retry the command")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	path := filepath.Join(dir, savedForwardsFile)
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var specs []ForwardSpec
	if err = json.Unmarshal(b, &specs); err != nil {
		return false, err
	}
	kept := specs[:0]
	found := false
	for _, spec := range specs {
		if spec.Save == name {
			found = true
		} else {
			kept = append(kept, spec)
		}
	}
	if !found {
		return false, nil
	}
	data, _ := json.MarshalIndent(kept, "", "  ")
	f, err := os.CreateTemp(dir, ".forwards-*")
	if err != nil {
		return false, err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(append(data, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return false, err
	}
	return true, os.Rename(tmp, path)
}
func forwardDaemonRunning(dir string) bool {
	c, err := connect(dir)
	if err != nil {
		return false
	}
	c.Close()
	return true
}
func forwardCLI(dir string, args []string) error {
	req := request{Op: args[0]}
	asJSON := false
	switch args[0] {
	case "forward":
		if len(args) == 3 && args[1] == "--resume" {
			if err := requireForwardCapability(dir, "saved-forwards-v1"); err != nil {
				return err
			}
			req.Op, req.Target = "resume-forward", args[2]
			break
		}
		if len(args) < 3 {
			return fmt.Errorf("usage: tailmux forward <host> <ports...> [--name HOST] [--save NAME] [--cloudflare TUNNEL --url HTTPS_URL | --ngrok --url HTTPS_URL] [--no-rewrite] [--json]")
		}
		s := ForwardSpec{Target: args[1], Rewrite: true}
		var ports []string
		var provider, tunnel, publicURL string
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
			case args[i] == "--save":
				i++
				if i == len(args) {
					return fmt.Errorf("--save needs a name")
				}
				s.Save = args[i]
			case strings.HasPrefix(args[i], "--save="):
				s.Save = strings.TrimPrefix(args[i], "--save=")
			case args[i] == "--cloudflare":
				i++
				if i == len(args) {
					return fmt.Errorf("--cloudflare needs a tunnel name or UUID")
				}
				if provider != "" {
					return fmt.Errorf("choose one public URL provider")
				}
				provider, tunnel = "cloudflare", args[i]
			case strings.HasPrefix(args[i], "--cloudflare="):
				if provider != "" {
					return fmt.Errorf("choose one public URL provider")
				}
				provider, tunnel = "cloudflare", strings.TrimPrefix(args[i], "--cloudflare=")
			case args[i] == "--ngrok":
				if provider != "" {
					return fmt.Errorf("choose one public URL provider")
				}
				provider = "ngrok"
			case args[i] == "--url":
				i++
				if i == len(args) {
					return fmt.Errorf("--url needs an HTTPS URL")
				}
				publicURL = args[i]
			case strings.HasPrefix(args[i], "--url="):
				publicURL = strings.TrimPrefix(args[i], "--url=")
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
		if provider != "" {
			if publicURL == "" {
				return fmt.Errorf("--%s requires --url HTTPS_URL", provider)
			}
			s.Public = &PublicForward{Provider: provider, URL: publicURL, Tunnel: tunnel}
			u, parseErr := url.Parse(publicURL)
			if parseErr == nil {
				if s.Name != "" && !strings.EqualFold(s.Name, u.Hostname()) {
					return fmt.Errorf("--name must match the public URL hostname")
				}
				s.Name = strings.ToLower(u.Hostname())
			}
		} else if publicURL != "" {
			return fmt.Errorf("--url requires --cloudflare or --ngrok")
		}
		if s.Name != "" && s.Public == nil {
			if !strings.Contains(s.Target, "/") {
				cfg, loadErr := loadConfig(dir)
				if loadErr != nil {
					return loadErr
				}
				s.Target, err = canonicalTarget(cfg, s.Target)
				if err != nil {
					return err
				}
			}
			s.BindAddress, err = namedLoopbackAddress(dir, s.Target, s.Name)
			if err != nil {
				return err
			}
		}
		s = persistentForwardSpec(s)
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
		if stopped, err := removeSavedForwardOffline(dir, req.Target); err != nil {
			return err
		} else if stopped {
			fmt.Println("Saved forward removed:", req.Target)
			return nil
		} else if !forwardDaemonRunning(dir) {
			return fmt.Errorf("no running daemon and no saved forward %q", req.Target)
		}
	}
	if req.Forward != nil {
		if req.Forward.Save != "" {
			if err := requireForwardCapability(dir, "saved-forwards-v1"); err != nil {
				return err
			}
		}
		if req.Forward.Public != nil {
			if err := requireForwardCapability(dir, "public-forwards-v1"); err != nil {
				return err
			}
		}
		if req.Forward.Name != "" && req.Forward.Public == nil {
			if err := requireForwardCapability(dir, "loopback-forwards-v1"); err != nil {
				return err
			}
		}
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
		if g.PublicError != "" {
			fmt.Println(g.PublicError)
		}
		if g.Spec.Save != "" {
			fmt.Println("Saved as:", g.Spec.Save)
		}
		if g.Spec.Public != nil {
			fmt.Printf("Public URL: %s (%s: %s)\n", g.Spec.Public.URL, g.Spec.Public.Provider, g.PublicState)
		}
		if args[0] == "forward" && g.Spec.Public == nil && g.Spec.Name != "" && !strings.HasSuffix(g.Spec.Name, ".localhost") && g.Spec.Name != "localhost" {
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
