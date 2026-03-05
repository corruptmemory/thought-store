package main

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

type Server struct {
	router *chi.Mux
	store  *Store
	ollama *OllamaClient
}

func NewServer(store *Store, ollama *OllamaClient) *Server {
	s := &Server{
		router: chi.NewRouter(),
		store:  store,
		ollama: ollama,
	}
	s.router.Use(middleware.Logger)
	s.router.Use(middleware.Recoverer)

	// API routes
	s.router.Post("/api/thoughts", s.handleCapture)
	s.router.Get("/api/thoughts", s.handleList)
	s.router.Get("/api/search", s.handleSearch)

	// Static files
	fs := http.FileServer(http.Dir("static"))
	s.router.Handle("/static/*", http.StripPrefix("/static/", fs))

	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

type captureRequest struct {
	Content string `json:"content"`
}

func (s *Server) handleCapture(w http.ResponseWriter, r *http.Request) {
	var req captureRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Content == "" {
		http.Error(w, "content is required", http.StatusBadRequest)
		return
	}

	embedding, err := s.ollama.Embed(r.Context(), req.Content)
	if err != nil {
		http.Error(w, "embedding failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	source := "web"
	if r.Header.Get("X-Source") != "" {
		source = r.Header.Get("X-Source")
	}

	thought, err := s.store.Insert(r.Context(), req.Content, embedding, source)
	if err != nil {
		http.Error(w, "store failed: "+err.Error(), http.StatusInternalServerError)
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

	thoughts, err := s.store.List(r.Context(), limit, offset)
	if err != nil {
		http.Error(w, "list failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(thoughts)
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

	embedding, err := s.ollama.Embed(r.Context(), q)
	if err != nil {
		http.Error(w, "embedding failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	results, err := s.store.Search(r.Context(), embedding, limit)
	if err != nil {
		http.Error(w, "search failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}
