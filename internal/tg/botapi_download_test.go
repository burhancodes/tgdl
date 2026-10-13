package tg

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-telegram/bot"
)

func TestMaskToken(t *testing.T) {
	token := "123456:ABC-DEF_ghi"
	rawPath := "/var/lib/telegram-bot-api/123456:ABC-DEF_ghi/documents/file_0.mkv"
	masked := MaskToken(rawPath, token)
	expected := "/var/lib/telegram-bot-api/***/documents/file_0.mkv"
	if masked != expected {
		t.Fatalf("expected %q, got %q", expected, masked)
	}

	rawText := "error with token 123456:ABC-DEF_ghi occurred"
	maskedText := MaskToken(rawText, token)
	if strings.Contains(maskedText, token) {
		t.Fatalf("masked text still contains token: %s", maskedText)
	}

	if MaskToken("plain", "") != "plain" {
		t.Fatalf("empty token modified text")
	}
}

func TestExtractRelativeFilePath(t *testing.T) {
	token := "987654:XYZ_123"
	tests := []struct {
		rawPath  string
		token    string
		expected string
	}{
		{
			rawPath:  "/var/lib/telegram-bot-api/987654:XYZ_123/documents/file_0.mkv",
			token:    token,
			expected: "documents/file_0.mkv",
		},
		{
			rawPath:  "/var/lib/telegram-bot-api/987654:XYZ_123/videos/clip.mp4",
			token:    token,
			expected: "videos/clip.mp4",
		},
		{
			rawPath:  "/var/lib/telegram-bot-api/documents/file_0.mkv",
			token:    "",
			expected: "documents/file_0.mkv",
		},
		{
			rawPath:  "/server/mount/videos/movie.mp4",
			token:    "",
			expected: "videos/movie.mp4",
		},
		{
			rawPath:  "/app/data/photos/pic.jpg",
			token:    "",
			expected: "photos/pic.jpg",
		},
		{
			rawPath:  "documents/file_0.mkv",
			token:    "",
			expected: "documents/file_0.mkv",
		},
		{
			rawPath:  "/var/lib/telegram-bot-api/987654:XYZ_123/thumbnails/thumb.jpg",
			token:    token,
			expected: "thumbnails/thumb.jpg",
		},
		{
			rawPath:  "/var/lib/telegram-bot-api/987654:XYZ_123/temp/part_1.tmp",
			token:    token,
			expected: "temp/part_1.tmp",
		},
		{
			rawPath:  "/custom/path/audio/track.flac",
			token:    "",
			expected: "audio/track.flac",
		},
		{
			rawPath:  "/var/lib/telegram-bot-api/voice/msg.ogg",
			token:    "",
			expected: "voice/msg.ogg",
		},
		{
			rawPath:  "/data/video_notes/note.mp4",
			token:    "",
			expected: "video_notes/note.mp4",
		},
		{
			rawPath:  "/stickers/sticker.webp",
			token:    "",
			expected: "stickers/sticker.webp",
		},
		{
			rawPath:  "/animations/gif.mp4",
			token:    "",
			expected: "animations/gif.mp4",
		},
		{
			rawPath:  "/music/track.mp3",
			token:    "",
			expected: "music/track.mp3",
		},
		{
			rawPath:  "987654:XYZ_123/documents/file_0.mkv",
			token:    token,
			expected: "documents/file_0.mkv",
		},
	}

	for _, tc := range tests {
		actual := ExtractRelativeFilePath(tc.rawPath, tc.token)
		if actual != tc.expected {
			t.Errorf("ExtractRelativeFilePath(%q, %q) = %q, want %q", tc.rawPath, tc.token, actual, tc.expected)
		}
	}
}

