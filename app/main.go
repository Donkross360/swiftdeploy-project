package main

import (
	"encoding/json"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
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

func (c *chaosState) metricValue() float64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	// Keep gauge encoding explicit and stable for policy/status consumers:
	// recover/none=0, slow=1, error=2.
	switch c.mode {
	case "slow":
		return 1
	case "error":
		return 2
	default:
		return 0
	}
}

type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
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

	httpRequestsTotal := promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests handled by method, path, and status code.",
		},
		[]string{"method", "path", "status_code"},
	)
	// Keep request latency as histogram buckets so downstream tooling can compute P99.
	httpRequestDurationSeconds := promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "Histogram of HTTP request latency in seconds.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "path"},
	)
	// Expose process uptime as a live gauge function (seconds since startup).
	appUptimeSeconds := promauto.NewGaugeFunc(
		prometheus.GaugeOpts{
			Name: "app_uptime_seconds",
			Help: "Uptime of the API process in seconds.",
		},
		func() float64 { return time.Since(startedAt).Seconds() },
	)
	_ = appUptimeSeconds
	appMode := promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "app_mode",
			Help: "Current mode where stable=0 and canary=1.",
		},
	)
	chaosActive := promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "chaos_active",
			Help: "Current chaos state where none=0, slow=1, error=2.",
		},
	)

	if mode == "canary" {
		appMode.Set(1)
	} else {
		appMode.Set(0)
	}
	chaosActive.Set(chaos.metricValue())

	// Canary responses must always advertise canary mode.
	withModeHeader := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if mode == "canary" {
				w.Header().Set("X-Mode", "canary")
			}
			next.ServeHTTP(w, r)
		})
	}

	promHandler := promhttp.Handler()

	// Exact (method, path) routing: unknown paths get 404 (so OPA-ish URLs cannot hit the "/" JSON).
	api := withModeHeader(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case r.Method == http.MethodGet && p == "/":
			if shouldFail := applyChaos(chaos, mode); shouldFail {
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
		case r.Method == http.MethodGet && p == "/healthz":
			if shouldFail := applyChaos(chaos, mode); shouldFail {
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
		case r.Method == http.MethodGet && p == "/metrics":
			promHandler.ServeHTTP(w, r)
		case r.Method == http.MethodPost && p == "/chaos":
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
				chaosActive.Set(chaos.metricValue())
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
				chaosActive.Set(chaos.metricValue())
				writeJSON(w, http.StatusOK, map[string]any{
					"status": "error",
					"rate":   payload.Rate,
				})
			case "recover":
				chaos.recover()
				chaosActive.Set(chaos.metricValue())
				writeJSON(w, http.StatusOK, map[string]any{
					"status": "recover",
				})
			default:
				writeJSON(w, http.StatusBadRequest, map[string]string{
					"error": "mode must be one of: slow, error, recover",
				})
			}
		default:
			http.NotFound(w, r)
		}
	}))

	// Capture throughput/error/latency uniformly for every route response.
	instrumentedMux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{
			ResponseWriter: w,
			statusCode:     http.StatusOK,
		}

		api.ServeHTTP(recorder, r)

		path := r.URL.Path
		httpRequestsTotal.WithLabelValues(r.Method, path, strconv.Itoa(recorder.statusCode)).Inc()
		httpRequestDurationSeconds.WithLabelValues(r.Method, path).Observe(time.Since(start).Seconds())
	})

	// Keep server-level timeouts explicit to reduce slowloris-style risk.
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           instrumentedMux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("swiftdeploy-api listening on :%s mode=%s version=%s", port, mode, version)
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func applyChaos(state *chaosState, mode string) bool {
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
		return rand.Float64() < errorRate
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

