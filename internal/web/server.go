package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"time"

	webassets "github.com/cows-project/cows/web"
)

type Server struct {
	db        *sql.DB
	templates *template.Template
	static    fs.FS
}

type healthSnapshot struct {
	Status    string
	Database  string
	CheckedAt string
}

type pageData struct {
	Title  string
	Health healthSnapshot
}

func New(db *sql.DB) (*Server, error) {
	templates, err := template.ParseFS(webassets.Files, "templates/layouts/*.html", "templates/pages/*.html", "templates/fragments/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	static, err := fs.Sub(webassets.Files, "static")
	if err != nil {
		return nil, fmt.Errorf("open static assets: %w", err)
	}
	return &Server{db: db, templates: templates, static: static}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(s.static))))
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /fragments/health", s.healthFragment)
	mux.HandleFunc("GET /", s.home)
	return mux
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data := pageData{Title: "COWS", Health: s.snapshot(r.Context())}
	if err := s.templates.ExecuteTemplate(w, "base", data); err != nil {
		http.Error(w, "failed to render page", http.StatusInternalServerError)
	}
}

func (s *Server) healthFragment(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "health-status", s.snapshot(r.Context())); err != nil {
		http.Error(w, "failed to render health status", http.StatusInternalServerError)
	}
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	snapshot := s.snapshot(r.Context())
	status := http.StatusOK
	if snapshot.Status != "ok" {
		status = http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Status   string `json:"status"`
		Database string `json:"database"`
	}{Status: snapshot.Status, Database: snapshot.Database})
}

func (s *Server) snapshot(ctx context.Context) healthSnapshot {
	snapshot := healthSnapshot{Status: "ok", Database: "ok", CheckedAt: time.Now().UTC().Format(time.RFC3339)}
	if err := s.db.PingContext(ctx); err != nil {
		snapshot.Status = "degraded"
		snapshot.Database = "unavailable"
	}
	return snapshot
}
