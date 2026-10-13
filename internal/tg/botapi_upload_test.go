package tg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-telegram/bot"
)

func TestUpload_StreamingMultipartAndContentLength(t *testing.T) {
	tempDir := t.TempDir()
	docPath := filepath.Join(tempDir, "sample_document.txt")
	docData := bytes.Repeat([]byte("This is test document payload for streaming upload.\n"), 5000) // ~260 KB
	if err := os.WriteFile(docPath, docData, 0o600); err != nil {
		t.Fatal(err)
	}

	thumbPath := filepath.Join(tempDir, "thumb.jpg")
	thumbData := bytes.Repeat([]byte("THUMB_IMG_DATA"), 500)
	if err := os.WriteFile(thumbPath, thumbData, 0o600); err != nil {
		t.Fatal(err)
	}

	var reqCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCount.Add(1)
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/botTESTTOKEN/sendDocument" {
			t.Errorf("unexpected URL path: %s", r.URL.Path)
		}

		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Fatalf("failed to parse Content-Type: %v", err)
		}
		if mediaType != "multipart/form-data" {
			t.Fatalf("expected multipart/form-data, got %s", mediaType)
		}
		boundary := params["boundary"]

		// Read entire body and verify exact Content-Length
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read body: %v", err)
		}
		if int64(len(bodyBytes)) != r.ContentLength {
			t.Errorf("body length %d != ContentLength %d", len(bodyBytes), r.ContentLength)
		}

		// Parse multipart
		mr := multipart.NewReader(bytes.NewReader(bodyBytes), boundary)
		fields := make(map[string]string)
		files := make(map[string][]byte)
		filenames := make(map[string]string)

		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("failed to read part: %v", err)
			}
			data, err := io.ReadAll(part)
			if err != nil {
				t.Fatalf("failed to read part data: %v", err)
			}
			name := part.FormName()
			if fn := part.FileName(); fn != "" {
				files[name] = data
				filenames[name] = fn
			} else {
				fields[name] = string(data)
			}
		}

		// Verify fields
		if fields["chat_id"] != "12345" {
			t.Errorf("chat_id = %s, want 12345", fields["chat_id"])
		}
		if fields["caption"] != "<b>Test Caption</b>" {
			t.Errorf("caption = %s, want <b>Test Caption</b>", fields["caption"])
		}
		if fields["parse_mode"] != "HTML" {
			t.Errorf("parse_mode = %s, want HTML", fields["parse_mode"])
		}
		if fields["disable_notification"] != "true" {
			t.Errorf("disable_notification = %s, want true", fields["disable_notification"])
		}
		if fields["disable_content_type_detection"] != "true" {
			t.Errorf("disable_content_type_detection = %s, want true", fields["disable_content_type_detection"])
		}
		if fields["thumbnail"] != "attach://thumb" {
			t.Errorf("thumbnail = %s, want attach://thumb", fields["thumbnail"])
		}

		// Verify files
		if !bytes.Equal(files["thumb"], thumbData) {
			t.Errorf("thumbnail data mismatch, len %d vs %d", len(files["thumb"]), len(thumbData))
		}
		if filenames["thumb"] != "thumb.jpg" {
			t.Errorf("thumb filename = %s, want thumb.jpg", filenames["thumb"])
		}

		if !bytes.Equal(files["document"], docData) {
			t.Errorf("document data mismatch, len %d vs %d", len(files["document"]), len(docData))
		}
		if filenames["document"] != "sample_document.txt" {
			t.Errorf("document filename = %s, want sample_document.txt", filenames["document"])
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok": true, "result": {"message_id": 42}}`))
	}))
	defer server.Close()

	api := NewBotAPI(nil, "TESTTOKEN")
	api.BaseURL = server.URL

	var progressCalled bool
	var lastProgCurr, lastProgTotal int64
	err := api.Upload(context.Background(), 12345, Upload{
		Path:    docPath,
		Caption: "<b>Test Caption</b>",
		Kind:    KindDocument,
		Thumb:   thumbPath,
		Progress: func(current, total int64) {
			progressCalled = true
			lastProgCurr = current
			lastProgTotal = total
		},
	})
	if err != nil {
		t.Fatalf("Upload failed: %v", err)
	}

	if reqCount.Load() != 1 {
		t.Errorf("expected 1 request, got %d", reqCount.Load())
	}
	if !progressCalled {
		t.Errorf("ProgressFunc was not called")
	}
	if lastProgCurr != int64(len(docData)) || lastProgTotal != int64(len(docData)) {
		t.Errorf("ProgressFunc final: curr=%d, total=%d, want %d", lastProgCurr, lastProgTotal, len(docData))
	}
}

