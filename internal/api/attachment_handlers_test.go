package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wesm/msgvault/internal/config"
	"github.com/wesm/msgvault/internal/store"
)

// attachmentMockStore extends mockStore with controllable attachment responses.
type attachmentMockStore struct {
	mockStore
	attachmentFile struct {
		filename    string
		mimeType    string
		storagePath string
		found       bool
		err         error
	}
	listAttachmentsResult []store.AttachmentListItem
	listAttachmentsTotal  int64
	listAttachmentsErr    error
}

func (m *attachmentMockStore) GetAttachmentFile(id int64) (filename, mimeType, storagePath string, found bool, err error) {
	f := m.attachmentFile
	return f.filename, f.mimeType, f.storagePath, f.found, f.err
}

func (m *attachmentMockStore) ListAttachments(mimePatterns []string, page, pageSize int, sortField, sortDir string) ([]store.AttachmentListItem, int64, error) {
	return m.listAttachmentsResult, m.listAttachmentsTotal, m.listAttachmentsErr
}

func newTestServerWithAttachmentStore(t *testing.T, ms *attachmentMockStore, attachDir string) *Server {
	t.Helper()
	cfg := &config.Config{
		Server: config.ServerConfig{APIPort: 8080},
		Data:   config.DataConfig{DataDir: filepath.Dir(attachDir)},
	}
	sched := newMockScheduler()
	return NewServer(cfg, ms, sched, testLogger())
}

