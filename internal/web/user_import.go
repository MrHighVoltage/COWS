package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/cows-project/cows/internal/auth"
	"github.com/cows-project/cows/internal/domain"
)

const (
	maxUserImportBytes = 2 << 20
	maxUserImportRows  = 1000
	userImportTTL      = 15 * time.Minute
	userDownloadTTL    = 10 * time.Minute
)

type userImportPageData struct {
	Previews         []auth.ImportUserPreview
	Groups           []domain.Group
	SelectedGroupIDs []string
	GroupNames       map[string]string
	DraftToken       string
	DownloadURL      string
	CreatedCount     int
	UpdatedCount     int
	Error            string
}

type userImportDraft struct {
	ActorID   string
	Inputs    []auth.ImportUserInput
	GroupIDs  []string
	Previews  []auth.ImportUserPreview
	CreatedAt time.Time
}

type userImportDownload struct {
	ActorID   string
	CSV       []byte
	ExpiresAt time.Time
}

type userImportStore struct {
	mu        sync.Mutex
	drafts    map[string]userImportDraft
	downloads map[string]userImportDownload
}

func newUserImportStore() *userImportStore {
	return &userImportStore{drafts: make(map[string]userImportDraft), downloads: make(map[string]userImportDownload)}
}

func (s *userImportStore) putDraft(draft userImportDraft) (string, error) {
	token, err := opaqueImportToken()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(time.Now().UTC())
	s.drafts[token] = draft
	return token, nil
}

func (s *userImportStore) getDraft(actorID, token string) (userImportDraft, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	draft, ok := s.drafts[token]
	if !ok || draft.ActorID != actorID || time.Since(draft.CreatedAt) > userImportTTL {
		return userImportDraft{}, false
	}
	return draft, true
}

func (s *userImportStore) deleteDraft(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.drafts, token)
}

func (s *userImportStore) putDownload(actorID string, data []byte) (string, error) {
	token, err := opaqueImportToken()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(time.Now().UTC())
	s.downloads[token] = userImportDownload{ActorID: actorID, CSV: append([]byte(nil), data...), ExpiresAt: time.Now().UTC().Add(userDownloadTTL)}
	return token, nil
}

func (s *userImportStore) getDownload(actorID, token string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	download, ok := s.downloads[token]
	if !ok || download.ActorID != actorID || time.Now().UTC().After(download.ExpiresAt) {
		return nil, false
	}
	return append([]byte(nil), download.CSV...), true
}

func (s *userImportStore) cleanupLocked(now time.Time) {
	for token, draft := range s.drafts {
		if now.Sub(draft.CreatedAt) > userImportTTL {
			delete(s.drafts, token)
		}
	}
	for token, download := range s.downloads {
		if now.After(download.ExpiresAt) {
			delete(s.downloads, token)
		}
	}
}

func opaqueImportToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func parseUserImportCSV(source io.Reader) ([]auth.ImportUserInput, error) {
	data, err := io.ReadAll(io.LimitReader(source, maxUserImportBytes+1))
	if err != nil || len(data) > maxUserImportBytes {
		return nil, auth.ErrInvalidUserImport
	}
	reader := csv.NewReader(bytes.NewReader(data))
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil {
		return nil, auth.ErrInvalidUserImport
	}
	if len(header) > 0 {
		header[0] = strings.TrimPrefix(header[0], "\ufeff")
	}
	if len(header) != 3 || header[0] != "username" || header[1] != "email" || header[2] != "display_name" {
		return nil, auth.ErrInvalidUserImport
	}
	inputs := make([]auth.ImportUserInput, 0, 32)
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || len(record) != 3 {
			return nil, auth.ErrInvalidUserImport
		}
		if len(inputs) >= maxUserImportRows {
			return nil, auth.ErrInvalidUserImport
		}
		inputs = append(inputs, auth.ImportUserInput{Username: strings.TrimSpace(record[0]), Email: strings.TrimSpace(record[1]), DisplayName: strings.TrimSpace(record[2])})
	}
	if len(inputs) == 0 {
		return nil, auth.ErrInvalidUserImport
	}
	return inputs, nil
}

func (s *Server) userImportPage(ctx context.Context, actorID string, data userImportPageData) (pageData, error) {
	groups, err := s.auth.ListGroups(ctx, actorID)
	if err != nil {
		return pageData{}, err
	}
	if data.GroupNames == nil {
		data.GroupNames = make(map[string]string, len(groups))
	}
	for _, group := range groups {
		data.GroupNames[group.ID] = group.Name
	}
	data.Groups = groups
	return pageData{Title: "Import users | COWS", UserImport: &data}, nil
}