func TestDownload_LocalBotAPI_Cases(t *testing.T) {
	tempDir := t.TempDir()
	dataDir := filepath.Join(tempDir, "botapi_data")
	destDir := filepath.Join(tempDir, "downloads")
	_ = os.MkdirAll(dataDir, 0o755)
	_ = os.MkdirAll(destDir, 0o755)

	token := "111222:TEST_TOKEN_SECRET"
	tokenDir := filepath.Join(dataDir, token, "documents")
	_ = os.MkdirAll(tokenDir, 0o755)

	payload := []byte("TEST_PAYLOAD_DATA_CONTENT_12345")
	validFile := filepath.Join(tokenDir, "file_0.mkv")
	if err := os.WriteFile(validFile, payload, 0o644); err != nil {
		t.Fatal(err)
	}

	var httpFileRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/file/") {
			httpFileRequests.Add(1)
			if strings.Contains(r.URL.Path, "file_cloud.mkv") {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("CLOUD_DATA"))
				return
			}
			if strings.Contains(r.URL.Path, "truncated.mkv") {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("SHORT"))
				return
			}
			w.WriteHeader(http.StatusNotFound)
			return
		}

		if strings.HasSuffix(r.URL.Path, "/getMe") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":{"id":12345,"is_bot":true,"first_name":"TestBot","username":"test_bot"}}`))
			return
		}

		if strings.HasSuffix(r.URL.Path, "/getFile") {
			w.Header().Set("Content-Type", "application/json")
			fileID := r.FormValue("file_id")

			switch fileID {
			case "file_readable":
				resp := fmt.Sprintf(`{"ok":true,"result":{"file_id":"file_readable","file_size":%d,"file_path":"%s"}}`, len(payload), validFile)
				_, _ = w.Write([]byte(resp))
			case "file_mode0000":
				f0 := filepath.Join(tokenDir, "file_0000.mkv")
				_ = os.WriteFile(f0, []byte("SECRET"), 0o000)
				resp := fmt.Sprintf(`{"ok":true,"result":{"file_id":"file_mode0000","file_size":6,"file_path":"%s"}}`, f0)
				_, _ = w.Write([]byte(resp))
			case "file_missing":
				fMiss := filepath.Join(tokenDir, "non_existent.mkv")
				resp := fmt.Sprintf(`{"ok":true,"result":{"file_id":"file_missing","file_size":100,"file_path":"%s"}}`, fMiss)
				_, _ = w.Write([]byte(resp))
			case "file_remap":
				resp := fmt.Sprintf(`{"ok":true,"result":{"file_id":"file_remap","file_size":%d,"file_path":"/server/custom/mount/%s/documents/file_remap.mkv"}}`, len(payload), token)
				_, _ = w.Write([]byte(resp))
			case "file_cloud":
				resp := `{"ok":true,"result":{"file_id":"file_cloud","file_size":10,"file_path":"documents/file_cloud.mkv"}}`
				_, _ = w.Write([]byte(resp))
			case "file_truncated":
				resp := `{"ok":true,"result":{"file_id":"file_truncated","file_size":1000,"file_path":"documents/truncated.mkv"}}`
				_, _ = w.Write([]byte(resp))
			default:
				http.NotFound(w, r)
			}
		}
	}))
	defer server.Close()

	b, err := bot.New(token, bot.WithServerURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	api := NewBotAPI(b, token)
	api.BaseURL = server.URL
	api.SetDataDir(dataDir)

	t.Run("Case_A_Readable", func(t *testing.T) {
		httpFileRequests.Store(0)
		res, err := api.Download(context.Background(), FileRef{FileID: "file_readable", Name: "downloaded.mkv", Size: int64(len(payload))}, destDir, nil)
		if err != nil {
			t.Fatalf("Download failed: %v", err)
		}
		st, err := os.Stat(res)
		if err != nil || st.Size() != int64(len(payload)) {
			t.Fatalf("size mismatch or missing dest file: %v", err)
		}
		if _, err := os.Stat(validFile); !os.IsNotExist(err) {
			t.Fatalf("source file should have been removed: %v", err)
		}
		if httpFileRequests.Load() != 0 {
			t.Fatalf("expected 0 HTTP file requests for readable local file, got %d", httpFileRequests.Load())
		}
	})

	t.Run("Case_B_PermissionsAndMissing", func(t *testing.T) {
		if os.Getuid() != 0 {
			_, err := api.Download(context.Background(), FileRef{FileID: "file_mode0000", Name: "m0.mkv"}, destDir, nil)
			if err == nil {
				t.Fatalf("expected error for mode 0000 file")
			}
			if !strings.Contains(err.Error(), "permission denied") {
				t.Fatalf("error should mention permission denied: %v", err)
			}
			if strings.Contains(err.Error(), token) {
				t.Fatalf("error must not contain bot token: %v", err)
			}
		}

		_, err := api.Download(context.Background(), FileRef{FileID: "file_missing", Name: "missing.mkv"}, destDir, nil)
		if err == nil {
			t.Fatalf("expected error for non-existent file")
		}
		if !strings.Contains(err.Error(), "not found on the shared volume") {
			t.Fatalf("error should say not found on the shared volume: %v", err)
		}
		if strings.Contains(err.Error(), token) {
			t.Fatalf("error must not contain bot token: %v", err)
		}
	})

	t.Run("Case_C_RemapDataDir", func(t *testing.T) {
		remapFile := filepath.Join(tokenDir, "file_remap.mkv")
		if err := os.WriteFile(remapFile, payload, 0o644); err != nil {
			t.Fatal(err)
		}
		res, err := api.Download(context.Background(), FileRef{FileID: "file_remap", Name: "remapped.mkv", Size: int64(len(payload))}, destDir, nil)
		if err != nil {
			t.Fatalf("Download with remapped path failed: %v", err)
		}
		st, err := os.Stat(res)
		if err != nil || st.Size() != int64(len(payload)) {
			t.Fatalf("size mismatch or missing file: %v", err)
		}
	})

	t.Run("Case_D_CloudRelativePath", func(t *testing.T) {
		res, err := api.Download(context.Background(), FileRef{FileID: "file_cloud", Name: "cloud.mkv", Size: 10}, destDir, nil)
		if err != nil {
			t.Fatalf("Cloud download failed: %v", err)
		}
		data, err := os.ReadFile(res)
		if err != nil || string(data) != "CLOUD_DATA" {
			t.Fatalf("downloaded content mismatch: %v", err)
		}
	})

	t.Run("Case_E_TruncatedDownload", func(t *testing.T) {
		_, err := api.Download(context.Background(), FileRef{FileID: "file_truncated", Name: "trunc.mkv", Size: 1000}, destDir, nil)
		if err == nil {
			t.Fatalf("expected error on truncated download")
		}
		partPath := filepath.Join(destDir, "trunc.mkv.part")
		if _, err := os.Stat(partPath); !os.IsNotExist(err) {
			t.Fatalf("expected .part file to be removed on error")
		}
	})
}