// TestHandleGetAttachment_NotFound verifies 404 when attachment ID doesn't exist.
func TestHandleGetAttachment_NotFound(t *testing.T) {
	ms := &attachmentMockStore{}
	ms.attachmentFile.found = false

	dir := t.TempDir()
	srv := newTestServerWithAttachmentStore(t, ms, dir)

	req := httptest.NewRequest("GET", "/api/v1/attachments/42", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// TestHandleGetAttachment_InvalidID verifies 400 for non-numeric IDs.
func TestHandleGetAttachment_InvalidID(t *testing.T) {
	ms := &attachmentMockStore{}
	dir := t.TempDir()
	srv := newTestServerWithAttachmentStore(t, ms, dir)

	req := httptest.NewRequest("GET", "/api/v1/attachments/notanumber", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// TestHandleGetAttachment_FileOnDisk verifies a real file is served inline for images.
func TestHandleGetAttachment_FileOnDisk(t *testing.T) {
	dir := t.TempDir()
	content := []byte("fake png data")
	if err := os.WriteFile(filepath.Join(dir, "abc123.png"), content, 0600); err != nil {
		t.Fatal(err)
	}

	ms := &attachmentMockStore{}
	ms.attachmentFile = struct {
		filename    string
		mimeType    string
		storagePath string
		found       bool
		err         error
	}{
		filename:    "photo.png",
		mimeType:    "image/png",
		storagePath: "abc123.png",
		found:       true,
	}

	// Point cfg DataDir so AttachmentsDir() returns dir.
	cfg := &config.Config{
		Server: config.ServerConfig{APIPort: 8080},
		Data:   config.DataConfig{DataDir: filepath.Dir(dir)},
	}
	// Patch: use a server whose AttachmentsDir resolves to dir by setting DataDir = parent(dir).
	// Because AttachmentsDir() = DataDir+"/attachments", we create the file at that path instead.
	attachDir := filepath.Join(cfg.Data.DataDir, "attachments")
	if err := os.MkdirAll(attachDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(attachDir, "abc123.png"), content, 0600); err != nil {
		t.Fatal(err)
	}

	sched := newMockScheduler()
	srv := NewServer(cfg, ms, sched, testLogger())

	req := httptest.NewRequest("GET", "/api/v1/attachments/1", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	disp := w.Header().Get("Content-Disposition")
	if disp == "" || disp[:6] != "inline" {
		t.Errorf("Content-Disposition = %q, want inline; ...", disp)
	}
}

// TestHandleGetAttachment_PathTraversal verifies path traversal is rejected.
func TestHandleGetAttachment_PathTraversal(t *testing.T) {
	ms := &attachmentMockStore{}
	ms.attachmentFile = struct {
		filename    string
		mimeType    string
		storagePath string
		found       bool
		err         error
	}{
		filename:    "evil.txt",
		mimeType:    "text/plain",
		storagePath: "../../etc/passwd",
		found:       true,
	}

	cfg := &config.Config{
		Server: config.ServerConfig{APIPort: 8080},
		Data:   config.DataConfig{DataDir: t.TempDir()},
	}
	sched := newMockScheduler()
	srv := NewServer(cfg, ms, sched, testLogger())

	req := httptest.NewRequest("GET", "/api/v1/attachments/1", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

// TestIsPreviewable verifies MIME type classification.
func TestIsPreviewable(t *testing.T) {
	cases := []struct {
		mime     string
		expected bool
	}{
		{"image/png", true},
		{"image/jpeg", true},
		{"application/pdf", true},
		{"text/plain", true},
		{"text/html", true},
		{"video/mp4", true},
		{"audio/mpeg", true},
		{"application/zip", false},
		{"application/octet-stream", false},
		{"application/vnd.ms-excel", false},
	}
	for _, tc := range cases {
		if got := isPreviewable(tc.mime); got != tc.expected {
			t.Errorf("isPreviewable(%q) = %v, want %v", tc.mime, got, tc.expected)
		}
	}
}

// TestHandleListAttachments_OK verifies a basic successful response.
func TestHandleListAttachments_OK(t *testing.T) {
	ms := &attachmentMockStore{}
	ms.listAttachmentsTotal = 2
	ms.listAttachmentsResult = []store.AttachmentListItem{
		{
			ID: 1, Filename: "report.pdf", MimeType: "application/pdf", Size: 12345,
			MessageID: 10, FromEmail: "alice@example.com", FromName: "Alice",
			SentAt: time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC),
		},
		{
			ID: 2, Filename: "photo.jpg", MimeType: "image/jpeg", Size: 67890,
			MessageID: 11, FromEmail: "bob@example.com", FromName: "",
			SentAt: time.Date(2024, 3, 2, 9, 0, 0, 0, time.UTC),
		},
	}

	cfg := &config.Config{Server: config.ServerConfig{APIPort: 8080}}
	sched := newMockScheduler()
	srv := NewServer(cfg, ms, sched, testLogger())

	req := httptest.NewRequest("GET", "/api/v1/engine/attachments?page=1&page_size=50", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["total"].(float64) != 2 {
		t.Errorf("total = %v, want 2", resp["total"])
	}
	items := resp["items"].([]interface{})
	if len(items) != 2 {
		t.Errorf("len(items) = %d, want 2", len(items))
	}
	first := items[0].(map[string]interface{})
	if first["filename"] != "report.pdf" {
		t.Errorf("items[0].filename = %v, want report.pdf", first["filename"])
	}
	// from_name present -> "Name <email>" format
	if first["from"] != "Alice <alice@example.com>" {
		t.Errorf("items[0].from = %v, want 'Alice <alice@example.com>'", first["from"])
	}
}

// TestHandleListAttachments_InvalidFileType verifies 400 for unknown file_type.
func TestHandleListAttachments_InvalidFileType(t *testing.T) {
	ms := &attachmentMockStore{}
	cfg := &config.Config{Server: config.ServerConfig{APIPort: 8080}}
	sched := newMockScheduler()
	srv := NewServer(cfg, ms, sched, testLogger())

	req := httptest.NewRequest("GET", "/api/v1/engine/attachments?file_type=unknown_type", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// TestHandleListAttachments_NoStore verifies 503 when store is nil.
func TestHandleListAttachments_NoStore(t *testing.T) {
	cfg := &config.Config{Server: config.ServerConfig{APIPort: 8080}}
	sched := newMockScheduler()
	srv := NewServer(cfg, nil, sched, testLogger())

	req := httptest.NewRequest("GET", "/api/v1/engine/attachments", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}
