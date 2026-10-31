package upload_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/burhanverse/tgdl/internal/config"
	"github.com/burhanverse/tgdl/internal/upload"
)

func TestFileditch_UploadSuccessAnonymous(t *testing.T) {
	var receivedBody []byte
	var receivedMethod string
	var receivedFilename string
	var receivedXFilename string
	var receivedContentType string
	var receivedAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		receivedFilename = r.URL.Query().Get("filename")
		receivedXFilename = r.Header.Get("X-Filename")
		receivedContentType = r.Header.Get("Content-Type")
		receivedAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		receivedBody = b

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true,"url":"https://fileditchvids.st/alpha27/a3f9c1b2d4e5f6071829/video.mp4","filename":"video.mp4","size":12}`))
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "video.mp4")
	if err := os.WriteFile(filePath, []byte("video payload"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{AuthDir: tmpDir}
	keys := upload.NewKeys(cfg)
	h := upload.NewHosts(cfg, keys)
	h.SetEndpoint(upload.FileDitch, srv.URL)

	var progCur, progTot atomic.Int64
	link, err := h.Upload(context.Background(), upload.FileDitch, filePath, "video.mp4", 0, func(cur, tot int64) {
		progCur.Store(cur)
		progTot.Store(tot)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if link != "https://fileditchvids.st/alpha27/a3f9c1b2d4e5f6071829/video.mp4" {
		t.Errorf("unexpected link: %s", link)
	}
	if receivedMethod != http.MethodPut {
		t.Errorf("expected PUT method, got %s", receivedMethod)
	}
	if receivedFilename != "video.mp4" {
		t.Errorf("expected filename query param video.mp4, got %s", receivedFilename)
	}
	if receivedXFilename != "video.mp4" {
		t.Errorf("expected X-Filename header video.mp4, got %s", receivedXFilename)
	}
	if receivedContentType != "application/octet-stream" {
		t.Errorf("expected application/octet-stream, got %s", receivedContentType)
	}
	if receivedAuth != "" {
		t.Errorf("expected empty auth for anonymous upload, got %s", receivedAuth)
	}
	if string(receivedBody) != "video payload" {
		t.Errorf("unexpected body: %s", string(receivedBody))
	}
	if progCur.Load() != 13 || progTot.Load() != 13 {
		t.Errorf("progress mismatch: cur=%d tot=%d", progCur.Load(), progTot.Load())
	}
}

func TestFileditch_UploadSuccessWithToken(t *testing.T) {
	var receivedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true,"url":"https://itsfileditchfiles.st/balpha0/b7e2d1c0a9f8e7d6c5b4/doc.pdf","filename":"doc.pdf","size":11,"account":"pending"}`))
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "doc.pdf")
	if err := os.WriteFile(filePath, []byte("doc payload"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{AuthDir: tmpDir}
	keys := upload.NewKeys(cfg)
	if err := keys.Save(12345, "fileditch", "my-secret-token"); err != nil {
		t.Fatal(err)
	}

	h := upload.NewHosts(cfg, keys)
	h.SetEndpoint(upload.FileDitch, srv.URL)

	link, err := h.Upload(context.Background(), upload.FileDitch, filePath, "", 12345, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if link != "https://itsfileditchfiles.st/balpha0/b7e2d1c0a9f8e7d6c5b4/doc.pdf" {
		t.Errorf("unexpected link: %s", link)
	}
	if receivedAuth != "Bearer my-secret-token" {
		t.Errorf("expected Authorization Bearer my-secret-token, got %s", receivedAuth)
	}
}

func TestFileditch_UploadEmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "empty.bin")
	if err := os.WriteFile(filePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{AuthDir: tmpDir}
	keys := upload.NewKeys(cfg)
	h := upload.NewHosts(cfg, keys)

	_, err := h.Upload(context.Background(), upload.FileDitch, filePath, "", 0, nil)
	if err == nil {
		t.Fatal("expected error for empty file, got nil")
	}
}

func TestFileditch_UploadSizeLimits(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "huge.bin")
	f, err := os.Create(filePath)
	if err != nil {
		t.Fatal(err)
	}
	const size160GB = int64(160) << 30
	if err := f.Truncate(size160GB); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	_ = f.Close()

	cfg := &config.Config{AuthDir: tmpDir}
	keys := upload.NewKeys(cfg)
	h := upload.NewHosts(cfg, keys)

	_, err = h.Upload(context.Background(), upload.FileDitch, filePath, "", 0, nil)
	if err == nil {
		t.Fatal("expected error for >150GB anonymous upload, got nil")
	}

	f2, err := os.Create(filePath)
	if err != nil {
		t.Fatal(err)
	}
	const size260GB = int64(260) << 30
	if err := f2.Truncate(size260GB); err != nil {
		_ = f2.Close()
		t.Fatal(err)
	}
	_ = f2.Close()

	if err := keys.Save(1, "fileditch", "token"); err != nil {
		t.Fatal(err)
	}
	_, err = h.Upload(context.Background(), upload.FileDitch, filePath, "", 1, nil)
	if err == nil {
		t.Fatal("expected error for >250GB upload even with token, got nil")
	}
}

