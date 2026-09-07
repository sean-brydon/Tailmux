package tailmux

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type PortMap struct {
	Local  int `json:"local"`
	Remote int `json:"remote"`
}
type ForwardSpec struct {
	Target      string         `json:"target"`
	Ports       []PortMap      `json:"ports"`
	Name        string         `json:"name,omitempty"`
	Rewrite     bool           `json:"rewrite_redirects,omitempty"`
	Save        string         `json:"save,omitempty"`
	Public      *PublicForward `json:"public,omitempty"`
	BindAddress string         `json:"bind_address,omitempty"`
}
type PublicForward struct {
	Provider string `json:"provider"`
	URL      string `json:"url"`
	Tunnel   string `json:"tunnel,omitempty"`
}
type ForwardInfo struct {
	ID          string      `json:"id"`
	Spec        ForwardSpec `json:"spec"`
	State       string      `json:"state"`
	Error       string      `json:"error,omitempty"`
	PublicState string      `json:"public_state,omitempty"`
	PublicError string      `json:"public_error,omitempty"`
}

func parsePorts(values []string) ([]PortMap, error) {
	var maps []PortMap
	seen := map[int]bool{}
	add := func(l, r int) error {
		if l < 1 || l > 65535 || r < 1 || r > 65535 {
			return fmt.Errorf("ports must be 1–65535")
		}
		if seen[l] {
			return fmt.Errorf("duplicate local port %d", l)
		}
		if len(maps) >= 100 {
			return fmt.Errorf("at most 100 ports per forward")
		}
		seen[l] = true
		maps = append(maps, PortMap{l, r})
		return nil
	}
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			if strings.Contains(part, ":") {
				a, b, ok := strings.Cut(part, ":")
				l, e1 := strconv.Atoi(a)
				r, e2 := strconv.Atoi(b)
				if !ok || e1 != nil || e2 != nil {
					return nil, fmt.Errorf("invalid mapping %q", part)
				}
				if err := add(l, r); err != nil {
					return nil, err
				}
			} else if strings.Contains(part, "-") {
				a, b, _ := strings.Cut(part, "-")
				l, e1 := strconv.Atoi(a)
				r, e2 := strconv.Atoi(b)
				if e1 != nil || e2 != nil || r < l || l < 1 || r > 65535 || r-l >= 100 {
					return nil, fmt.Errorf("invalid range %q", part)
				}
				for p := l; p <= r; p++ {
					if err := add(p, p); err != nil {
						return nil, err
					}
				}
			} else {
				p, err := strconv.Atoi(part)
				if err != nil {
					return nil, fmt.Errorf("invalid port %q", part)
				}
				if err = add(p, p); err != nil {
					return nil, err
				}
			}
		}
	}
	if len(maps) == 0 {
		return nil, fmt.Errorf("specify a port, range, or local:remote mapping")
	}
	return maps, nil
}
func (s ForwardSpec) validate() error {
	if len(s.Ports) == 0 || len(s.Ports) > 100 {
		return fmt.Errorf("specify between 1 and 100 ports")
	}
	seen := map[int]bool{}
	for _, p := range s.Ports {
		if p.Local < 1 || p.Local > 65535 || p.Remote < 1 || p.Remote > 65535 || seen[p.Local] {
			return fmt.Errorf("invalid or repeated local port %d", p.Local)
		}
		seen[p.Local] = true
	}
	if s.Name != "" {
		if len(s.Name) > 253 || s.Name != strings.ToLower(s.Name) || net.ParseIP(s.Name) != nil {
			return fmt.Errorf("invalid local hostname %q", s.Name)
		}
		for _, label := range strings.Split(s.Name, ".") {
			if !safeName.MatchString(label) || strings.Contains(label, "_") || strings.HasSuffix(label, "-") {
				return fmt.Errorf("invalid local hostname %q", s.Name)
			}
		}
	}
	if s.Save != "" && !safeName.MatchString(s.Save) {
		return fmt.Errorf("invalid saved forward name %q", s.Save)
	}
	if address := s.listenerAddress(); net.ParseIP(address) == nil || net.ParseIP(address).To4() == nil || !net.ParseIP(address).IsLoopback() {
		return fmt.Errorf("forward bind address must be an IPv4 loopback address")
	}
	if (s.Name == "" || s.Public != nil) && s.listenerAddress() != "127.0.0.1" {
		return fmt.Errorf("raw and public forwards must bind to 127.0.0.1")
	}
	if s.Public != nil {
		if len(s.Ports) != 1 {
			return fmt.Errorf("public URLs require exactly one forwarded port")
		}
		if s.Public.Provider != "cloudflare" && s.Public.Provider != "ngrok" {
			return fmt.Errorf("unsupported public URL provider %q", s.Public.Provider)
		}
		if s.Public.Provider == "cloudflare" && !safeName.MatchString(s.Public.Tunnel) {
			return fmt.Errorf("--cloudflare needs a tunnel name or UUID")
		}
		u, err := url.Parse(s.Public.URL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return fmt.Errorf("public URL must be an HTTPS origin without a path, query, or credentials")
		}
		if net.ParseIP(u.Hostname()) != nil || strings.Contains(u.Hostname(), "_") {
			return fmt.Errorf("public URL must use a DNS hostname")
		}
		if u.Port() != "" {
			port, err := strconv.Atoi(u.Port())
			if err != nil || port < 1 || port > 65535 {
				return fmt.Errorf("public URL has an invalid port")
			}
		}
	}
	return nil
}
func (s ForwardSpec) listenerAddress() string {
	if s.BindAddress != "" {
		return s.BindAddress
	}
	return "127.0.0.1"
}
func localhostName(name string) bool {
	return name == "localhost" || name == "127.0.0.1" || name == "::1"
}

