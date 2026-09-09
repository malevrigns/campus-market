// Command websocket runs the edu-mall WebSocket connection layer (Part 2 of the course).
// It wires a sink -> hub -> HTTP management handlers and serves on :8083.
// In production the hub's online state is Redis-backed and fan-out goes through
// OpenResty+Nchan; here an in-memory sink keeps it runnable offline.
package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"qingfly/edu-mall/ws/hub"
	"qingfly/edu-mall/ws/server"
)

func main() {
	if os.Getenv("ALLOW_UNSAFE_LEGACY_DEMO") != "true" || os.Getenv("APP_ENV") == "production" {
		log.Fatal("This is a legacy in-memory teaching demo. Run go run ./cmd/server for the campus application.")
	}
	sink := server.MemorySink{}
	h := hub.New(sink, 90*time.Second) // online-state TTL

	mux := http.NewServeMux()
	srv := server.New(h)
	{
		routes := srv.Routes()
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if fn, ok := routes[r.Method+" "+r.URL.Path]; ok {
				fn(w, r)
				return
			}
			http.NotFound(w, r)
		})
	}
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })

	addr := ":8083"
	if v := os.Getenv("WS_ADDR"); v != "" {
		addr = v
	}
	cors := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type,Accept")
			if r.Method == "OPTIONS" {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			h.ServeHTTP(w, r)
		})
	}
	if err := http.ListenAndServe(addr, cors(mux)); err != nil {
		log.Fatalf("[websocket] serve: %v", err)
	}
}
