package proxy

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"smart-proxy/internal/store"
)

// gRPC through an ingress controller arrives as HTTP/2 without TLS and must reach the
// application the same way (with trailers).
func TestH2CRequestsReachTheAppOverH2C(t *testing.T) {
	app := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Trailer", "Grpc-Status")
		w.Header().Set("Content-Type", "application/grpc")
		io.WriteString(w, r.Proto)
		w.Header().Set("Grpc-Status", "0")
	}))
	app.Config.Protocols = new(http.Protocols)
	app.Config.Protocols.SetUnencryptedHTTP2(true)
	app.Start()
	defer app.Close()
	defer func(d func(context.Context, string, string) (net.Conn, error)) { dial = d }(dial)
	dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, app.Listener.Addr().String())
	}

	h, _, _ := webHandler(t, []*store.RouteConfig{webRoute("ing-grpc", "/", "grpc")}, ready("grpc", 1))
	h.Transport = nil // the real transports
	front := httptest.NewUnstartedServer(h)
	front.Config.Protocols = new(http.Protocols)
	front.Config.Protocols.SetHTTP1(true)
	front.Config.Protocols.SetUnencryptedHTTP2(true)
	front.Start()
	defer front.Close()

	client := &http.Client{Transport: &http.Transport{Protocols: func() *http.Protocols {
		p := new(http.Protocols)
		p.SetUnencryptedHTTP2(true)
		return p
	}()}}
	req, _ := http.NewRequest("POST", front.URL+"/pkg.Service/Method", nil)
	req.Host = "web.example.com"
	req.Header.Set("Content-Type", "application/grpc")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "HTTP/2.0" || resp.Trailer.Get("Grpc-Status") != "0" {
		t.Fatalf("app saw %q, trailer %q", body, resp.Trailer.Get("Grpc-Status"))
	}
}
