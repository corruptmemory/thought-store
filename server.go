package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jim/thought-store/templates"
)

type Server struct {
	router      *chi.Mux
	brain       ThoughtStore
	store       *SQLiteStore // for sync operations
	dbPath      string       // for sync export
	instanceUID string       // for sync import
	modelID     int          // for sync import
	vecIndex    *VecIndex    // for updating after sync import
}

func NewServer(brain ThoughtStore) *Server {
	s := &Server{
		router: chi.NewRouter(),
		brain:  brain,
	}
	s.router.Use(middleware.Logger)
	s.router.Use(middleware.Recoverer)

	// Pages
	s.router.Get("/", s.handleHome)

	// API routes
	s.router.Post("/api/thoughts", s.handleCapture)
	s.router.Get("/api/thoughts", s.handleList)
	s.router.Get("/api/search", s.handleSearch)
	s.router.Get("/api/stats", s.handleStats)
	s.router.Get("/api/diagnostics", s.handleDiagnostics)

	// Sync routes
	s.router.Get("/api/sync", s.handleSyncExport)
	s.router.Post("/api/sync", s.handleSyncImport)
	s.router.Post("/api/reload", s.handleReload)

	// Static files
	fs := http.FileServer(http.Dir("static"))
	s.router.Handle("/static/*", http.StripPrefix("/static/", fs))

	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

// SetSyncState configures the server for sync operations.
func (s *Server) SetSyncState(store *SQLiteStore, dbPath, instanceUID string, modelID int, vecIndex *VecIndex) {
	s.store = store
	s.dbPath = dbPath
	s.instanceUID = instanceUID
	s.modelID = modelID
	s.vecIndex = vecIndex
}

func isHTMX(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	thoughts, err := s.brain.List(r.Context(), 50, 0)
	if err != nil {
		http.Error(w, "list failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	data := make([]templates.ThoughtData, len(thoughts))
	for i, t := range thoughts {
		data[i] = templates.ThoughtData{
			ID:        t.ID,
			Content:   t.Content,
			Source:    t.Source,
			CreatedAt: t.CreatedAt,
		}
	}

	templates.Home(data).Render(r.Context(), w)
}

type captureRequest struct {
	Content  string                 `json:"content"`
	Metadata []ThoughtMetadataEntry `json:"metadata,omitempty"`
}

func (s *Server) handleCapture(w http.ResponseWriter, r *http.Request) {
	var content string
	var metadata []ThoughtMetadataEntry

	if r.Header.Get("Content-Type") == "application/x-www-form-urlencoded" {
		content = r.FormValue("content")
	} else {
		var req captureRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		content = req.Content
		metadata = req.Metadata
	}

	if content == "" {
		http.Error(w, "content is required", http.StatusBadRequest)
		return
	}

	source := "web"
	if r.Header.Get("X-Source") != "" {
		source = r.Header.Get("X-Source")
	}

	thought, err := s.brain.Capture(r.Context(), content, source, metadata)
	if err != nil {
		http.Error(w, "capture failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if isHTMX(r) {
		data := templates.ThoughtData{
			ID:        thought.ID,
			Content:   thought.Content,
			Source:    thought.Source,
			CreatedAt: thought.CreatedAt,
		}
		templates.ThoughtCard(data).Render(r.Context(), w)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(thought)
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 20
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	thoughts, err := s.brain.List(r.Context(), limit, offset)
	if err != nil {
		http.Error(w, "list failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(thoughts)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.brain.Stats(r.Context())
	if err != nil {
		http.Error(w, "stats failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		http.Error(w, "q parameter is required", http.StatusBadRequest)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 5
	}

	results, err := s.brain.Search(r.Context(), q, limit)
	if err != nil {
		http.Error(w, "search failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if isHTMX(r) {
		data := make([]templates.ThoughtData, len(results))
		for i, res := range results {
			sim := res.Similarity
			data[i] = templates.ThoughtData{
				ID:         res.ID,
				Content:    res.Content,
				Source:     res.Source,
				CreatedAt:  res.CreatedAt,
				Similarity: &sim,
			}
		}
		templates.ThoughtList(data).Render(r.Context(), w)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	report, err := s.brain.Diagnostics(r.Context())
	if err != nil {
		http.Error(w, "diagnostics failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(report)
}

func (s *Server) handleSyncExport(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		http.Error(w, "sync not configured", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=brain.tar.zst")

	if err := ExportBundle(r.Context(), s.store, s.dbPath, w); err != nil {
		fmt.Fprintf(os.Stderr, "sync export error: %v\n", err)
	}
}

func (s *Server) handleSyncImport(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		http.Error(w, "sync not configured", http.StatusServiceUnavailable)
		return
	}

	result, err := ImportBundle(r.Context(), s.store, s.instanceUID, s.modelID, r.Body, s.vecIndex)
	if err != nil {
		http.Error(w, "import failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	if s.store == nil || s.vecIndex == nil {
		http.Error(w, "not configured", http.StatusServiceUnavailable)
		return
	}

	ids, vectors, err := s.store.LoadEmbeddings(r.Context(), s.modelID)
	if err != nil {
		http.Error(w, "reload failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	s.vecIndex.Load(ids, vectors)

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"reloaded":%d}`, len(ids))
}
