package tailmux

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestForwardPorts(t *testing.T) {
	p, e := parsePorts([]string{"3000-3002,8080:80"})
	if e != nil || len(p) != 4 || p[3].Remote != 80 {
		t.Fatalf("%v %v", p, e)
	}
	for _, v := range []string{"0", "65536", "3002-3000", "1-101", "3000,3000", "3:bad"} {
		if _, e := parsePorts([]string{v}); e == nil {
			t.Fatal(v)
		}
	}
}

func TestForwardBindAddressValidation(t *testing.T) {
	base := ForwardSpec{Target: "p/dev", Name: "dev.localhost", Ports: []PortMap{{3000, 3000}}, BindAddress: "127.20.0.2"}
	if err := base.validate(); err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{"0.0.0.0", "192.168.1.4", "::1", "bad"} {
		s := base
		s.BindAddress = address
		if s.validate() == nil {
			t.Fatalf("accepted bind address %q", address)
		}
	}
	raw := base
	raw.Name, raw.BindAddress = "", "127.20.0.2"
	if raw.validate() == nil {
		t.Fatal("raw forward accepted per-box bind address")
	}
	public := base
	public.Public = &PublicForward{Provider: "ngrok", URL: "https://dev.example.com"}
	if public.validate() == nil {
		t.Fatal("public forward accepted per-box bind address")
	}
}

func TestForwardListenersUseAddressAndPortIdentity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newForwardManager(ctx, t.TempDir())
	close(m.restored)
	first, second := forwardListenerKey{"127.20.0.2", 3000}, forwardListenerKey{"127.20.0.3", 3000}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	m.listeners[first] = &forwardListener{ln: ln, owners: map[string]bool{"one": true}, routes: map[string]http.Handler{"one.localhost": http.NotFoundHandler()}}
	m.listeners[second] = &forwardListener{owners: map[string]bool{"two": true}, routes: map[string]http.Handler{"two.localhost": http.NotFoundHandler()}}
	_, groupCancel := context.WithCancel(ctx)
	g := &forwardGroup{info: ForwardInfo{ID: "one", Spec: ForwardSpec{Name: "one.localhost", BindAddress: first.Address, Ports: []PortMap{{3000, 3000}}}}, cancel: groupCancel, cleanup: func() {}}
	m.release(g)
	if m.listeners[first] != nil {
		t.Fatal("released listener remained")
	}
	if m.listeners[second] == nil {
		t.Fatal("release removed same port on another loopback address")
	}
}

func TestPublicForwardValidationAndRewrite(t *testing.T) {
	s := ForwardSpec{Target: "personal/dev", Name: "preview.example.com", Ports: []PortMap{{3000, 3000}}, Rewrite: true, Save: "preview", Public: &PublicForward{Provider: "cloudflare", Tunnel: "preview-tunnel", URL: "https://preview.example.com"}}
	if err := s.validate(); err != nil {
		t.Fatal(err)
	}
	if got := rewriteURL("http://localhost:3000/callback", s, true); got != "https://preview.example.com/callback" {
		t.Fatal(got)
	}
	if got := rewriteURL("https://preview.example.com/login", s, false); got != "http://localhost:3000/login" {
		t.Fatal(got)
	}
	s.Public.URL = "https://preview.example.com:8443"
	if got := rewriteURL("https://preview.example.com:8443/login", s, false); got != "http://localhost:3000/login" {
		t.Fatal(got)
	}
	s.Public.URL = "https://preview.example.com"
	bad := s
	bad.Ports = append(bad.Ports, PortMap{4000, 4000})
	if bad.validate() == nil {
		t.Fatal("public multi-port forward accepted")
	}
	bad = s
	bad.Public = &PublicForward{Provider: "ngrok", URL: "http://preview.example.com"}
	if bad.validate() == nil {
		t.Fatal("insecure public URL accepted")
	}
	bin, args := publicTunnelCommand(s, "/private/config.yml")
	if bin != "cloudflared" || strings.Join(args, " ") != "tunnel --config /private/config.yml run preview-tunnel" {
		t.Fatalf("%s %q", bin, args)
	}
	s.Public = &PublicForward{Provider: "ngrok", URL: "https://preview.ngrok.app"}
	bin, args = publicTunnelCommand(s, "")
	if bin != "ngrok" || strings.Join(args, " ") != "http http://127.0.0.1:3000 --url https://preview.ngrok.app" {
		t.Fatalf("%s %q", bin, args)
	}
}

