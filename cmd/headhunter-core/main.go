// Command headhunter-core is the Headhunter engine: a Postgres-backed data
// model + state machine, SQL analytics, an MCP-shaped API, and (soon) the
// on-demand scraper operator.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/RevREB/Headhunter-Core/internal/analytics"
	"github.com/RevREB/Headhunter-Core/internal/api"
	"github.com/RevREB/Headhunter-Core/internal/store"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	addr := env("LISTEN_ADDR", ":8080")
	dsn := os.Getenv("DATABASE_URL")

	var st *store.Store
	var an *analytics.Analytics
	if dsn != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var err error
		st, err = store.Open(ctx, dsn)
		if err != nil {
			log.Fatalf("db connect: %v", err)
		}
		if err := st.Migrate(ctx); err != nil {
			log.Fatalf("migrate: %v", err)
		}
		an = analytics.New(st.Pool)
		log.Printf("connected to Postgres; schema migrated")
	} else {
		log.Printf("DATABASE_URL unset — serving health only (degraded mode)")
	}

	srv := api.New(st, an)
	log.Printf("headhunter-core listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, srv.Routes()))
}
