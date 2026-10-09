// Command echoapp is the application the end-to-end tests put behind Smart Proxy: it reports
// what it received, streams, upgrades connections and speaks h2c, using the standard library only.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, "<html><body>echoapp</body></html>")
	})
	// What the application received.
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		h := sha256.New()
		n, err := io.Copy(h, r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"method": r.Method, "path": r.URL.Path, "proto": r.Proto, "host": r.Host,
			"bytes": n, "sha256": hex.EncodeToString(h.Sum(nil)), "pod": os.Getenv("HOSTNAME"),
			"variant": os.Getenv("VARIANT"), "user": r.Header.Get("X-Smart-Proxy-User"),
			"authorization": r.Header.Get("Authorization"), "cookies": r.Header.Get("Cookie"),
		})
	})
	// Server-sent events, one per second.
	mux.HandleFunc("/sse", func(w http.ResponseWriter, r *http.Request) {
		seconds, _ := strconv.Atoi(r.URL.Query().Get("seconds"))
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < seconds; i++ {
			fmt.Fprintf(w, "data: %d\n\n", i)
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(time.Second):
			}
		}
	})
	// A WebSocket-style upgrade, then an echo of whatever the client sends.
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "websocket" {
			http.Error(w, "upgrade required", http.StatusUpgradeRequired)
			return
		}
		conn, buf, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		buf.Flush()
		io.Copy(conn, buf)
	})

	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	srv := &http.Server{Addr: ":8080", Handler: mux, Protocols: &protocols, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
