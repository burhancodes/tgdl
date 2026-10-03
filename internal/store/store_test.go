package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer st.Close()

	// Create job
	job, err := st.CreateJob(ctx, 12345, "https://example.com/file.zip", true, "custom-args")
	if err != nil {
		t.Fatalf("CreateJob failed: %v", err)
	}
	if job.ID == "" || job.ChatID != 12345 || job.Status != StatusQueued || !job.SplitLargeFile {
		t.Fatalf("unexpected job values: %+v", job)
	}

	// Get job
	fetched, err := st.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob failed: %v", err)
	}
	if fetched.ID != job.ID || fetched.Args != "custom-args" {
		t.Errorf("GetJob mismatch: got %+v, want %+v", fetched, job)
	}

	// Set status message
	if err := st.SetStatusMessage(ctx, job.ID, 42); err != nil {
		t.Fatalf("SetStatusMessage failed: %v", err)
	}

	// Update progress
	newStatus := StatusDownloading
	total := 10
	sent := 5
	skipped := 1
	if err := st.UpdateProgress(ctx, job.ID, Update{
		Status:       &newStatus,
		TotalFiles:   &total,
		SentFiles:    &sent,
		SkippedFiles: &skipped,
	}); err != nil {
		t.Fatalf("UpdateProgress failed: %v", err)
	}

	fetched, _ = st.GetJob(ctx, job.ID)
	if fetched.Status != StatusDownloading || fetched.TotalFiles != 10 || fetched.SentFiles != 5 || fetched.SkippedFiles != 1 || fetched.StatusMsgID != 42 {
		t.Errorf("progress update mismatch: %+v", fetched)
	}

	// Uploaded files tracking
	files, err := st.UploadedFilenames(ctx, job.ID)
	if err != nil || len(files) != 0 {
		t.Errorf("expected 0 uploaded files initially, got %v, err %v", files, err)
	}
	if err := st.MarkUploaded(ctx, job.ID, "part1.mp4"); err != nil {
		t.Fatalf("MarkUploaded failed: %v", err)
	}
	files, err = st.UploadedFilenames(ctx, job.ID)
	if err != nil {
		t.Fatalf("UploadedFilenames failed: %v", err)
	}
	if _, ok := files["part1.mp4"]; !ok {
		t.Errorf("expected part1.mp4 in uploaded files")
	}

	// Resumable jobs
	resumable, err := st.ResumableJobs(ctx)
	if err != nil {
		t.Fatalf("ResumableJobs failed: %v", err)
	}
	if len(resumable) != 1 || resumable[0].ID != job.ID {
		t.Errorf("expected 1 resumable job, got %d", len(resumable))
	}

	// Active jobs for chat
	chatJobs, err := st.ActiveJobsForChat(ctx, 12345)
	if err != nil {
		t.Fatalf("ActiveJobsForChat failed: %v", err)
	}
	if len(chatJobs) != 1 {
		t.Errorf("expected 1 active chat job, got %d", len(chatJobs))
	}

	// Set terminal status
	if err := st.SetStatus(ctx, job.ID, StatusDone); err != nil {
		t.Fatalf("SetStatus failed: %v", err)
	}
	if !StatusDone.IsTerminal() {
		t.Errorf("expected StatusDone to be terminal")
	}
	chatJobs, err = st.ActiveJobsForChat(ctx, 12345)
	if err != nil {
		t.Fatalf("ActiveJobsForChat failed: %v", err)
	}
	if len(chatJobs) != 0 {
		t.Errorf("expected 0 active chat jobs after completion, got %d", len(chatJobs))
	}
}
