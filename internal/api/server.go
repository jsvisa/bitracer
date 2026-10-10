package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jsvisa/bitracer/internal/btc"
	"github.com/jsvisa/bitracer/internal/casegraph"
	"github.com/jsvisa/bitracer/internal/labeler"
	"github.com/jsvisa/bitracer/internal/notify"
	"github.com/jsvisa/bitracer/internal/store"
)

type Server struct {
	st  *store.Store
	rpc *btc.Client
	lbl *labeler.Service
}

func New(st *store.Store, rpc *btc.Client, lbl *labeler.Service) *Server {
	return &Server{st: st, rpc: rpc, lbl: lbl}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/cases", s.listCases)
	mux.HandleFunc("POST /api/cases", s.createCase)
	mux.HandleFunc("GET /api/cases/{id}", s.getCase)
	mux.HandleFunc("PATCH /api/cases/{id}", s.patchCase)
	mux.HandleFunc("DELETE /api/cases/{id}", s.deleteCase)
	mux.HandleFunc("GET /api/cases/{id}/txs", s.listCaseTxs)
	mux.HandleFunc("POST /api/cases/{id}/txs", s.addCaseTx)
	mux.HandleFunc("DELETE /api/cases/{id}/txs/{txid}", s.deleteCaseTx)
	mux.HandleFunc("GET /api/channels", s.listChannels)
	mux.HandleFunc("POST /api/channels", s.addChannel)
	mux.HandleFunc("POST /api/channels/test", s.testChannel)
	mux.HandleFunc("DELETE /api/channels/{id}", s.deleteChannel)
	mux.HandleFunc("GET /api/cases/{id}/channels", s.listCaseChannels)
	mux.HandleFunc("PUT /api/cases/{id}/channels", s.setCaseChannels)
	mux.HandleFunc("GET /api/alerts", s.listAlerts)
	mux.HandleFunc("GET /api/sync", s.syncStatus)
	mux.HandleFunc("GET /api/graph", s.graph)
	mux.HandleFunc("GET /api/label", s.lookupLabel)
	mux.HandleFunc("POST /api/cases/{id}/resolve-labels", s.resolveCaseLabels)
	mux.HandleFunc("POST /api/addresses/{address}/terminal", s.pinTerminal)
	mux.HandleFunc("DELETE /api/addresses/{address}/terminal", s.unpinTerminal)
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type channelBody struct {
	Name   string          `json:"name"`
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config"`
	// CaseID is test-only: when set, the test message carries that
	// case's fund-flow graph image.
	CaseID int64 `json:"case_id,omitempty"`
}

type caseBody struct {
	Name       string   `json:"name"`
	Status     string   `json:"status"`
	MinBTC     *float64 `json:"min_btc"`
	DepthCap   *int32   `json:"depth_cap"`
	BranchCap  *int32   `json:"branch_cap"`
	ChannelIDs []int64  `json:"channel_ids"`
}

func (ch *channelBody) validate() error {
	if strings.TrimSpace(ch.Name) == "" {
		return errors.New("channel name required")
	}
	return nil
}

func (s *Server) createCase(w http.ResponseWriter, r *http.Request) {
	var b caseBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(b.Name) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("name required"))
		return
	}
	if len(b.ChannelIDs) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("at least one notify channel must be bound to the case"))
		return
	}
	if err := s.st.ValidateChannelIDs(r.Context(), b.ChannelIDs); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("unknown channel id in channel_ids"))
		return
	}
	var minSats *int64
	if b.MinBTC != nil && *b.MinBTC > 0 {
		v := int64(*b.MinBTC * 1e8)
		minSats = &v
	}
	depthCap, branchCap := int32(50), int32(500)
	if b.DepthCap != nil && *b.DepthCap > 0 {
		depthCap = *b.DepthCap
	}
	if b.BranchCap != nil && *b.BranchCap > 0 {
		branchCap = *b.BranchCap
	}
	c, err := s.st.CreateCase(r.Context(), b.Name, minSats, depthCap, branchCap)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.st.SetCaseChannels(r.Context(), c.ID, b.ChannelIDs); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) listCases(w http.ResponseWriter, r *http.Request) {
	cases, err := s.st.ListCases(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if cases == nil {
		cases = []store.Case{}
	}
	writeJSON(w, http.StatusOK, cases)
}

func (s *Server) getCase(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	c, err := s.st.GetCase(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) patchCase(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var b caseBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if b.Status != "" {
		if b.Status != "active" && b.Status != "paused" {
			writeErr(w, http.StatusBadRequest, errors.New("status must be active or paused"))
			return
		}
		if err := s.st.SetCaseStatus(r.Context(), id, b.Status); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		return
	}
	c, err := s.st.GetCase(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	name := c.Name
	if b.Name != "" {
		name = b.Name
	}
	minSats := c.MinSats
	if b.MinBTC != nil {
		if *b.MinBTC <= 0 {
			minSats = nil
		} else {
			v := int64(*b.MinBTC * 1e8)
			minSats = &v
		}
	}
	depthCap, branchCap := c.DepthCap, c.BranchCap
	if b.DepthCap != nil && *b.DepthCap > 0 {
		depthCap = *b.DepthCap
	}
	if b.BranchCap != nil && *b.BranchCap > 0 {
		branchCap = *b.BranchCap
	}
	if err := s.st.UpdateCase(r.Context(), id, name, minSats, depthCap, branchCap); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"updated": strconv.FormatInt(id, 10)})
}

func (s *Server) deleteCase(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.st.DeleteCase(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"deleted": strconv.FormatInt(id, 10)})
}

