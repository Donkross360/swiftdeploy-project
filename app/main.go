package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

// chaosState holds the current chaos mode for all handlers.
type chaosState struct {
	mu     sync.RWMutex
	action string
}

// set stores the active chaos action with write locking.
func (c *chaosState) set(action string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.action = action
}

// get returns the active chaos action with read locking.
func (c *chaosState) get() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.action
}

// main wires runtime config, endpoints, and server startup.
func main() {
	// Runtime behavior is controlled by environment so promote can switch mode
	// without requiring a new image build.
	mode := getenvDefault("MODE", "stable")
	version := getenvDefault("VERSION", "1.0.0")
	port := getenvDefault("PORT", "3000")
	chaos := &chaosState{action: "recover"}

	mux := http.NewServeMux()

	// Root endpoint exposes deploy metadata used during stable/canary verification.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		applyChaos(w, chaos.get())
		if chaos.get() == "error" {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"status":  "error",
				"message": "chaos mode forcing errors",
			})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"service": "swiftdeploy-api",
			"mode":    mode,
			"version": version,
			"chaos":   chaos.get(),
		})
	})

	// Health endpoint is the contract used by swiftdeploy readiness checks.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		applyChaos(w, chaos.get())
		if chaos.get() == "error" {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"status": "unhealthy",
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"status": "ok",
		})
	})

	// Chaos endpoint enables controlled failure modes for rollout testing.
	mux.HandleFunc("/chaos", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{
				"error": "method not allowed",
			})
			return
		}

		var payload struct {
			Action string `json:"action"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "invalid json payload",
			})
			return
		}

		switch payload.Action {
		case "slow", "error", "recover":
			chaos.set(payload.Action)
			writeJSON(w, http.StatusOK, map[string]string{
				"status": payload.Action,
			})
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "action must be one of: slow, error, recover",
			})
		}
	})

	// Keep server-level timeouts explicit to reduce slowloris-style risk.
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("swiftdeploy-api listening on :%s mode=%s version=%s", port, mode, version)
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func applyChaos(w http.ResponseWriter, action string) {
	if action == "slow" {
		// Deterministic latency helps test timeouts and rollback behavior.
		time.Sleep(3 * time.Second)
		w.Header().Set("X-Chaos-Mode", "slow")
	}
}

// writeJSON standardizes JSON response headers and encoding.
func writeJSON(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(payload)
}

// getenvDefault reads an env var and falls back when unset.
func getenvDefault(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}
