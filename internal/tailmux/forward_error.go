package tailmux

import (
	_ "embed"
	"fmt"
	"html/template"
	"net/http"
	"strings"
)

//go:embed pages/no-route.html
var noRouteHTML string
var noRouteTemplate = template.Must(template.New("no-route").Parse(noRouteHTML))

type forwardErrorPage struct {
	Host                                                            string
	Code                                                            int
	Label, Heading, Punchline, Description, RecoveryTitle, Recovery string
}

func serveNoForward(w http.ResponseWriter, r *http.Request) {
	serveForwardError(w, r, forwardErrorPage{
		Host: r.Host, Code: http.StatusMisdirectedRequest,
		Label: "Unclaimed packet", Heading: "Right network.", Punchline: "Wrong doorstep.",
		Description:   "Your request made it to Tailmux, but there’s no forward assigned to this hostname. A small detour. We’ll get you there.",
		RecoveryTitle: "Let’s find your box.",
		Recovery:      "Check the hostname and port, then open your dashboard to inspect or create a forward.",
	}, "No Tailmux forward for this hostname")
}
func serveAppUnavailable(w http.ResponseWriter, r *http.Request, port int) {
	serveForwardError(w, r, forwardErrorPage{
		Host: r.Host, Code: http.StatusBadGateway,
		Label: "Delivery on hold", Heading: "Found your box.", Punchline: "Nobody’s home.",
		Description:   "This forward is configured, but Tailmux couldn’t reach the remote application. It may be stopped, starting up, or disconnected.",
		RecoveryTitle: "Let’s get it answering.",
		Recovery:      fmt.Sprintf("Check that your app is listening on remote port %d. In Tailmux, select the box and press p to inspect ports, or check the forward’s connection status. Then try again.", port),
	}, "Tailmux could not reach the remote application")
}
func serveForwardError(w http.ResponseWriter, r *http.Request, page forwardErrorPage, plain string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if !strings.Contains(r.Header.Get("Accept"), "text/html") {
		http.Error(w, plain, page.Code)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	w.WriteHeader(page.Code)
	_ = noRouteTemplate.Execute(w, page)
}