func (s *Server) listCaseTxs(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	txs, err := s.st.ListCaseTxs(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if txs == nil {
		txs = []store.CaseTx{}
	}
	writeJSON(w, http.StatusOK, txs)
}

func (s *Server) addCaseTx(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var b struct {
		Txid string `json:"txid"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if !isTxid(b.Txid) {
		writeErr(w, http.StatusBadRequest, errors.New("txid must be 64 hex chars"))
		return
	}
	if _, err := s.st.GetCase(r.Context(), id); err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if err := s.st.AddCaseTx(r.Context(), id, strings.ToLower(b.Txid)); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"added": b.Txid})
}

func (s *Server) deleteCaseTx(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.st.DeleteCaseTx(r.Context(), id, r.PathValue("txid")); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"deleted": r.PathValue("txid")})
}

func (s *Server) listChannels(w http.ResponseWriter, r *http.Request) {
	chs, err := s.st.ListChannels(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if chs == nil {
		chs = []store.Channel{}
	}
	writeJSON(w, http.StatusOK, chs)
}

func (s *Server) addChannel(w http.ResponseWriter, r *http.Request) {
	var b channelBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := b.validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if _, err := validateChannel(b.Type, b.Config); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ch, err := s.st.CreateChannel(r.Context(), strings.TrimSpace(b.Name), b.Type, b.Config)
	if err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			writeErr(w, http.StatusConflict, errors.New("a channel with this name already exists"))
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, ch)
}

func (s *Server) testChannel(w http.ResponseWriter, r *http.Request) {
	var b channelBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	n, err := buildNotifier(b.Type, b.Config)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	msg := notify.TestMessage()
	if b.CaseID > 0 {
		msg.CaseID = b.CaseID
		if png := casegraph.CaseGraphPNG(r.Context(), s.st, b.CaseID); len(png) > 0 {
			msg.PNG = png
		}
	}
	if err := n.Send(r.Context(), msg); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("test message failed: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "test message sent"})
}

func (s *Server) deleteChannel(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.st.DeleteChannel(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"deleted": strconv.FormatInt(id, 10)})
}

func (s *Server) listCaseChannels(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	chs, err := s.st.ListCaseChannels(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if chs == nil {
		chs = []store.Channel{}
	}
	writeJSON(w, http.StatusOK, chs)
}

func (s *Server) setCaseChannels(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var b struct {
		ChannelIDs []int64 `json:"channel_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if len(b.ChannelIDs) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("at least one notify channel must stay bound to the case"))
		return
	}
	if err := s.st.SetCaseChannels(r.Context(), id, b.ChannelIDs); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusBadRequest, errors.New("unknown channel id in channel_ids"))
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"updated": strconv.FormatInt(id, 10)})
}

func (s *Server) listAlerts(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 && l <= 1000 {
		limit = l
	}
	caseID, _ := strconv.ParseInt(r.URL.Query().Get("case_id"), 10, 64)
	alerts, err := s.st.ListAlerts(r.Context(), caseID, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if alerts == nil {
		alerts = []store.Alert{}
	}
	writeJSON(w, http.StatusOK, alerts)
}

type syncStatusBody struct {
	LastHeight  int64  `json:"last_height"`
	LastBlockTs int64  `json:"last_block_ts"`
	UpdatedAt   string `json:"updated_at"`
	ChainHeight *int64 `json:"chain_height"`
	LagBlocks   *int64 `json:"lag_blocks"`
}

func (s *Server) syncStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.st.SyncState(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	body := syncStatusBody{
		LastHeight: st.LastHeight,
		UpdatedAt:  st.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if ts, err := s.st.BlockTime(r.Context(), st.LastHeight); err == nil {
		body.LastBlockTs = ts
	}
	if info, err := s.rpc.Info(r.Context()); err == nil {
		h := info.Blocks
		body.ChainHeight = &h
		lag := h - st.LastHeight
		if lag < 0 {
			lag = 0
		}
		body.LagBlocks = &lag
	}
	writeJSON(w, http.StatusOK, body)
}

func isTxid(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

func validateChannel(typ string, cfg json.RawMessage) (any, error) {
	n, err := buildNotifier(typ, cfg)
	if err != nil {
		return nil, err
	}
	return n, nil
}
