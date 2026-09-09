// Command im runs the edu-mall IM service (Part 3 of the course).
// It wires a pusher -> service -> net/http handlers and serves on :8082.
package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"qingfly/edu-mall/im/handler"
	"qingfly/edu-mall/im/model"
	"qingfly/edu-mall/im/service"
)

// logPusher is an offline stand-in for the real WS gRPC pusher.
type logPusher struct{}

func (logPusher) Push(_ context.Context, convID, excludeUserID string, msg *model.Message) error {
	return nil
}

func main() {
	svc := service.NewService(logPusher{})
	mux := http.NewServeMux()
	h := handler.New(svc)
	routes := h.Routes()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if fn, ok := routes[r.Method+" "+r.URL.Path]; ok {
			fn(w, r)
			return
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })

	addr := ":8082"
	if v := os.Getenv("IM_ADDR"); v != "" {
		addr = v
	}
	cors := withCORS(mux)
	log.Printf("[im] listening on %s", addr)
	if err := http.ListenAndServe(addr, cors); err != nil {
		log.Fatalf("[im] serve: %v", err)
	}
}

// withCORS lets the local browser console call across ports during demo.
func withCORS(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type,Accept")
		if r.Method == "OPTIONS" { w.WriteHeader(http.StatusNoContent); return }
		h.ServeHTTP(w, r)
	})
}
