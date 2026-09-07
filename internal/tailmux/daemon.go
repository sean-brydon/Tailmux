package tailmux

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"tailscale.com/tsnet"
)

type request struct {
	Forward *ForwardSpec `json:"forward,omitempty"`
	Op      string       `json:"op"`
	Target  string       `json:"target,omitempty"`
}
type response struct {
	Forwards     []ForwardInfo    `json:"forwards,omitempty"`
	Capabilities []string         `json:"capabilities,omitempty"`
	Kind         string           `json:"kind"`
	Message      string           `json:"message,omitempty"`
	Hosts        []discoveredHost `json:"hosts,omitempty"`
}
type discoveredHost struct {
	Target  string `json:"target"`
	Address string `json:"address"`
	Online  bool   `json:"online"`
}

// One JSON line each way precedes the raw byte stream. Readers must retain buffered bytes.
func readLine(r *bufio.Reader, v any) error {
	var line []byte
	for {
		part, err := r.ReadSlice('\n')
		line = append(line, part...)
		if len(line) > 1<<20 {
			return fmt.Errorf("protocol message exceeds 1 MiB")
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			return err
		}
		return json.Unmarshal(line, v)
	}
}
func send(w io.Writer, kind, msg string) error {
	return json.NewEncoder(w).Encode(response{Kind: kind, Message: msg})
}
func socketPath(dir string) string { return filepath.Join(dir, "daemon.sock") }
func connect(dir string) (net.Conn, error) {
	return net.DialTimeout("unix", socketPath(dir), time.Second)
}
func ensureDaemon(dir string) error {
	if c, e := connect(dir); e == nil {
		c.Close()
		return nil
	}
	if len(socketPath(dir)) > 100 {
		return fmt.Errorf("TAILMUX_HOME path too long for a Unix socket; choose a shorter directory")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "daemon.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	cmd := exec.Command(exe, "daemon")
	cmd.Env = append(os.Environ(), "TAILMUX_HOME="+dir)
	cmd.Stdout = f
	cmd.Stderr = f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	for i := 0; i < 100; i++ {
		if c, e := connect(dir); e == nil {
			c.Close()
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("daemon did not start; see %s", filepath.Join(dir, "daemon.log"))
}

type daemon struct {
	forwards *forwardManager
	dir      string
	mu       sync.Mutex
	nodes    map[string]*tsnet.Server
	stop     context.CancelFunc
}

func (d *daemon) node(profile string) (*tsnet.Server, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if s := d.nodes[profile]; s != nil {
		return s, nil
	}
	dir := filepath.Join(d.dir, "state", profile)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	s := &tsnet.Server{Dir: dir, Hostname: "tailmux-" + profile, Logf: func(string, ...any) {}, UserLogf: log.Printf}
	if err := s.Start(); err != nil {
		return nil, err
	}
	d.nodes[profile] = s
	return s, nil
}
func serve(dir string) error {
	// Never allow ambient credentials to enroll both profiles into the same account.
	for _, k := range []string{"TS_AUTHKEY", "TS_AUTH_KEY", "TS_CLIENT_SECRET", "TS_CLIENT_ID", "TS_ID_TOKEN", "TS_AUDIENCE", "TSNET_FORCE_LOGIN"} {
		os.Unsetenv(k)
	}
	lock, err := os.OpenFile(filepath.Join(dir, "daemon.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("another Tailmux daemon owns this directory: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	os.Remove(socketPath(dir))
	ln, err := net.Listen("unix", socketPath(dir))
	if err != nil {
		return err
	}
	defer os.Remove(socketPath(dir))
	defer ln.Close()
	if err = os.Chmod(socketPath(dir), 0600); err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	d := &daemon{dir: dir, nodes: map[string]*tsnet.Server{}, stop: cancel}
	d.forwards = newForwardManager(ctx, dir)
	go d.forwards.restore()
	defer d.forwards.close()
	go func() { <-ctx.Done(); ln.Close() }()
	defer func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		for _, s := range d.nodes {
			s.Close()
		}
	}()
	log.Print("Tailmux daemon ready")
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		c, e := ln.Accept()
		if e != nil {
			if ctx.Err() != nil {
				return nil
			}
			return e
		}
		wg.Add(1)
		go func() { defer wg.Done(); defer c.Close(); d.handle(ctx, c) }()
	}
}
func (d *daemon) handle(parent context.Context, c net.Conn) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	go func() { <-ctx.Done(); c.Close() }()
	r := bufio.NewReader(c)
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	var req request
	if err := readLine(r, &req); err != nil {
		return
	}
	c.SetReadDeadline(time.Time{})
	fail := func(err error) { send(c, "error", err.Error()) }
	if req.Op == "ping" {
		send(c, "ok", "ready")
		return
	}
	if req.Op == "capabilities" {
		json.NewEncoder(c).Encode(response{Kind: "capabilities", Capabilities: []string{"saved-forwards-v1", "public-forwards-v1", "loopback-forwards-v1"}})
		return
	}
	if req.Op == "stop" {
		send(c, "ok", "Tailmux daemon stopped")
		d.stop()
		return
	}
	if req.Op == "forward" || req.Op == "forwards" || req.Op == "unforward" || req.Op == "resume-forward" {
		var infos []ForwardInfo
		var err error
		switch req.Op {
		case "forward":
			if req.Forward == nil {
				fail(fmt.Errorf("missing forward specification"))
				return
			}
			var info ForwardInfo
			info, err = d.forwards.add(*req.Forward)
			infos = []ForwardInfo{info}
		case "forwards":
			infos = d.forwards.list()
		case "unforward":
			err = d.forwards.remove(req.Target)
		case "resume-forward":
			var info ForwardInfo
			info, err = d.forwards.resume(req.Target)
			infos = []ForwardInfo{info}
		}
		if err != nil {
			fail(err)
			return
		}
		json.NewEncoder(c).Encode(response{Kind: "forwards", Forwards: infos})
		return
	}
	cfg, err := loadConfig(d.dir)
	if err != nil {
		fail(err)
		return
	}
	var profile string
	var host Host
	switch req.Op {
	case "login", "hosts":
		profile = req.Target
		if !cfg.hasProfile(profile) {
			fail(fmt.Errorf("unknown profile %q", profile))
			return
		}
	case "dial", "doctor":
		host, err = cfg.host(req.Target)
		if err != nil {
			fail(err)
			return
		}
		profile = host.Profile
	default:
		fail(fmt.Errorf("unknown operation %q", req.Op))
		return
	}
	s, err := d.node(profile)
	if err != nil {
		fail(err)
		return
	}
	if req.Op == "login" {
		d.login(ctx, c, s, profile)
		return
	}
	timeout, cancelTimeout := context.WithTimeout(ctx, 25*time.Second)
	defer cancelTimeout()
	lc, err := s.LocalClient()
	if err != nil {
		fail(err)
		return
	}
	status, err := lc.Status(timeout)
	if err != nil {
		fail(err)
		return
	}
	if status.BackendState == "NeedsLogin" || status.AuthURL != "" {
		fail(fmt.Errorf("profile %s needs login; run tailmux login %s", profile, profile))
		return
	}
	if _, err = s.Up(timeout); err != nil {
		fail(fmt.Errorf("profile %s not ready (run tailmux login %s): %w", profile, profile, err))
		return
	}
	if req.Op == "hosts" {
		st, err := lc.Status(timeout)
		if err != nil {
			fail(err)
			return
		}
		// A restored node can report Running before the fresh peer map arrives.
		// Give initial discovery a short grace period; empty tailnets remain valid.
		for attempt := 0; len(st.Peer) == 0 && attempt < 10; attempt++ {
			select {
			case <-timeout.Done():
				fail(timeout.Err())
				return
			case <-time.After(200 * time.Millisecond):
			}
			st, err = lc.Status(timeout)
			if err != nil {
				fail(err)
				return
			}
		}
		hosts := []discoveredHost{}
		for _, peer := range st.Peer {
			address := strings.TrimSuffix(peer.DNSName, ".")
			name := strings.Split(address, ".")[0]
			if address == "" && len(peer.TailscaleIPs) > 0 {
				address = peer.TailscaleIPs[0].String()
				name = address
			}
			if !safeAddress.MatchString(address) || !safeAddress.MatchString(name) {
				continue
			}
			hosts = append(hosts, discoveredHost{Target: profile + "/" + name, Address: address, Online: peer.Online})
		}
		sort.Slice(hosts, func(i, j int) bool { return hosts[i].Target < hosts[j].Target })
		json.NewEncoder(c).Encode(response{Kind: "inventory", Hosts: hosts})
		return
	}
	conn, err := s.Dial(timeout, "tcp", net.JoinHostPort(host.Address, fmt.Sprint(host.Port)))
	if err != nil {
		fail(fmt.Errorf("%s/%s port %d: %w", profile, host.Address, host.Port, err))
		return
	}
	defer conn.Close()
	go func() { <-ctx.Done(); conn.Close() }()
	if req.Op == "doctor" {
		send(c, "ok", fmt.Sprintf("%s: %s profile, TCP %s:%d reachable", req.Target, profile, host.Address, host.Port))
		return
	}
	if err = send(c, "ready", ""); err != nil {
		return
	}
	// SSH owns the byte stream after the handshake; stdout must contain no diagnostics.
	done := make(chan struct{})
	go func() {
		io.Copy(conn, r)
		if h, ok := conn.(interface{ CloseWrite() error }); ok {
			h.CloseWrite()
		}
		close(done)
	}()
	io.Copy(c, conn)
	c.Close()
	conn.Close()
	<-done
}
func (d *daemon) login(ctx context.Context, c net.Conn, s *tsnet.Server, profile string) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	// Detect a cancelled CLI while waiting for browser authentication.
	go func() { var b [1]byte; c.Read(b[:]); cancel() }()
	lc, err := s.LocalClient()
	if err != nil {
		send(c, "error", err.Error())
		return
	}
	result := make(chan error, 1)
	go func() { _, err := s.Up(ctx); result <- err }()
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	lastURL := ""
	for {
		select {
		case err := <-result:
			if err != nil {
				send(c, "error", err.Error())
				return
			}
			st, err := lc.Status(ctx)
			if err != nil {
				send(c, "error", err.Error())
				return
			}
			name := "unknown"
			if st.CurrentTailnet != nil {
				name = st.CurrentTailnet.Name
			}
			send(c, "ok", fmt.Sprintf("%s connected to %s (%v)", profile, name, st.TailscaleIPs))
			return
		case <-tick.C:
			st, err := lc.Status(ctx)
			if err == nil && st.AuthURL != "" && st.AuthURL != lastURL {
				lastURL = st.AuthURL
				if send(c, "info", fmt.Sprintf("Sign in with your %s account: %s", profile, lastURL)) != nil {
					return
				}
			}
		case <-ctx.Done():
			send(c, "error", ctx.Err().Error())
			return
		}
	}
}
func rpc(dir, op, target string) (net.Conn, *bufio.Reader, error) {
	if err := ensureDaemon(dir); err != nil {
		return nil, nil, err
	}
	c, err := connect(dir)
	if err != nil {
		return nil, nil, err
	}
	if err = json.NewEncoder(c).Encode(request{Op: op, Target: target}); err != nil {
		c.Close()
		return nil, nil, err
	}
	r := bufio.NewReader(c)
	for {
		var res response
		if err = readLine(r, &res); err != nil {
			c.Close()
			return nil, nil, err
		}
		switch res.Kind {
		case "error":
			c.Close()
			return nil, nil, errors.New(res.Message)
		case "info":
			fmt.Fprintln(os.Stderr, res.Message)
		case "ok":
			fmt.Fprintln(os.Stdout, res.Message)
			c.Close()
			return nil, nil, nil
		case "ready":
			return c, r, nil
		default:
			c.Close()
			return nil, nil, fmt.Errorf("invalid daemon response %q", res.Kind)
		}
	}
}
