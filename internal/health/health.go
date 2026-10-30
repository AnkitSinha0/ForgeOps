// Package health serves the two probes every ForgeOps service exposes.
//
//	/healthz — liveness: "is the process alive?" Never touches dependencies,
//	           so a Postgres outage does not make an orchestrator restart
//	           every healthy service in a loop.
//	/readyz  — readiness: "can this instance do useful work right now?"
//	           Pings each dependency; 503 tells a load balancer to stop
//	           sending traffic until it recovers.
package health

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"go.uber.org/zap"
)

// checkTimeout bounds one readiness probe. Orchestrators typically give a probe
// ~1-5s; a hung dependency must not hang the probe past that.
const checkTimeout = 2 * time.Second

type Check struct {
	Name string
	Ping func(ctx context.Context) error
}

type Handler struct {
	service string
	checks  []Check
	log     *zap.Logger
}

func NewHandler(service string, log *zap.Logger, checks ...Check) *Handler {
	return &Handler{service: service, checks: checks, log: log}
}

type response struct {
	Status  string            `json:"status"`
	Service string            `json:"service"`
	Checks  map[string]string `json:"checks,omitempty"`
}

func (h *Handler) Live(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, response{Status: "ok", Service: h.service})
}

func (h *Handler) Ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), checkTimeout)
	defer cancel()

	status, code := "ready", http.StatusOK
	results := make(map[string]string, len(h.checks))
	for _, c := range h.checks {
		if err := c.Ping(ctx); err != nil {
			// The error goes to the log, not the response: it can contain
			// internal hostnames and ports, and /readyz may be reachable publicly.
			h.log.Warn("readiness check failed", zap.String("check", c.Name), zap.Error(err))
			results[c.Name] = "fail"
			status, code = "not_ready", http.StatusServiceUnavailable
			continue
		}
		results[c.Name] = "ok"
	}

	writeJSON(w, code, response{Status: status, Service: h.service, Checks: results})
}

// OpsMux is the router for services whose only HTTP surface is operational
// (the executor, and the router's separate ops port).
func (h *Handler) OpsMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.Live)
	mux.HandleFunc("GET /readyz", h.Ready)
	return mux
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