func TestUploadGroup_Streaming(t *testing.T) {
	tempDir := t.TempDir()
	f1Path := filepath.Join(tempDir, "video1.mp4")
	f1Data := []byte("FAKE_MP4_VIDEO_STREAM_DATA_1")
	if err := os.WriteFile(f1Path, f1Data, 0o600); err != nil {
		t.Fatal(err)
	}

	f2Path := filepath.Join(tempDir, "doc2.pdf")
	f2Data := []byte("FAKE_PDF_DOCUMENT_STREAM_DATA_2")
	if err := os.WriteFile(f2Path, f2Data, 0o600); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/botTESTTOKEN/sendMediaGroup" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Fatalf("parse content type: %v", err)
		}
		mr := multipart.NewReader(r.Body, params["boundary"])

		fields := make(map[string]string)
		files := make(map[string][]byte)
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("next part: %v", err)
			}
			data, _ := io.ReadAll(part)
			if fn := part.FileName(); fn != "" {
				files[part.FormName()] = data
			} else {
				fields[part.FormName()] = string(data)
			}
		}

		if fields["chat_id"] != "54321" {
			t.Errorf("chat_id = %s, want 54321", fields["chat_id"])
		}
		if !bytes.Equal(files["file0"], f1Data) {
			t.Errorf("file0 data mismatch")
		}
		if !bytes.Equal(files["file1"], f2Data) {
			t.Errorf("file1 data mismatch")
		}

		var mediaItems []struct {
			Type              string `json:"type"`
			Media             string `json:"media"`
			Caption           string `json:"caption"`
			SupportsStreaming bool   `json:"supports_streaming"`
		}
		if err := json.Unmarshal([]byte(fields["media"]), &mediaItems); err != nil {
			t.Fatalf("unmarshal media: %v", err)
		}
		if len(mediaItems) != 2 {
			t.Fatalf("expected 2 media items, got %d", len(mediaItems))
		}
		if mediaItems[0].Type != "video" || mediaItems[0].Media != "attach://file0" || !mediaItems[0].SupportsStreaming {
			t.Errorf("unexpected media item 0: %+v", mediaItems[0])
		}
		if mediaItems[1].Type != "document" || mediaItems[1].Media != "attach://file1" {
			t.Errorf("unexpected media item 1: %+v", mediaItems[1])
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok": true, "result": [{"message_id": 1}, {"message_id": 2}]}`))
	}))
	defer server.Close()

	api := NewBotAPI(nil, "TESTTOKEN")
	api.BaseURL = server.URL

	err := api.UploadGroup(context.Background(), 54321, []GroupItem{
		{Path: f1Path, Caption: "Video item", Video: true},
		{Path: f2Path, Caption: "Document item"},
	})
	if err != nil {
		t.Fatalf("UploadGroup failed: %v", err)
	}
}

func TestUpload_512MB_SparseFile_HeapGrowth(t *testing.T) {
	tempDir := t.TempDir()
	sparsePath := filepath.Join(tempDir, "sparse_512mb.bin")
	f, err := os.Create(sparsePath)
	if err != nil {
		t.Fatal(err)
	}
	const size512MB = 512 * 1024 * 1024
	if err := f.Truncate(size512MB); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	var bytesReceived atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			t.Errorf("io.Copy error: %v", err)
		}
		bytesReceived.Add(n)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok": true, "result": {"message_id": 999}}`))
	}))
	defer server.Close()

	api := NewBotAPI(nil, "TESTTOKEN")
	api.BaseURL = server.URL

	// Warm-up runtime memory stats
	runtime.GC()
	var memBefore runtime.MemStats
	runtime.ReadMemStats(&memBefore)

	var lastProgress atomic.Int64
	err = api.Upload(context.Background(), 777, Upload{
		Path: sparsePath,
		Kind: KindDocument,
		Progress: func(current, total int64) {
			lastProgress.Store(current)
		},
	})
	if err != nil {
		t.Fatalf("Upload 512MB failed: %v", err)
	}

	runtime.GC()
	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)

	heapInuseDelta := int64(memAfter.HeapInuse) - int64(memBefore.HeapInuse)
	totalAllocDelta := int64(memAfter.TotalAlloc) - int64(memBefore.TotalAlloc)

	t.Logf("512MB Upload: HeapInuse delta: %d bytes (%.2f MiB), TotalAlloc delta: %d bytes (%.2f MiB)",
		heapInuseDelta, float64(heapInuseDelta)/(1024*1024),
		totalAllocDelta, float64(totalAllocDelta)/(1024*1024))

	const maxAllowed = 64 * 1024 * 1024 // 64 MiB limit as required
	if heapInuseDelta > maxAllowed {
		t.Errorf("HeapInuse delta %d exceeds 64 MiB limit", heapInuseDelta)
	}
	if totalAllocDelta > maxAllowed {
		t.Errorf("TotalAlloc delta %d exceeds 64 MiB limit", totalAllocDelta)
	}

	if bytesReceived.Load() < size512MB {
		t.Errorf("server received %d bytes, want at least %d", bytesReceived.Load(), size512MB)
	}
	if lastProgress.Load() != size512MB {
		t.Errorf("progressFunc last value = %d, want %d", lastProgress.Load(), size512MB)
	}
}