func TestDownload_SameFileTwice_ClearErrorWhenNotRecreated(t *testing.T) {
	tempDir := t.TempDir()
	dataDir := filepath.Join(tempDir, "botapi_data")
	destDir := filepath.Join(tempDir, "dest")
	_ = os.MkdirAll(dataDir, 0o755)
	_ = os.MkdirAll(destDir, 0o755)

	token := "333444:ONCE_AND_TWICE"
	fileDir := filepath.Join(dataDir, token, "documents")
	_ = os.MkdirAll(fileDir, 0o755)
	filePath := filepath.Join(fileDir, "once.mkv")
	payload := []byte("ONCE_CONTENT")
	if err := os.WriteFile(filePath, payload, 0o644); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/file/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		resp := fmt.Sprintf(`{"ok":true,"result":{"file_id":"file_once","file_size":%d,"file_path":"%s"}}`, len(payload), filePath)
		_, _ = w.Write([]byte(resp))
	}))
	defer server.Close()

	b, err := bot.New(token, bot.WithServerURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	api := NewBotAPI(b, token)
	api.BaseURL = server.URL
	api.SetDataDir(dataDir)

	p1, err := api.Download(context.Background(), FileRef{FileID: "file_once", Name: "once1.mkv", Size: int64(len(payload))}, destDir, nil)
	if err != nil {
		t.Fatalf("first download failed: %v", err)
	}
	if _, err := os.Stat(p1); err != nil {
		t.Fatalf("dest file not found: %v", err)
	}
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Fatalf("source file should have been deleted: %v", err)
	}

	_, err = api.Download(context.Background(), FileRef{FileID: "file_once", Name: "once2.mkv", Size: int64(len(payload))}, destDir, nil)
	if err == nil {
		t.Fatalf("expected second download to fail when file was removed and server did not recreate")
	}
	if !strings.Contains(err.Error(), "not found on the shared volume") {
		t.Fatalf("expected clear error mentioning not found on the shared volume, got: %v", err)
	}
}
