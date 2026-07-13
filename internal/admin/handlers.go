package admin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Hoot-Code/Hoot-Lb/internal/config"
	"github.com/Hoot-Code/Hoot-Lb/internal/discovery"
	"github.com/Hoot-Code/Hoot-Lb/internal/logging"
	"github.com/Hoot-Code/Hoot-Lb/internal/runtime"
)

type handler struct {
	snap           *runtime.AtomicSnapshot
	cfg            *config.Config
	logger         *slog.Logger
	restartTrigger func() error
	audit          *AuditLogger
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, ErrorResponse{Error: msg})
}

// extractToken extracts the bearer token from the request's
// Authorization header. Returns empty string if not present.
func extractToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(auth, "Bearer ")
}

func extractPoolName(path string) string {
	path = strings.TrimPrefix(path, "/admin/pools/")
	idx := strings.Index(path, "/")
	if idx < 0 {
		return path
	}
	return path[:idx]
}

func extractBackendAddress(path string) string {
	path = strings.TrimPrefix(path, "/admin/pools/")
	idx := strings.Index(path, "/backends/")
	if idx < 0 {
		return ""
	}
	addr := path[idx+len("/backends/"):]
	addr = strings.TrimSuffix(addr, "/drain")
	addr = strings.TrimSuffix(addr, "/undrain")
	return addr
}

func (h *handler) listPools(w http.ResponseWriter, r *http.Request) {
	snap := h.snap.Load()
	poolInfos := make([]PoolInfo, 0, len(h.cfg.Pools))

	for _, p := range h.cfg.Pools {
		info := PoolInfo{
			Name:      p.Name,
			Algorithm: p.Algorithm,
			Discovery: p.Discovery != nil,
		}

		if ps, ok := snap.PoolStates[p.Name]; ok {
			info.Backends = make([]BackendInfo, len(ps.Servers))
			for i, srv := range ps.Servers {
				info.Backends[i] = BackendInfo{
					Address:  srv.Address(),
					Weight:   srv.Weight(),
					Healthy:  srv.IsHealthy(),
					Draining: srv.IsDraining(),
				}
			}
		}

		poolInfos = append(poolInfos, info)
	}

	writeJSON(w, http.StatusOK, ListPoolsResponse{Pools: poolInfos})
}

func (h *handler) findPoolConfig(name string) (*config.PoolConfig, bool) {
	for i := range h.cfg.Pools {
		if h.cfg.Pools[i].Name == name {
			return &h.cfg.Pools[i], true
		}
	}
	return nil, false
}

func (h *handler) addBackend(w http.ResponseWriter, r *http.Request) {
	poolName := extractPoolName(r.URL.Path)

	poolCfg, ok := h.findPoolConfig(poolName)
	if !ok {
		writeError(w, http.StatusNotFound, "pool not found")
		return
	}

	if poolCfg.Discovery != nil {
		writeError(w, http.StatusConflict, "cannot add backends to a discovery-driven pool")
		return
	}

	var req AddBackendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Address == "" {
		writeError(w, http.StatusBadRequest, "address is required")
		return
	}

	oldSnap := h.snap.Load()
	ps, ok := oldSnap.PoolStates[poolName]
	if !ok {
		writeError(w, http.StatusNotFound, "pool not found")
		return
	}

	for _, srv := range ps.Servers {
		if srv.Address() == req.Address {
			writeError(w, http.StatusConflict, "backend address already exists")
			return
		}
	}

	newBackends := make([]discovery.Backend, 0, len(ps.Servers)+1)
	for _, srv := range ps.Servers {
		newBackends = append(newBackends, discovery.Backend{Address: srv.Address(), Weight: srv.Weight()})
	}
	newBackends = append(newBackends, discovery.Backend{Address: req.Address, Weight: req.Weight})

	newSnap, err := runtime.UpdatePoolBackends(oldSnap, poolName, newBackends, *poolCfg, h.logger)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update pool backends")
		return
	}

	ctx := context.Background()
	if newChecker, ok := newSnap.Checkers[poolName]; ok {
		newChecker.Start(ctx)
	}

	h.snap.Swap(newSnap)

	if oldChecker, ok := oldSnap.Checkers[poolName]; ok {
		oldChecker.Stop()
	}

	h.logger.Info("backend added",
		slog.String(logging.ComponentKey, "admin"),
		slog.String(logging.PoolKey, poolName),
		slog.String(logging.BackendKey, req.Address))

	if h.audit != nil {
		h.audit.LogMutation("add_backend", "success", extractToken(r), getRole(r))
	}

	w.WriteHeader(http.StatusCreated)
}