func TestUpload_CtxCancel_PromptAndNoGoroutineLeak(t *testing.T) {
	tempDir := t.TempDir()
	sparsePath := filepath.Join(tempDir, "cancel_test.bin")
	f, err := os.Create(sparsePath)
	if err != nil {
		t.Fatal(err)
	}
	const fileSize = 128 * 1024 * 1024
	if err := f.Truncate(fileSize); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	ctx, cancel := context.WithCancel(context.Background())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 64*1024)
		_, _ = io.ReadFull(r.Body, buf)
		cancel() // Cancel context during transfer!
		_, _ = io.Copy(io.Discard, r.Body)
	}))
	defer server.Close()

	api := NewBotAPI(nil, "TESTTOKEN")
	api.BaseURL = server.URL

	// Warm-up client connections and measure baseline goroutines
	api.uploadClient.CloseIdleConnections()
	time.Sleep(50 * time.Millisecond)
	initialGoroutines := runtime.NumGoroutine()

	start := time.Now()
	err = api.Upload(ctx, 123, Upload{
		Path: sparsePath,
		Kind: KindDocument,
	})
	duration := time.Since(start)

	if duration > 3*time.Second {
		t.Errorf("cancellation took %v; expected prompt return (<3s)", duration)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}

	// Wait briefly for transport connections to close and check goroutine leak
	api.uploadClient.CloseIdleConnections()
	var finalGoroutines int
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runtime.GC()
		api.uploadClient.CloseIdleConnections()
		finalGoroutines = runtime.NumGoroutine()
		if finalGoroutines <= initialGoroutines {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	if finalGoroutines > initialGoroutines {
		t.Errorf("goroutine leak: initial=%d, final=%d", initialGoroutines, finalGoroutines)
	}
}

func TestUpload_LocalDirMap(t *testing.T) {
	tempDir := t.TempDir()
	localFile := filepath.Join(tempDir, "myvideo.mp4")
	if err := os.WriteFile(localFile, []byte("LOCAL_VIDEO_CONTENT"), 0o600); err != nil {
		t.Fatal(err)
	}

	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "outside.mp4")
	if err := os.WriteFile(outsideFile, []byte("STREAMED_VIDEO_CONTENT"), 0o600); err != nil {
		t.Fatal(err)
	}

	var localReqReceived, streamedReqReceived atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Fatalf("parse media type: %v", err)
		}
		mr := multipart.NewReader(r.Body, params["boundary"])

		fields := make(map[string]string)
		hasFiles := false
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("next part: %v", err)
			}
			data, _ := io.ReadAll(part)
			if part.FileName() != "" {
				hasFiles = true
			} else {
				fields[part.FormName()] = string(data)
			}
		}

		if fields["video"] == "file:///container/downloads/myvideo.mp4" {
			localReqReceived.Store(true)
			if hasFiles {
				t.Errorf("expected NO file parts when local file URL is used")
			}
		} else if hasFiles {
			streamedReqReceived.Store(true)
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok": true, "result": {"message_id": 1}}`))
	}))
	defer server.Close()

	api := NewBotAPI(nil, "TESTTOKEN")
	api.BaseURL = server.URL
	api.SetLocalDirMap(tempDir, "/container/downloads")

	// 1. Mapped file should send file:/// URL
	var progCalled bool
	err := api.Upload(context.Background(), 123, Upload{
		Path: localFile,
		Kind: KindVideo,
		Progress: func(curr, total int64) {
			progCalled = true
		},
	})
	if err != nil {
		t.Fatalf("local upload failed: %v", err)
	}
	if !localReqReceived.Load() {
		t.Errorf("server did not receive file:/// video field")
	}
	if !progCalled {
		t.Errorf("expected ProgressFunc to be invoked for local upload")
	}

	// 2. Unmapped file should fall back to streaming multipart
	err = api.Upload(context.Background(), 123, Upload{
		Path: outsideFile,
		Kind: KindVideo,
	})
	if err != nil {
		t.Fatalf("streamed upload failed: %v", err)
	}
	if !streamedReqReceived.Load() {
		t.Errorf("server did not receive streamed file part for unmapped file")
	}
}