// Only rewrite direct loopback URLs for ports belonging to this forward group.
// External OAuth URLs (including embedded redirect_uri parameters) remain untouched.
func rewriteURL(value string, s ForwardSpec, outward bool) string {
	u, err := url.Parse(value)
	if err != nil || u.User != nil || u.Host == "" || (u.Scheme != "" && u.Scheme != "http" && (s.Public == nil || u.Scheme != "https")) {
		return value
	}
	port := 80
	if u.Scheme == "https" {
		port = 443
	}
	if u.Port() != "" {
		port, err = strconv.Atoi(u.Port())
		if err != nil {
			return value
		}
	}
	for _, p := range s.Ports {
		if outward && localhostName(strings.ToLower(u.Hostname())) && port == p.Remote {
			if s.Public != nil {
				public, _ := url.Parse(s.Public.URL)
				u.Scheme, u.Host = public.Scheme, public.Host
			} else {
				u.Host = net.JoinHostPort(s.Name, strconv.Itoa(p.Local))
			}
			return u.String()
		}
		inwardName, inwardPort := s.Name, p.Local
		if s.Public != nil {
			public, _ := url.Parse(s.Public.URL)
			inwardName = public.Hostname()
			inwardPort = 443
			if public.Port() != "" {
				inwardPort, _ = strconv.Atoi(public.Port())
			}
		}
		if !outward && strings.EqualFold(u.Hostname(), inwardName) && port == inwardPort {
			u.Scheme = "http"
			u.Host = net.JoinHostPort("localhost", strconv.Itoa(p.Remote))
			return u.String()
		}
	}
	return value
}
func rewriteJSON(v any, s ForwardSpec) (any, bool) {
	changed := false
	switch x := v.(type) {
	case string:
		r := rewriteURL(x, s, true)
		return r, r != x
	case []any:
		for i, v := range x {
			r, c := rewriteJSON(v, s)
			x[i] = r
			changed = changed || c
		}
	case map[string]any:
		for k, v := range x {
			r, c := rewriteJSON(v, s)
			x[k] = r
			changed = changed || c
		}
	}
	return v, changed
}
func rewriteResponse(resp *http.Response, s ForwardSpec) error {
	for _, header := range []string{"Location", "Content-Location"} {
		if v := resp.Header.Get(header); v != "" {
			resp.Header.Set(header, rewriteURL(v, s, true))
		}
	}
	if v := resp.Header.Get("Refresh"); v != "" {
		parts := strings.SplitN(v, ";", 2)
		if len(parts) == 2 {
			tail := strings.TrimSpace(parts[1])
			if len(tail) > 4 && strings.EqualFold(tail[:4], "url=") {
				value := strings.Trim(strings.TrimSpace(tail[4:]), "\"'")
				resp.Header.Set("Refresh", parts[0]+"; url="+rewriteURL(value, s, true))
			}
		}
	}
	cookies := resp.Header.Values("Set-Cookie")
	if len(cookies) > 0 {
		resp.Header.Del("Set-Cookie")
		for _, raw := range cookies {
			parts := strings.Split(raw, ";")
			kept := parts[:1]
			for _, p := range parts[1:] {
				key, value, ok := strings.Cut(strings.TrimSpace(p), "=")
				if ok && strings.EqualFold(key, "domain") && localhostName(strings.TrimPrefix(strings.ToLower(value), ".")) {
					continue
				}
				kept = append(kept, p)
			}
			resp.Header.Add("Set-Cookie", strings.Join(kept, ";"))
		}
	}
	// Bound buffering and leave streams, JavaScript, HTML and compressed bodies intact.
	if !strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "application/json") || resp.Header.Get("Content-Encoding") != "" || resp.Body == nil {
		return nil
	}
	const limit = 2 << 20
	original := resp.Body
	b, err := io.ReadAll(io.LimitReader(original, limit+1))
	if err != nil {
		original.Close()
		return err
	}
	if len(b) > limit {
		resp.Body = &joinedBody{Reader: io.MultiReader(bytes.NewReader(b), original), Closer: original}
		return nil
	}
	original.Close()
	var value any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&value); err == nil {
		if _, err = dec.Token(); err == io.EOF {
			if updated, changed := rewriteJSON(value, s); changed {
				b, err = json.Marshal(updated)
				if err != nil {
					return err
				}
				resp.Header.Del("ETag")
				resp.Header.Del("Last-Modified")
			}
		}
	}
	resp.Body = io.NopCloser(bytes.NewReader(b))
	resp.ContentLength = int64(len(b))
	resp.Header.Set("Content-Length", strconv.Itoa(len(b)))
	return nil
}

