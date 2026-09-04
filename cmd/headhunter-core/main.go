// Command headhunter-core is the Headhunter engine: a Postgres-backed data
// model + state machine, SQL analytics, an MCP-shaped API, and the on-demand
// scraper operator that launches one Kubernetes Job per ATS.
package main

import (
	"log"
	"net/http"
	"os"
)

func main() {
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	// TODO(phase-1): /api/tools, /api/tools/{name}, /api/cycle, /api/scan/ingest
	log.Printf("headhunter-core listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