func TestPublicForwardSetsExternalProxyHeaders(t *testing.T) {
	var proto, host string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proto, host = r.Header.Get("X-Forwarded-Proto"), r.Header.Get("X-Forwarded-Host")
		if r.Host != "localhost:3000" {
			t.Errorf("upstream host %q", r.Host)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	s := ForwardSpec{Name: "preview.example.com", Ports: []PortMap{{3000, 3000}}, Public: &PublicForward{Provider: "ngrok", URL: "https://preview.example.com:8443"}}
	proxy, tr := httpForward(s, s.Ports[0], func(ctx context.Context, _ int) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", strings.TrimPrefix(upstream.URL, "http://"))
	})
	defer tr.CloseIdleConnections()
	proxy.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://preview.example.com:3000/", nil))
	if proto != "https" || host != "preview.example.com:8443" {
		t.Fatalf("proto=%q host=%q", proto, host)
	}
}

func TestPublicForwardCannotShareLocalListener(t *testing.T) {
	l := &forwardListener{routes: map[string]http.Handler{"one.localhost": http.NotFoundHandler()}}
	public := ForwardSpec{Name: "public.example.com", Public: &PublicForward{Provider: "ngrok", URL: "https://public.example.com"}}
	if !forwardListenerConflicts(l, public) {
		t.Fatal("public forward shared an existing local listener")
	}
	l = &forwardListener{public: true, routes: map[string]http.Handler{"public.example.com": http.NotFoundHandler()}}
	if !forwardListenerConflicts(l, ForwardSpec{Name: "two.localhost"}) {
		t.Fatal("local forward shared a public listener")
	}
}

func TestStartingForwardReservesRouteAndRawListener(t *testing.T) {
	pending := &forwardListener{routes: map[string]http.Handler{"box.localhost": http.NotFoundHandler()}}
	if !forwardListenerConflicts(pending, ForwardSpec{Name: "box.localhost"}) {
		t.Fatal("duplicate hostname was not reserved during startup")
	}
	raw := &forwardListener{raw: "starting", routes: map[string]http.Handler{}}
	if !forwardListenerConflicts(raw, ForwardSpec{Name: "other.localhost"}) {
		t.Fatal("raw listener was not reserved during startup")
	}
}

func TestSavedForwardFileIsPrivateAndRemovedByName(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newForwardManager(ctx, dir)
	close(m.restored)
	gctx, gcancel := context.WithCancel(ctx)
	_ = gctx
	g := &forwardGroup{info: ForwardInfo{ID: "abc", Spec: ForwardSpec{Target: "p/dev", Save: "web", Ports: []PortMap{{3000, 3000}}}}, cancel: gcancel, cleanup: func() {}}
	m.groups[g.info.ID] = g
	if err := m.saveLocked(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, savedForwardsFile)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("%v %v", info, err)
	}
	if err = m.remove("web"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || strings.TrimSpace(string(b)) != "[]" {
		t.Fatalf("%s %v", b, err)
	}
}

func TestPublicTunnelLifecycleAndSanitizedError(t *testing.T) {
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "ngrok")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\nsleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	s := ForwardSpec{Target: "p/dev", Name: "preview.ngrok.app", Ports: []PortMap{{3000, 3000}}, Public: &PublicForward{Provider: "ngrok", URL: "https://preview.ngrok.app"}}
	ctx, cancel := context.WithCancel(context.Background())
	cleanup, done, err := startPublicTunnel(ctx, t.TempDir(), s)
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("public tunnel did not stop")
	}
	cancel()

	if err = os.WriteFile(binPath, []byte("#!/bin/sh\necho secret-token >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	_, _, err = startPublicTunnel(context.Background(), t.TempDir(), s)
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("provider output leaked: %v", err)
	}
}

