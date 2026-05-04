package main

import (
	"encoding/json"
	"log"
	"math/rand"
	"net/http"
	"os"
	"sync"
	"time"
)

// chaosState tracks active chaos behavior across requests.
type chaosState struct {
	mu           sync.RWMutex
	mode         string
	slowDuration time.Duration
	errorRate    float64
}

// setSlow enables a deterministic request delay.
func (c *chaosState) setSlow(durationSeconds int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mode = "slow"
	c.slowDuration = time.Duration(durationSeconds) * time.Second
	c.errorRate = 0
}

// setError enables probabilistic HTTP 500 responses.
func (c *chaosState) setError(rate float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mode = "error"
	c.errorRate = rate
	c.slowDuration = 0
}

// recover disables all active chaos behavior.
func (c *chaosState) recover() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mode = "recover"
	c.slowDuration = 0
	c.errorRate = 0
}

// snapshot returns a consistent chaos configuration view for handlers.
func (c *chaosState) snapshot() (string, time.Duration, float64) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.mode, c.slowDuration, c.errorRate
}

// main wires runtime config, endpoints, and server startup.
func main() {
	// Runtime behavior is controlled by environment so promote can switch mode
	// without requiring a new image build.
	mode := getenvDefault("MODE", "stable")
	version := getenvDefault("APP_VERSION", "1.0.0")
	port := getenvDefault("APP_PORT", "3000")
	startedAt := time.Now()
	chaos := &chaosState{mode: "recover"}
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	mux := http.NewServeMux()

	// Canary responses must always advertise canary mode.
	withModeHeader := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if mode == "canary" {
				w.Header().Set("X-Mode", "canary")
			}
			next(w, r)
		}
	}

	// Root endpoint exposes deploy metadata used during stable/canary verification.
	mux.HandleFunc("/", withModeHeader(func(w http.ResponseWriter, r *http.Request) {
		if shouldFail := applyChaos(chaos, mode, rng); shouldFail {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"status":  "error",
				"message": "chaos error mode triggered",
			})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"message":   "welcome to swiftdeploy service",
			"mode":      mode,
			"version":   version,
			"timestamp": time.Now().UTC().Format(time.RFC3339),
		})
	}))

	// Health endpoint is the contract used by swiftdeploy readiness checks.
	mux.HandleFunc("/healthz", withModeHeader(func(w http.ResponseWriter, r *http.Request) {
		if shouldFail := applyChaos(chaos, mode, rng); shouldFail {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"status":  "error",
				"message": "chaos error mode triggered",
			})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"status":         "ok",
			"uptime_seconds": int(time.Since(startedAt).Seconds()),
		})
	}))

	// Chaos endpoint enables controlled failure modes for rollout testing.
	mux.HandleFunc("/chaos", withModeHeader(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{
				"error": "method not allowed",
			})
			return
		}

		if mode != "canary" {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error": "chaos endpoint is only active in canary mode",
			})
			return
		}

		var payload struct {
			Mode     string  `json:"mode"`
			Duration int     `json:"duration"`
			Rate     float64 `json:"rate"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "invalid json payload",
			})
			return
		}

		switch payload.Mode {
		case "slow":
			if payload.Duration <= 0 {
				writeJSON(w, http.StatusBadRequest, map[string]string{
					"error": "duration must be > 0 for slow mode",
				})
				return
			}
			chaos.setSlow(payload.Duration)
			writeJSON(w, http.StatusOK, map[string]any{
				"status":   "slow",
				"duration": payload.Duration,
			})
		case "error":
			if payload.Rate < 0 || payload.Rate > 1 {
				writeJSON(w, http.StatusBadRequest, map[string]string{
					"error": "rate must be between 0 and 1 for error mode",
				})
				return
			}
			chaos.setError(payload.Rate)
			writeJSON(w, http.StatusOK, map[string]any{
				"status": "error",
				"rate":   payload.Rate,
			})
		case "recover":
			chaos.recover()
			writeJSON(w, http.StatusOK, map[string]any{
				"status": "recover",
			})
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "mode must be one of: slow, error, recover",
			})
		}
	}))

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

func applyChaos(state *chaosState, mode string, rng *rand.Rand) bool {
	// Chaos behavior only activates in canary mode.
	if mode != "canary" {
		return false
	}

	chaosMode, slowDuration, errorRate := state.snapshot()
	switch chaosMode {
	case "slow":
		time.Sleep(slowDuration)
		return false
	case "error":
		if errorRate <= 0 {
			return false
		}
		return rng.Float64() < errorRate
	case "recover", "":
		return false
	default:
		return false
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

