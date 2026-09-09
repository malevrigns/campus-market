// Command mall runs the edu-mall backend service (Part 1 of the course).
// It wires the in-memory stores -> service -> net/http handlers and serves on :8081.
// In production the memory stores are replaced by GORM/MySQL adapters (same interfaces),
// and handlers are mounted on Gin; the business logic is unchanged.
package main

import (
	"log"
	"net/http"
	"os"
	"strconv"

	"qingfly/edu-mall/internal/pkg/snowflake"
	"qingfly/edu-mall/mall/handler"
	"qingfly/edu-mall/mall/repository"
	"qingfly/edu-mall/mall/service"
)

func main() {
	if os.Getenv("ALLOW_UNSAFE_LEGACY_DEMO") != "true" || os.Getenv("APP_ENV") == "production" {
		log.Fatal("This is a legacy in-memory teaching demo. Run go run ./cmd/server for the campus application.")
	}
	// --- data layer (swap for GORM/MySQL in production) ---
	orders := repository.NewMemoryOrderStore()
	stock := repository.NewMemoryStockStore(map[string]int64{})
	// seed a couple of SKUs with stock so the demo is exercisable end-to-end.
	_ = stock.Seed("sku-golang-course", 1000)
	_ = stock.Seed("sku-agent-course", 500)

	// --- service layer ---
	nodeID, _ := strconv.Atoi(os.Getenv("SNOWFLAKE_NODE"))
	idgen := snowflake.NewNode(int64(nodeID))
	svc := service.NewOrderService(orders, stock, idgen)

	// --- API layer ---
	mux := http.NewServeMux()
	h := handler.New(svc, []byte(os.Getenv("GATEWAY_SECRET")))
	for route, fn := range h.Routes() {
		mux.HandleFunc(route, fn)
	}
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	// Serve the visual test console at "/" so users can open http://localhost:8081/
	// in a browser and click through every service (localhost = secure context for
	// WebCrypto HMAC; cross-port calls rely on the CORS headers below).
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.ServeFile(w, r, "console.html")
	})

	addr := ":8081"
	if v := os.Getenv("MALL_ADDR"); v != "" {
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
		log.Fatalf("[mall] serve: %v", err)
	}
}
