package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/jsvisa/bitracer/internal/labeler"
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

func (s *Server) pinTerminal(w http.ResponseWriter, r *http.Request) {
	addr := r.PathValue("address")
	if addr == "" {
		writeErr(w, http.StatusBadRequest, errors.New("address required"))
		return
	}
	var b struct {
		Kind string `json:"kind"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil && !errors.Is(err, io.EOF) {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	}
	if b.Kind == "" {
		b.Kind = "manual"
	}
	if !labeler.ValidTerminalKind(b.Kind) {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("kind must be one of %s", strings.Join(labeler.TerminalKinds, ", ")))
		return
	}
	if err := s.lbl.PinTerminal(r.Context(), addr, b.Kind); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"pinned": addr, "kind": b.Kind})
}

func (s *Server) unpinTerminal(w http.ResponseWriter, r *http.Request) {
	addr := r.PathValue("address")
	if addr == "" {
		writeErr(w, http.StatusBadRequest, errors.New("address required"))
		return
	}
	if err := s.lbl.UnpinTerminal(r.Context(), addr); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"unpinned": addr})
}