type joinedBody struct {
	io.Reader
	io.Closer
}

func httpForward(s ForwardSpec, p PortMap, dial func(context.Context, int) (net.Conn, error)) (*httputil.ReverseProxy, *http.Transport) {
	upstream := &url.URL{Scheme: "http", Host: net.JoinHostPort("localhost", strconv.Itoa(p.Remote))}
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx, p.Remote) }, MaxIdleConnsPerHost: 16, IdleConnTimeout: 60 * time.Second, ResponseHeaderTimeout: 30 * time.Second}
	proxy := &httputil.ReverseProxy{Transport: tr, FlushInterval: -1, Rewrite: func(r *httputil.ProxyRequest) {
		r.SetURL(upstream)
		r.Out.Host = upstream.Host
		r.SetXForwarded()
		if s.Public != nil {
			public, _ := url.Parse(s.Public.URL)
			r.Out.Header.Set("X-Forwarded-Proto", public.Scheme)
			r.Out.Header.Set("X-Forwarded-Host", public.Host)
		}
		if s.Rewrite {
			r.Out.Header.Del("Accept-Encoding")
			for _, h := range []string{"Origin", "Referer"} {
				if v := r.Out.Header.Get(h); v != "" {
					r.Out.Header.Set(h, rewriteURL(v, s, false))
				}
			}
		}
	}, ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
		serveAppUnavailable(w, r, p.Remote)
	}}
	if s.Rewrite {
		proxy.ModifyResponse = func(r *http.Response) error { return rewriteResponse(r, s) }
	}
	return proxy, tr
}