func TestRemoveSavedForwardOffline(t *testing.T) {
	dir := t.TempDir()
	specs := []ForwardSpec{{Target: "p/a", Save: "one", Ports: []PortMap{{3000, 3000}}}, {Target: "p/b", Save: "two", Ports: []PortMap{{4000, 4000}}}}
	b, _ := json.Marshal(specs)
	if err := os.WriteFile(filepath.Join(dir, savedForwardsFile), b, 0600); err != nil {
		t.Fatal(err)
	}
	found, err := removeSavedForwardOffline(dir, "one")
	if err != nil || !found {
		t.Fatalf("%v %v", found, err)
	}
	b, _ = os.ReadFile(filepath.Join(dir, savedForwardsFile))
	if strings.Contains(string(b), `"one"`) || !strings.Contains(string(b), `"two"`) {
		t.Fatal(string(b))
	}
	if info, err := os.Stat(filepath.Join(dir, savedForwardsFile)); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("%v %v", info, err)
	}
}

func TestCloudflareTunnelUsesIsolatedIngressConfig(t *testing.T) {
	binDir, dir := t.TempDir(), t.TempDir()
	binPath := filepath.Join(binDir, "cloudflared")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\nsleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	s := ForwardSpec{Target: "p/dev", Save: "web", Name: "web.example.com", Ports: []PortMap{{3000, 3000}}, Public: &PublicForward{Provider: "cloudflare", Tunnel: "web-tunnel", URL: "https://web.example.com"}}
	cleanup, done, err := startPublicTunnel(context.Background(), dir, s)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "public-tunnels", "cloudflare-web.yml")
	b, err := os.ReadFile(configPath)
	if err != nil || !strings.Contains(string(b), "hostname: web.example.com") || !strings.Contains(string(b), "service: http://127.0.0.1:3000") || !strings.Contains(string(b), "http_status:404") {
		t.Fatalf("%s %v", b, err)
	}
	cleanup()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cloudflared did not stop")
	}
	if _, err = os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("config was not cleaned up: %v", err)
	}
}

