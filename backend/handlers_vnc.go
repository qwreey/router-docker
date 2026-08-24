// The VNC tab's HTTP layer - same conventions as handlers_approutes.go/
// handlers_devproxy.go (reads open, gate.RequirePassword on every mutating
// route). Thin on purpose: internal/vnc owns the App-Route-in-lockstep
// bookkeeping, this file only maps it onto HTTP.
package main

import (
	"encoding/json"
	"errors"
	"net/http"

	"router/internal/approutes"
	"router/internal/vnc"
)

// vncTargetsResponse ships the backend picker's options alongside the list
// itself, so the frontend doesn't hand-duplicate a list only the backend
// actually knows (see internal/vnc.Backends - Selkies is meant to land
// there and show up in the UI without a matching frontend change).
type vncTargetsResponse struct {
	Targets  []vnc.Info `json:"targets"`
	Backends []string   `json:"backends"`
}

func handleListVncTargets(w http.ResponseWriter, r *http.Request) {
	list, err := vnc.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, vncTargetsResponse{Targets: list, Backends: vnc.Backends()})
}

func handleCreateVncTarget(w http.ResponseWriter, r *http.Request) {
	var body vnc.Target
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := vnc.Create(r.Context(), body); err != nil {
		writeVncError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func handleUpdateVncTarget(w http.ResponseWriter, r *http.Request) {
	oldName := r.PathValue("name")
	var body vnc.Target
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// An omitted name means "keep this one" rather than "rename to empty" -
	// same contract handleUpdateAppRoute already uses.
	if body.Name == "" {
		body.Name = oldName
	}
	if err := vnc.Update(r.Context(), oldName, body); err != nil {
		writeVncError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func handleDeleteVncTarget(w http.ResponseWriter, r *http.Request) {
	if err := vnc.Delete(r.Context(), r.PathValue("name")); err != nil {
		writeVncError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func writeVncError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, vnc.ErrTargetExists), errors.Is(err, approutes.ErrAppExists):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, vnc.ErrTargetNotFound), errors.Is(err, approutes.ErrAppNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, approutes.ErrReloadFailed):
		// The fragment and the registry are both already written - this is
		// an apply failure, not bad client input. Same split
		// writeAppRouteError makes.
		writeError(w, http.StatusInternalServerError, err.Error())
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
}