func TestUpload_ErrorMapping(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "test.txt")
	if err := os.WriteFile(filePath, []byte("test payload"), 0o600); err != nil {
		t.Fatal(err)
	}

	// 1. FloodError (429 with retry_after)
	serverFlood := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusTooManyRequests)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok": false, "error_code": 429, "description": "Too Many Requests", "parameters": {"retry_after": 12}}`))
	}))
	defer serverFlood.Close()

	api := NewBotAPI(nil, "TESTTOKEN")
	api.BaseURL = serverFlood.URL
	err := api.Upload(context.Background(), 123, Upload{Path: filePath, Kind: KindDocument})
	secs, isFlood := IsFlood(err)
	if !isFlood || secs != 12 {
		t.Errorf("expected FloodError with 12s, got %v (isFlood=%v, secs=%d)", err, isFlood, secs)
	}

	// 2. BadRequestError (400)
	serverBadReq := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusBadRequest)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok": false, "error_code": 400, "description": "Bad Request: PHOTO_SAVE_FILE_INVALID"}`))
	}))
	defer serverBadReq.Close()

	api.BaseURL = serverBadReq.URL
	err = api.Upload(context.Background(), 123, Upload{Path: filePath, Kind: KindDocument})
	msg, isBadReq := IsBadRequest(err)
	if !isBadReq || msg != "Bad Request: PHOTO_SAVE_FILE_INVALID" {
		t.Errorf("expected BadRequestError, got %v (isBadReq=%v, msg=%s)", err, isBadReq, msg)
	}

	// 3. Ambiguous upload (connection closed after body is fully sent)
	serverAmbiguous := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body) // Read whole body to completion
		// Hijack connection and abruptly close it
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("server does not support hijacking")
		}
		conn, _, _ := hj.Hijack()
		conn.Close()
	}))
	defer serverAmbiguous.Close()

	api.BaseURL = serverAmbiguous.URL
	err = api.Upload(context.Background(), 123, Upload{Path: filePath, Kind: KindDocument})
	if !errors.Is(err, ErrAmbiguousUpload) {
		t.Errorf("expected ErrAmbiguousUpload, got: %v", err)
	}

	// 4. Server 504 Gateway Timeout after body fully sent is also ambiguous
	server504 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusGatewayTimeout)
		w.Write([]byte("<html>Gateway Timeout</html>"))
	}))
	defer server504.Close()

	api.BaseURL = server504.URL
	err = api.Upload(context.Background(), 123, Upload{Path: filePath, Kind: KindDocument})
	if !errors.Is(err, ErrAmbiguousUpload) {
		t.Errorf("expected ErrAmbiguousUpload on 504, got: %v", err)
	}

	// 5. Connection closed BEFORE body is fully sent is NOT ambiguous
	bigPath := filepath.Join(tempDir, "big_partial.bin")
	fBig, err := os.Create(bigPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = fBig.Truncate(10 * 1024 * 1024)
	fBig.Close()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			// Read only first 100 bytes and close connection abruptly
			buf := make([]byte, 100)
			_, _ = conn.Read(buf)
			conn.Close()
		}
	}()

	api.BaseURL = "http://" + listener.Addr().String()
	err = api.Upload(context.Background(), 123, Upload{Path: bigPath, Kind: KindDocument})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if errors.Is(err, ErrAmbiguousUpload) {
		t.Errorf("early failure should NOT be ErrAmbiguousUpload, got: %v", err)
	}
}