func TestRemoveDuringRestoreDoesNotResurrectSavedForward(t *testing.T) {
	dir := t.TempDir()
	config := `{"profiles":["p"],"hosts":{}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newForwardManager(ctx, dir)
	close(m.restored)
	_, groupCancel := context.WithCancel(ctx)
	spec := ForwardSpec{Target: "missing", Save: "web", Ports: []PortMap{{3000, 3000}}}
	placeholder := &forwardGroup{info: ForwardInfo{ID: "saved-web", Spec: spec, State: "restoring"}, cancel: groupCancel, cleanup: func() {}}
	m.groups["saved-web"] = placeholder
	go m.restoreSaved(spec, placeholder)
	deadline := time.Now().Add(2 * time.Second)
	for {
		m.mu.Lock()
		state := placeholder.info.State
		m.mu.Unlock()
		if state == "reconnecting" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("restore did not enter retry state")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := m.remove("web"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.groups) != 0 {
		t.Fatalf("removed forward returned: %#v", m.groups)
	}
}

func TestCorruptSavedManifestIsVisibleAndCannotBeOverwritten(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, savedForwardsFile), []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	m := newForwardManager(context.Background(), dir)
	m.restore()
	infos := m.list()
	if len(infos) != 1 || infos[0].ID != "manifest" || infos[0].State != "failed" {
		t.Fatalf("%#v", infos)
	}
	m.mu.Lock()
	err := m.saveLocked()
	m.mu.Unlock()
	if err == nil || !strings.Contains(err.Error(), "unreadable") {
		t.Fatalf("%v", err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, savedForwardsFile))
	if string(b) != "{broken" {
		t.Fatal("corrupt manifest was overwritten")
	}
}

func TestPublicURLNeedsProvider(t *testing.T) {
	err := forwardCLI(t.TempDir(), []string{"forward", "p/dev", "3000", "--url", "https://example.com"})
	if err == nil || !strings.Contains(err.Error(), "requires --cloudflare or --ngrok") {
		t.Fatalf("%v", err)
	}
}
func TestForwardRewrite(t *testing.T) {
	s := ForwardSpec{Name: "box.localhost", Ports: []PortMap{{3000, 3000}, {4000, 4000}}, Rewrite: true}
	for _, v := range []string{"https://localhost:3000/x", "https://provider.test/login?redirect_uri=http://localhost:3000/callback", "http://localhost:5000/x", "http://example.test:3000/x"} {
		if rewriteURL(v, s, true) != v {
			t.Fatal(v)
		}
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "localhost:3000" || r.Header.Get("Origin") != "http://localhost:3000" {
			t.Errorf("upstream %s %s", r.Host, r.Header.Get("Origin"))
		}
		w.Header().Set("Location", "http://localhost:3000/home")
		w.Header().Add("Set-Cookie", "session=value; Domain=localhost; HttpOnly; Secure; SameSite=Lax")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", "old")
		io.WriteString(w, `{"url":"http://localhost:4000/callback","n":9007199254740993}`)
	}))
	defer upstream.Close()
	proxy, tr := httpForward(s, s.Ports[0], func(ctx context.Context, _ int) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", strings.TrimPrefix(upstream.URL, "http://"))
	})
	defer tr.CloseIdleConnections()
	r := httptest.NewRequest("GET", "http://box.localhost:3000/", nil)
	r.Header.Set("Origin", "http://box.localhost:3000")
	w := httptest.NewRecorder()
	proxy.ServeHTTP(w, r)
	res := w.Result()
	b, _ := io.ReadAll(res.Body)
	if res.Header.Get("Location") != "http://box.localhost:3000/home" || !strings.Contains(string(b), "http://box.localhost:4000/callback") || !strings.Contains(string(b), "9007199254740993") || res.Header.Get("ETag") != "" {
		t.Fatalf("%v %s", res.Header, b)
	}
	cookie := res.Header.Get("Set-Cookie")
	if strings.Contains(cookie, "Domain=") || !strings.Contains(cookie, "Secure") || !strings.Contains(cookie, "HttpOnly") {
		t.Fatal(cookie)
	}
}
func TestForwardLargeJSONUnchanged(t *testing.T) {
	body := `{"url":"http://localhost:3000/","padding":"` + strings.Repeat("x", 2<<20) + `"}`
	r := &http.Response{Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
	if e := rewriteResponse(r, ForwardSpec{Name: "box.localhost", Ports: []PortMap{{3000, 3000}}}); e != nil {
		t.Fatal(e)
	}
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if string(b) != body {
		t.Fatal("large body changed")
	}
}

func TestForwardWebSocketUpgrade(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, b, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer c.Close()
		fmt.Fprint(b, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		b.Flush()
		io.Copy(c, b)
	}))
	defer upstream.Close()
	s := ForwardSpec{Name: "box.localhost", Ports: []PortMap{{3000, 3000}}, Rewrite: true}
	proxy, tr := httpForward(s, s.Ports[0], func(ctx context.Context, _ int) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", strings.TrimPrefix(upstream.URL, "http://"))
	})
	defer tr.CloseIdleConnections()
	server := httptest.NewServer(proxy)
	defer server.Close()
	c, e := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	fmt.Fprint(c, "GET / HTTP/1.1\r\nHost: box.localhost:3000\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
	r := bufio.NewReader(c)
	res, e := http.ReadResponse(r, nil)
	if e != nil {
		t.Fatal(e)
	}
	if res.StatusCode != 101 {
		t.Fatal(res.Status)
	}
	fmt.Fprint(c, "duplex")
	b := make([]byte, 6)
	if _, e = io.ReadFull(r, b); e != nil || string(b) != "duplex" {
		t.Fatalf("%q %v", b, e)
	}
}
