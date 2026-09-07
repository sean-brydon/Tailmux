package tailmux

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNoForwardBrowserAndAPI(t *testing.T) {
	for _, accept := range []string{"text/html", "application/json"} {
		r := httptest.NewRequest("GET", "http://unknown.test:3000/", nil)
		r.Host = "<script>alert(1)</script>"
		r.Header.Set("Accept", accept)
		w := httptest.NewRecorder()
		serveNoForward(w, r)
		if w.Code != http.StatusMisdirectedRequest {
			t.Fatal(w.Code)
		}
		body := w.Body.String()
		if strings.Contains(body, "<script>") {
			t.Fatal("host was not escaped")
		}
		if accept == "text/html" {
			if !strings.Contains(body, "Wrong doorstep.") || !strings.Contains(body, "&lt;script&gt;") {
				t.Fatal("missing HTML or escaped host")
			}
		} else if body != "No Tailmux forward for this hostname\n" {
			t.Fatal("API response changed")
		}
	}
}

func TestUnavailableAppHasBrowserRecoveryPage(t *testing.T) {
	for _, accept := range []string{"text/html", "application/json"} {
		r := httptest.NewRequest("GET", "http://devl.test:3003/", nil)
		r.Header.Set("Accept", accept)
		w := httptest.NewRecorder()
		serveAppUnavailable(w, r, 3003)
		if w.Code != http.StatusBadGateway {
			t.Fatal(w.Code)
		}
		if accept == "text/html" {
			if !strings.Contains(w.Body.String(), "Nobody’s home.") || !strings.Contains(w.Body.String(), "remote port 3003") {
				t.Fatal("missing app recovery page")
			}
		} else if w.Body.String() != "Tailmux could not reach the remote application\n" {
			t.Fatal("API response changed")
		}
	}
}
