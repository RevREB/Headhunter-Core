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
	"github.com/RevREB/Headhunter-Core/internal/llm"
	"github.com/RevREB/Headhunter-Core/internal/store"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// connectWithRetry waits out first-boot races (e.g. CNPG still provisioning the
// role/database) rather than crash-looping.
func connectWithRetry(dsn string, budget time.Duration) *store.Store {
	deadline := time.Now().Add(budget)
	for attempt := 1; ; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		st, err := store.Open(ctx, dsn)
		cancel()
		if err == nil {
			return st
		}
		if time.Now().After(deadline) {
			log.Fatalf("db connect: gave up after %d attempts: %v", attempt, err)
		}
		log.Printf("db connect attempt %d failed: %v; retrying in 3s", attempt, err)
		time.Sleep(3 * time.Second)
	}
}

func main() {
	addr := env("LISTEN_ADDR", ":8080")
	dsn := os.Getenv("DATABASE_URL")

	var st *store.Store
	var an *analytics.Analytics
	if dsn != "" {
		st = connectWithRetry(dsn, 120*time.Second)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := st.Migrate(ctx); err != nil {
			log.Fatalf("migrate: %v", err)
		}
		an = analytics.New(st.Pool)
		log.Printf("connected to Postgres; schema migrated")
	} else {
		log.Printf("DATABASE_URL unset — serving health only (degraded mode)")
	}

	srv := api.New(st, an, llm.FromEnv())
	log.Printf("headhunter-core listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, srv.Routes()))
}