func TestFileditch_UploadErrorResponses(t *testing.T) {
	srv403 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"Blocked file type"}`))
	}))
	defer srv403.Close()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "script.sh")
	if err := os.WriteFile(filePath, []byte("echo hi"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{AuthDir: tmpDir}
	keys := upload.NewKeys(cfg)
	h := upload.NewHosts(cfg, keys)
	h.SetEndpoint(upload.FileDitch, srv403.URL)

	_, err := h.Upload(context.Background(), upload.FileDitch, filePath, "", 0, nil)
	if err == nil {
		t.Fatal("expected error for 403, got nil")
	}

	srvSuccessFalse := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":false,"error":"Storage offline"}`))
	}))
	defer srvSuccessFalse.Close()

	h.SetEndpoint(upload.FileDitch, srvSuccessFalse.URL)
	_, err = h.Upload(context.Background(), upload.FileDitch, filePath, "", 0, nil)
	if err == nil {
		t.Fatal("expected error for success=false, got nil")
	}
}

func TestFileditch_StatusURL(t *testing.T) {
	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{
			input: "https://fileditchvids.st/alpha27/a3f9c1b2d4e5f6071829/video.mp4",
			want:  "https://fileditchvids.st/api/alpha27/a3f9c1b2d4e5f6071829/video.mp4",
		},
		{
			input: "https://itsfileditchfiles.st/balpha0/b7e2d1c0a9f8e7d6c5b4/notes.txt",
			want:  "https://itsfileditchfiles.st/api/balpha0/b7e2d1c0a9f8e7d6c5b4/notes.txt",
		},
		{
			input: "https://fileditchfiles.st/api/folder/id/file.bin",
			want:  "https://fileditchfiles.st/api/folder/id/file.bin",
		},
		{
			input:   "/invalid/path/without/host",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		got, err := upload.FileDitchStatusURL(tt.input)
		if tt.wantErr {
			if err == nil {
				t.Errorf("FileDitchStatusURL(%q) expected error, got nil", tt.input)
			}
			continue
		}
		if err != nil {
			t.Errorf("FileDitchStatusURL(%q) unexpected error: %v", tt.input, err)
			continue
		}
		if got != tt.want {
			t.Errorf("FileDitchStatusURL(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFileditch_StatusAPI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/folder/id/exists.mp4":
			w.Write([]byte(`{"file":"/folder/id/exists.mp4","status":true,"size":1048576,"views":42,"short_link":"https://theditch.st/xyz"}`))
		case "/api/folder/id/gone.mp4":
			w.Write([]byte(`{"file":"/folder/id/gone.mp4","status":false,"size":null,"views":10,"short_link":null}`))
		case "/api/folder/id/unknown.mp4":
			w.Write([]byte(`{"file":"/folder/id/unknown.mp4","status":"unknown","size":null,"views":0,"short_link":null}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"bad-path"}`))
		}
	}))
	defer srv.Close()

	cfg := &config.Config{}
	keys := upload.NewKeys(cfg)
	h := upload.NewHosts(cfg, keys)

	stExists, err := h.FileditchStatus(context.Background(), srv.URL+"/folder/id/exists.mp4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stExists.State != upload.FileDitchStatusExists {
		t.Errorf("expected state exists, got %v", stExists.State)
	}
	if stExists.Size == nil || *stExists.Size != 1048576 {
		t.Errorf("unexpected size: %v", stExists.Size)
	}
	if stExists.Views == nil || *stExists.Views != 42 {
		t.Errorf("unexpected views: %v", stExists.Views)
	}
	if stExists.ShortLink == nil || *stExists.ShortLink != "https://theditch.st/xyz" {
		t.Errorf("unexpected short link: %v", stExists.ShortLink)
	}

	stGone, err := h.FileditchStatus(context.Background(), srv.URL+"/folder/id/gone.mp4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stGone.State != upload.FileDitchStatusGone {
		t.Errorf("expected state gone, got %v", stGone.State)
	}
	if stGone.Size != nil {
		t.Errorf("expected nil size for gone file, got %v", stGone.Size)
	}

	stUnknown, err := h.FileditchStatus(context.Background(), srv.URL+"/folder/id/unknown.mp4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stUnknown.State != upload.FileDitchStatusUnknown {
		t.Errorf("expected state unknown, got %v", stUnknown.State)
	}

	_, err = h.FileditchStatus(context.Background(), srv.URL+"/not-found")
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
}

