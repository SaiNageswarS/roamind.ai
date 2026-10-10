// Command memory will host the long-term memory service (phase 2): a
// per-user isolation layer in front of Basic Memory. For now it only serves a
// health check so the deployment topology is in place.
package main

import (
	"log/slog"
	"net/http"
	"os"
)

func main() {
	addr := os.Getenv("ROAMIND_MEMORY_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8790"
	}
	http.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	slog.Info("memory placeholder listening", "addr", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		slog.Error("memory exited", "err", err)
		os.Exit(1)
	}
}