func TestUpload_FileMode_TimeoutCalculation(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "sparse_600mb.bin")
	f, err := os.Create(filePath)
	if err != nil {
		t.Fatal(err)
	}
	const size600MB = int64(600 * 1024 * 1024)
	if err := f.Truncate(size600MB); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	_ = f.Close()

	var recordedTimeout time.Duration
	var handlerCalled atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled.Store(true)
		// Fake server delays its response
		time.Sleep(20 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok": true, "result": {"message_id": 100}}`))
	}))
	defer server.Close()

	api := NewBotAPI(nil, "TESTTOKEN")
	api.BaseURL = server.URL
	api.SetLocalDirMap(tempDir, "/container/downloads")

	var recordedDeadline time.Time
	var hasDeadline bool
	baseTr := api.uploadClient.Transport
	if baseTr == nil {
		baseTr = http.DefaultTransport
	}
	api.uploadClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			recordedDeadline, hasDeadline = req.Context().Deadline()
			return baseTr.RoundTrip(req)
		}),
	}

	expectedMinTimeout := 15*time.Minute + time.Duration(size600MB/(512*1024))*time.Second
	var progCurr atomic.Int64
	err = api.Upload(context.Background(), 123, Upload{
		Path: filePath,
		Kind: KindDocument,
		Progress: func(curr, total int64) {
			progCurr.Store(curr)
		},
	})
	if err != nil {
		t.Fatalf("Upload failed: %v", err)
	}
	if !handlerCalled.Load() {
		t.Fatal("handler was not called")
	}

	if !hasDeadline {
		t.Fatal("request context did not have a deadline")
	}
	recordedTimeout = time.Until(recordedDeadline)
	if recordedTimeout < expectedMinTimeout-10*time.Second {
		t.Errorf("computed timeout %v is less than expected minimum %v", recordedTimeout, expectedMinTimeout)
	}
	if progCurr.Load() != size600MB {
		t.Errorf("expected final progress %d, got %d", size600MB, progCurr.Load())
	}
}

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestUpload_FileMode_FallbackOn400(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "fallback_doc.txt")
	testData := []byte("FALLBACK_STREAMED_PAYLOAD")
	if err := os.WriteFile(filePath, testData, 0o644); err != nil {
		t.Fatal(err)
	}

	var reqCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := reqCount.Add(1)
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Fatalf("parse media type: %v", err)
		}
		mr := multipart.NewReader(r.Body, params["boundary"])

		hasFilePart := false
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				break
			}
			if p.FileName() != "" {
				hasFilePart = true
			}
		}

		w.Header().Set("Content-Type", "application/json")
		if count == 1 {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"ok": false, "error_code": 400, "description": "wrong file identifier/HTTP URL specified"}`))
			return
		}

		if !hasFilePart {
			t.Errorf("expected file part on retry, but none found")
		}
		w.Write([]byte(`{"ok": true, "result": {"message_id": 200}}`))
	}))
	defer server.Close()

	api := NewBotAPI(nil, "TESTTOKEN")
	api.BaseURL = server.URL
	api.SetLocalDirMap(tempDir, "/container/downloads")

	err := api.Upload(context.Background(), 123, Upload{
		Path: filePath,
		Kind: KindDocument,
	})
	if err != nil {
		t.Fatalf("Upload with retry failed: %v", err)
	}
	if reqCount.Load() != 2 {
		t.Errorf("expected 2 requests (attempt + retry), got %d", reqCount.Load())
	}
}

func TestDownload_SameFileTwice_LocalBotAPI(t *testing.T) {
	tempDir := t.TempDir()
	dataDir := filepath.Join(tempDir, "botapi_data")
	destDir1 := filepath.Join(tempDir, "dest1")
	destDir2 := filepath.Join(tempDir, "dest2")
	_ = os.MkdirAll(dataDir, 0o755)

	srcFilePath := filepath.Join(dataDir, "video123.mp4")
	fileContent := []byte("SIMULATED_TG_BOT_API_SAVED_VIDEO")
	if err := os.WriteFile(srcFilePath, fileContent, 0o644); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := fmt.Sprintf(`{"ok": true, "result": {"file_id": "file_abc", "file_unique_id": "u_abc", "file_size": %d, "file_path": "%s"}}`,
			len(fileContent), srcFilePath)
		w.Write([]byte(resp))
	}))
	defer server.Close()

	b, err := bot.New("TESTTOKEN", bot.WithServerURL(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	api := NewBotAPI(b, "TESTTOKEN")
	api.BaseURL = server.URL
	api.SetDataDir(dataDir)

	fRef := FileRef{FileID: "file_abc", Name: "video123.mp4", Size: int64(len(fileContent))}

	p1, err := api.Download(context.Background(), fRef, destDir1, nil)
	if err != nil {
		t.Fatalf("first Download failed: %v", err)
	}
	d1, err := os.ReadFile(p1)
	if err != nil || !bytes.Equal(d1, fileContent) {
		t.Fatalf("downloaded file 1 invalid: %v", err)
	}

	if _, err := os.Stat(srcFilePath); !os.IsNotExist(err) {
		t.Fatalf("expected source file to be removed after download, got err: %v", err)
	}

	if err := os.WriteFile(srcFilePath, fileContent, 0o644); err != nil {
		t.Fatal(err)
	}

	p2, err := api.Download(context.Background(), fRef, destDir2, nil)
	if err != nil {
		t.Fatalf("second Download failed: %v", err)
	}
	d2, err := os.ReadFile(p2)
	if err != nil || !bytes.Equal(d2, fileContent) {
		t.Fatalf("downloaded file 2 invalid: %v", err)
	}
}

func TestUpload_ThumbnailAlwaysMultipart_LocalDirMap(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "sample.mp4")
	thumbPath := filepath.Join(tempDir, "thumb.jpg")
	if err := os.WriteFile(filePath, []byte("VIDEO_DATA"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(thumbPath, []byte("THUMB_JPEG_DATA"), 0o644); err != nil {
		t.Fatal(err)
	}

	var receivedFields = make(map[string]string)
	var receivedFiles = make(map[string][]byte)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedFields = make(map[string]string)
		receivedFiles = make(map[string][]byte)

		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Fatalf("parse media type: %v", err)
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("next part error: %v", err)
			}
			data, err := io.ReadAll(p)
			if err != nil {
				t.Fatalf("read part: %v", err)
			}
			formName := p.FormName()
			if p.FileName() != "" {
				receivedFiles[formName] = data
			} else {
				receivedFields[formName] = string(data)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok": true, "result": {"message_id": 123}}`))
	}))
	defer server.Close()

	api := NewBotAPI(nil, "TESTTOKEN")
	api.BaseURL = server.URL
	api.SetLocalDirMap(tempDir, "/container/downloads")

	// 1. KindVideo with ServerVersion >= 9.0 (default 9.0)
	err := api.Upload(context.Background(), 123, Upload{
		Path:  filePath,
		Thumb: thumbPath,
		Kind:  KindVideo,
	})
	if err != nil {
		t.Fatalf("Upload failed: %v", err)
	}

	// The video file itself should use file:// because of LocalDirMap
	if val := receivedFields["video"]; !strings.HasPrefix(val, "file://") {
		t.Errorf("expected video field to be file:// URL, got %q", val)
	}

	// Thumbnail must be attach://thumb and NOT file://
	if val := receivedFields["thumbnail"]; val != "attach://thumb" {
		t.Errorf("expected thumbnail field to be 'attach://thumb', got %q", val)
	}
	if data, ok := receivedFiles["thumb"]; !ok || string(data) != "THUMB_JPEG_DATA" {
		t.Errorf("expected multipart file part named 'thumb' with thumbnail content, got %q (found=%v)", string(data), ok)
	}

	// Cover must be attach://cover and present on video upload when version >= 9.0
	if val := receivedFields["cover"]; val != "attach://cover" {
		t.Errorf("expected cover field to be 'attach://cover', got %q", val)
	}
	if data, ok := receivedFiles["cover"]; !ok || string(data) != "THUMB_JPEG_DATA" {
		t.Errorf("expected multipart file part named 'cover' with cover content, got %q (found=%v)", string(data), ok)
	}

	// 2. KindVideo with ServerVersion < 9.0
	api.SetServerVersion(8.3)
	err = api.Upload(context.Background(), 123, Upload{
		Path:  filePath,
		Thumb: thumbPath,
		Kind:  KindVideo,
	})
	if err != nil {
		t.Fatalf("Upload with v8.3 failed: %v", err)
	}

	if _, ok := receivedFields["cover"]; ok {
		t.Errorf("expected cover field to be omitted on Bot API < 9.0, but was present: %v", receivedFields["cover"])
	}
	if _, ok := receivedFiles["cover"]; ok {
		t.Errorf("expected cover file part to be omitted on Bot API < 9.0, but was present")
	}
	if val := receivedFields["thumbnail"]; val != "attach://thumb" {
		t.Errorf("expected thumbnail field to still be 'attach://thumb', got %q", val)
	}
	if data, ok := receivedFiles["thumb"]; !ok || string(data) != "THUMB_JPEG_DATA" {
		t.Errorf("expected multipart 'thumb' on v8.3, got %q (found=%v)", string(data), ok)
	}
}