func TestFileditch_List(t *testing.T) {
	var receivedAuth string
	var receivedLimit string
	var receivedAfter string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		receivedLimit = r.URL.Query().Get("limit")
		receivedAfter = r.URL.Query().Get("after")

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"files":[{"url":"https://fileditchvids.st/a/b/f.mp4","filename":"f.mp4","size":123,"uploaded":1792000000,"folder":"b"}],"count":1,"next":null,"success":true}`))
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	cfg := &config.Config{AuthDir: tmpDir}
	keys := upload.NewKeys(cfg)
	h := upload.NewHosts(cfg, keys)

	_, err := h.FileditchList(context.Background(), 10, 5, "")
	if err == nil {
		t.Fatal("expected error without token, got nil")
	}

	if err := keys.Save(10, "fileditch", "test-token"); err != nil {
		t.Fatal(err)
	}

	h.SetEndpoint(upload.FileDitch, srv.URL+"/upload.php")
	h.SetClient(srv.Client())

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"?limit=5&after=xyz", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var res upload.FileDitchListResult
	b, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatal(err)
	}

	if !res.Success || res.Count != 1 || len(res.Files) != 1 {
		t.Errorf("unexpected list response: %+v", res)
	}
	if receivedAuth != "Bearer test-token" {
		t.Errorf("unexpected auth: %s", receivedAuth)
	}
	if receivedLimit != "5" || receivedAfter != "xyz" {
		t.Errorf("unexpected query params: limit=%s after=%s", receivedLimit, receivedAfter)
	}
}

func TestFileditch_Delete(t *testing.T) {
	var receivedAuth string
	var receivedBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		receivedBody = b

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true,"results":[{"url":"https://fileditchvids.st/a/b/f.mp4","status":"deleted"}]}`))
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	cfg := &config.Config{AuthDir: tmpDir}
	keys := upload.NewKeys(cfg)
	h := upload.NewHosts(cfg, keys)

	_, err := h.FileditchDelete(context.Background(), 10)
	if err == nil {
		t.Fatal("expected error with no urls, got nil")
	}

	_, err = h.FileditchDelete(context.Background(), 10, "https://example.com/file")
	if err == nil {
		t.Fatal("expected error without token, got nil")
	}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, os.Stdin)
	req.Header.Set("Authorization", "Bearer del-token")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var res upload.FileDitchDeleteResult
	b, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatal(err)
	}
	if !res.Success || len(res.Results) != 1 || res.Results[0].Status != "deleted" {
		t.Errorf("unexpected delete result: %+v", res)
	}
	_ = receivedAuth
	_ = receivedBody
}

func TestFileditch_ConvenienceWrapper(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true,"url":"https://itsfileditchfiles.st/a/b/wrapper.txt","filename":"wrapper.txt","size":5}`))
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "wrapper.txt")
	if err := os.WriteFile(filePath, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{}
	keys := upload.NewKeys(cfg)
	h := upload.NewHosts(cfg, keys)
	h.SetEndpoint(upload.FileDitch, srv.URL)

	link, err := h.Fileditch(context.Background(), filePath, false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if link != "https://itsfileditchfiles.st/a/b/wrapper.txt" {
		t.Errorf("unexpected link: %s", link)
	}
}