func (h *handler) removeBackend(w http.ResponseWriter, r *http.Request) {
	poolName := extractPoolName(r.URL.Path)
	address := extractBackendAddress(r.URL.Path)

	poolCfg, ok := h.findPoolConfig(poolName)
	if !ok {
		writeError(w, http.StatusNotFound, "pool not found")
		return
	}

	if poolCfg.Discovery != nil {
		writeError(w, http.StatusConflict, "cannot remove backends from a discovery-driven pool")
		return
	}

	oldSnap := h.snap.Load()
	ps, ok := oldSnap.PoolStates[poolName]
	if !ok {
		writeError(w, http.StatusNotFound, "pool not found")
		return
	}

	found := false
	for _, srv := range ps.Servers {
		if srv.Address() == address {
			found = true
			break
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, "backend not found")
		return
	}

	newBackends := make([]discovery.Backend, 0, len(ps.Servers)-1)
	for _, srv := range ps.Servers {
		if srv.Address() != address {
			newBackends = append(newBackends, discovery.Backend{Address: srv.Address(), Weight: srv.Weight()})
		}
	}

	newSnap, err := runtime.UpdatePoolBackends(oldSnap, poolName, newBackends, *poolCfg, h.logger)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update pool backends")
		return
	}

	ctx := context.Background()
	if newChecker, ok := newSnap.Checkers[poolName]; ok {
		newChecker.Start(ctx)
	}

	h.snap.Swap(newSnap)

	if oldChecker, ok := oldSnap.Checkers[poolName]; ok {
		oldChecker.Stop()
	}

	h.logger.Info("backend removed",
		slog.String(logging.ComponentKey, "admin"),
		slog.String(logging.PoolKey, poolName),
		slog.String(logging.BackendKey, address))

	if h.audit != nil {
		h.audit.LogMutation("remove_backend", "success", extractToken(r), getRole(r))
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) drainBackend(w http.ResponseWriter, r *http.Request) {
	poolName := extractPoolName(r.URL.Path)
	address := extractBackendAddress(r.URL.Path)

	snap := h.snap.Load()
	ps, ok := snap.PoolStates[poolName]
	if !ok {
		writeError(w, http.StatusNotFound, "pool not found")
		return
	}

	found := false
	for _, srv := range ps.Servers {
		if srv.Address() == address {
			srv.SetDraining(true)
			found = true
			break
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, "backend not found")
		return
	}

	h.logger.Info("backend set to draining",
		slog.String(logging.ComponentKey, "admin"),
		slog.String(logging.PoolKey, poolName),
		slog.String(logging.BackendKey, address))

	if h.audit != nil {
		h.audit.LogMutation("drain", "success", extractToken(r), getRole(r))
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) undrainBackend(w http.ResponseWriter, r *http.Request) {
	poolName := extractPoolName(r.URL.Path)
	address := extractBackendAddress(r.URL.Path)

	snap := h.snap.Load()
	ps, ok := snap.PoolStates[poolName]
	if !ok {
		writeError(w, http.StatusNotFound, "pool not found")
		return
	}

	found := false
	for _, srv := range ps.Servers {
		if srv.Address() == address {
			srv.SetDraining(false)
			found = true
			break
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, "backend not found")
		return
	}

	h.logger.Info("backend draining cleared",
		slog.String(logging.ComponentKey, "admin"),
		slog.String(logging.PoolKey, poolName),
		slog.String(logging.BackendKey, address))

	if h.audit != nil {
		h.audit.LogMutation("undrain", "success", extractToken(r), getRole(r))
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) restart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.restartTrigger == nil {
		writeError(w, http.StatusNotImplemented, "restart not supported on this platform")
		return
	}
	if err := h.restartTrigger(); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		if h.audit != nil {
			h.audit.LogMutation("restart", "failure", extractToken(r), getRole(r))
		}
		return
	}
	if h.audit != nil {
		h.audit.LogMutation("restart", "success", extractToken(r), getRole(r))
	}
	writeJSON(w, http.StatusAccepted, RestartResponse{Status: "restart initiated"})
}
