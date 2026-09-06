package tailmux

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
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