func (s *Server) adminUsersImport(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAdministrator(w, r)
	if !ok {
		return
	}
	data, err := s.userImportPage(r.Context(), user.ID, userImportPageData{})
	if err != nil {
		http.Error(w, "failed to load groups", http.StatusInternalServerError)
		return
	}
	data.User = &user
	data.CSRFToken = s.ensureCSRF(w, r)
	s.render(w, http.StatusOK, "admin-user-import-page", data)
}

func (s *Server) adminUsersImportPreview(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAdministrator(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUserImportBytes)
	if !s.validCSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	if err := r.ParseMultipartForm(maxUserImportBytes); err != nil {
		s.renderUserImportError(w, r, user, "Upload a valid CSV file.")
		return
	}
	file, _, err := r.FormFile("csv_file")
	if err != nil {
		s.renderUserImportError(w, r, user, "Select a CSV file to import.")
		return
	}
	defer file.Close()
	inputs, err := parseUserImportCSV(file)
	if err != nil {
		s.renderUserImportError(w, r, user, "The CSV must contain username, email, and display_name columns with valid user records.")
		return
	}
	groupIDs := append([]string(nil), r.Form["group_ids"]...)
	previews, err := s.auth.PreviewUserImport(r.Context(), user.ID, inputs, groupIDs)
	if err != nil {
		s.renderUserImportError(w, r, user, "The CSV contains invalid or duplicate user records.")
		return
	}
	token, err := s.userImports.putDraft(userImportDraft{ActorID: user.ID, Inputs: inputs, GroupIDs: groupIDs, Previews: previews, CreatedAt: time.Now().UTC()})
	if err != nil {
		s.renderUserImportError(w, r, user, "The import preview could not be prepared.")
		return
	}
	data, err := s.userImportPage(r.Context(), user.ID, userImportPageData{Previews: previews, SelectedGroupIDs: groupIDs, DraftToken: token})
	if err != nil {
		http.Error(w, "failed to load groups", http.StatusInternalServerError)
		return
	}
	data.User = &user
	data.CSRFToken = s.ensureCSRF(w, r)
	s.render(w, http.StatusOK, "admin-user-import-page", data)
}

func (s *Server) adminUsersImportCommit(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAdministrator(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if !s.validCSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	draft, ok := s.userImports.getDraft(user.ID, r.FormValue("draft_token"))
	if !ok {
		s.renderUserImportError(w, r, user, "The import preview has expired. Upload the CSV again.")
		return
	}
	results, err := s.auth.ImportUsers(r.Context(), user.ID, draft.Inputs, draft.GroupIDs)
	if err != nil {
		s.renderUserImportError(w, r, user, "The import could not be committed. No changes were applied.")
		return
	}
	data, err := userImportCSV(results)
	if err != nil {
		s.renderUserImportError(w, r, user, "The import completed, but the credential export could not be prepared.")
		return
	}
	downloadToken, err := s.userImports.putDownload(user.ID, data)
	if err != nil {
		s.renderUserImportError(w, r, user, "The import completed, but the credential export could not be prepared.")
		return
	}
	s.userImports.deleteDraft(r.FormValue("draft_token"))
	created, updated := 0, 0
	for _, result := range results {
		if result.Existing {
			updated++
		} else {
			created++
		}
	}
	page, err := s.userImportPage(r.Context(), user.ID, userImportPageData{DownloadURL: "/admin/users/import/download/" + downloadToken, CreatedCount: created, UpdatedCount: updated})
	if err != nil {
		http.Error(w, "import completed", http.StatusOK)
		return
	}
	page.User = &user
	page.CSRFToken = s.ensureCSRF(w, r)
	s.render(w, http.StatusOK, "admin-user-import-page", page)
}

func (s *Server) adminUsersImportDownload(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireAdministrator(w, r)
	if !ok {
		return
	}
	data, ok := s.userImports.getDownload(user.ID, r.PathValue("token"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="cows-user-import.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) renderUserImportError(w http.ResponseWriter, r *http.Request, user domain.User, message string) {
	data, err := s.userImportPage(r.Context(), user.ID, userImportPageData{Error: message})
	if err != nil {
		http.Error(w, message, http.StatusBadRequest)
		return
	}
	data.User = &user
	data.CSRFToken = s.ensureCSRF(w, r)
	s.render(w, http.StatusBadRequest, "admin-user-import-page", data)
}

func userImportCSV(results []auth.ImportedUserResult) ([]byte, error) {
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	if err := writer.Write([]string{"username", "email", "display_name", "status", "password"}); err != nil {
		return nil, err
	}
	for _, result := range results {
		status := "created"
		if result.Existing {
			status = "existing / updated"
		}
		if err := writer.Write([]string{result.Username, result.Email, result.DisplayName, status, result.Password}); err != nil {
			return nil, err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, fmt.Errorf("write user import CSV: %w", err)
	}
	return buffer.Bytes(), nil
}
