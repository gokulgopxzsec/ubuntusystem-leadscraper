package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/makeforme/leadscraper/internal/adapters/db/postgres"
)

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if body == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// The status line is already sent, so this can only be logged.
		slog.Error("encode response failed", "error", err)
	}
}

type errorBody struct {
	Error string `json:"error"`
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorBody{Error: msg})
}

// writeRepoError maps a repository error to a status code, so a missing row
// returns 404 rather than a 500 that looks like an outage.
func writeRepoError(w http.ResponseWriter, err error, what string) {
	if errors.Is(err, postgres.ErrNotFound) {
		writeError(w, http.StatusNotFound, what+" not found")
		return
	}
	slog.Error(what+" failed", "error", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

func queryInt(r *http.Request, key string, def int) int {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return v
}

// queryBool returns nil when the parameter is absent, which is different from
// it being present and false.
func queryBool(r *http.Request, key string) *bool {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return nil
	}
	return &v
}

type listResponse struct {
	Data  any   `json:"data"`
	Total int64 `json:"total"`
	Page  int   `json:"page"`
	Limit int   `json:"limit"`
}
