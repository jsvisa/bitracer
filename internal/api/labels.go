package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/jsvisa/bitracer/internal/labels"
)

func (s *Server) lookupLabel(w http.ResponseWriter, r *http.Request) {
	addr := r.URL.Query().Get("address")
	if addr == "" {
		writeErr(w, http.StatusBadRequest, errors.New("address required"))
		return
	}
	lbl, err := s.lbl.ResolveAddress(r.Context(), addr)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if lbl == nil {
		lbl = &labels.Label{}
	}
	writeJSON(w, http.StatusOK, lbl)
}

func (s *Server) resolveCaseLabels(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if _, err := s.st.GetCase(r.Context(), id); err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if err := s.lbl.Pass(r.Context(), id, 200); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "resolved"})
}
