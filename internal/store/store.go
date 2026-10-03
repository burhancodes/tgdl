// Package store persists job state in SQLite.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, no cgo
)

// Status is the lifecycle state of a job.
type Status string

const (
	StatusQueued      Status = "queued"
	StatusDownloading Status = "downloading"
	StatusUploading   Status = "uploading"
	StatusDone        Status = "done"
	StatusFailed      Status = "failed"
	StatusCancelled   Status = "cancelled"
)

// ErrNotFound is returned when a job does not exist.
var ErrNotFound = errors.New("job not found")

// Job is a persisted download job.
type Job struct {
	ID             string
	ChatID         int64
	StatusMsgID    int
	URL            string
	Status         Status
	TotalFiles     int
	SentFiles      int
	SkippedFiles   int
	Error          string
	SplitLargeFile bool
	Args           string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// DownloadDir is the job's directory name beneath the downloads root.
func (j *Job) DownloadDir() string { return "job_" + j.ID }

// Update carries optional field updates; nil pointers are left untouched.
type Update struct {
	Status       *Status
	TotalFiles   *int
	SentFiles    *int
	SkippedFiles *int
	Error        *string
	URL          *string
}

const schema = `
CREATE TABLE IF NOT EXISTS jobs (
    id TEXT PRIMARY KEY,
    chat_id INTEGER NOT NULL,
    status_message_id INTEGER,
    url TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'queued',
    total_files INTEGER NOT NULL DEFAULT 0,
    sent_files INTEGER NOT NULL DEFAULT 0,
    skipped_files INTEGER NOT NULL DEFAULT 0,
    error TEXT,
    split_large_files INTEGER NOT NULL DEFAULT 1,
    args TEXT,
    created_at REAL NOT NULL,
    updated_at REAL NOT NULL
);
CREATE TABLE IF NOT EXISTS uploaded_files (
    job_id TEXT NOT NULL,
    filename TEXT NOT NULL,
    PRIMARY KEY (job_id, filename)
);
CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status);
`

// Store is a SQLite-backed job repository. It is safe for concurrent use.
type Store struct{ db *sql.DB }

// Open opens (creating if needed) the database at path and applies migrations.
func Open(ctx context.Context, path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // serialise writers; avoids SQLITE_BUSY
	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	// Legacy databases used an INTEGER primary key; convert to TEXT ids.
	if typ, ok := s.idColumnType(ctx); ok && strings.Contains(strings.ToUpper(typ), "INT") {
		slog.Info("migrating jobs table: INTEGER id -> TEXT")
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		stmts := []string{
			`ALTER TABLE jobs RENAME TO jobs_old`,
			`ALTER TABLE uploaded_files RENAME TO uploaded_files_old`,
		}
		hasUploaded := true
		for i, q := range stmts {
			if _, err := tx.ExecContext(ctx, q); err != nil {
				if i == 1 {
					hasUploaded = false
					continue
				}
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, schema); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO jobs (id, chat_id, status_message_id, url, status,
			total_files, sent_files, skipped_files, error, split_large_files, args, created_at, updated_at)
			SELECT CAST(id AS TEXT), chat_id, status_message_id, url, status, total_files, sent_files,
			skipped_files, error, split_large_files, args, created_at, updated_at FROM jobs_old`); err != nil {
			return err
		}
		if hasUploaded {
			if _, err := tx.ExecContext(ctx, `INSERT INTO uploaded_files (job_id, filename)
				SELECT CAST(job_id AS TEXT), filename FROM uploaded_files_old`); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DROP TABLE uploaded_files_old`); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `DROP TABLE jobs_old`); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return err
	}
	// Additive column migrations; "duplicate column" errors are expected.
	for _, q := range []string{
		`ALTER TABLE jobs ADD COLUMN split_large_files INTEGER NOT NULL DEFAULT 1`,
		`ALTER TABLE jobs ADD COLUMN args TEXT`,
	} {
		if _, err := s.db.ExecContext(ctx, q); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return err
		}
	}
	return nil
}

func (s *Store) idColumnType(ctx context.Context) (string, bool) {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(jobs)`)
	if err != nil {
		return "", false
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return "", false
		}
		if name == "id" {
			return typ, true
		}
	}
	return "", false
}

func now() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func fromEpoch(f float64) time.Time {
	return time.Unix(int64(f), int64((f-float64(int64(f)))*1e9))
}

// CreateJob inserts a queued job with a unique random id.
func (s *Store) CreateJob(ctx context.Context, chatID int64, url string, split bool, args string) (*Job, error) {
	for attempt := 0; attempt < 16; attempt++ {
		var b [4]byte
		if _, err := rand.Read(b[:]); err != nil {
			return nil, err
		}
		id := hex.EncodeToString(b[:])
		t := now()
		var argVal any
		if args != "" {
			argVal = args
		}
		splitInt := 0
		if split {
			splitInt = 1
		}
		_, err := s.db.ExecContext(ctx,
			`INSERT INTO jobs (id, chat_id, url, status, split_large_files, args, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, chatID, url, StatusQueued, splitInt, argVal, t, t)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				continue
			}
			return nil, err
		}
		return s.GetJob(ctx, id)
	}
	return nil, errors.New("could not allocate unique job id")
}

const jobCols = `id, chat_id, COALESCE(status_message_id,0), url, status, total_files, sent_files,
	skipped_files, COALESCE(error,''), split_large_files, COALESCE(args,''), created_at, updated_at`

type scanner interface{ Scan(dest ...any) error }

func scanJob(r scanner) (*Job, error) {
	var j Job
	var split int
	var created, updated float64
	var status string
	if err := r.Scan(&j.ID, &j.ChatID, &j.StatusMsgID, &j.URL, &status, &j.TotalFiles, &j.SentFiles,
		&j.SkippedFiles, &j.Error, &split, &j.Args, &created, &updated); err != nil {
		return nil, err
	}
	j.Status = Status(status)
	j.SplitLargeFile = split != 0
	j.CreatedAt, j.UpdatedAt = fromEpoch(created), fromEpoch(updated)
	return &j, nil
}

// GetJob returns ErrNotFound if the job does not exist.
func (s *Store) GetJob(ctx context.Context, id string) (*Job, error) {
	j, err := scanJob(s.db.QueryRowContext(ctx, `SELECT `+jobCols+` FROM jobs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return j, err
}

func (s *Store) SetStatusMessage(ctx context.Context, id string, msgID int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET status_message_id = ?, updated_at = ? WHERE id = ?`, msgID, now(), id)
	return err
}

// UpdateProgress applies the non-nil fields of u.
func (s *Store) UpdateProgress(ctx context.Context, id string, u Update) error {
	var sets []string
	var vals []any
	add := func(col string, v any) {
		sets = append(sets, col+" = ?")
		vals = append(vals, v)
	}
	if u.Status != nil {
		add("status", string(*u.Status))
	}
	if u.TotalFiles != nil {
		add("total_files", *u.TotalFiles)
	}
	if u.SentFiles != nil {
		add("sent_files", *u.SentFiles)
	}
	if u.SkippedFiles != nil {
		add("skipped_files", *u.SkippedFiles)
	}
	if u.Error != nil {
		add("error", *u.Error)
	}
	if u.URL != nil {
		add("url", *u.URL)
	}
	if len(sets) == 0 {
		return nil
	}
	add("updated_at", now())
	vals = append(vals, id)
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET `+strings.Join(sets, ", ")+` WHERE id = ?`, vals...)
	return err
}

// SetStatus is a convenience wrapper for status-only updates.
func (s *Store) SetStatus(ctx context.Context, id string, st Status) error {
	return s.UpdateProgress(ctx, id, Update{Status: &st})
}

func (s *Store) MarkUploaded(ctx context.Context, jobID, filename string) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO uploaded_files (job_id, filename) VALUES (?, ?)`, jobID, filename)
	return err
}

func (s *Store) UploadedFilenames(ctx context.Context, jobID string) (map[string]struct{}, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT filename FROM uploaded_files WHERE job_id = ?`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			return nil, err
		}
		out[f] = struct{}{}
	}
	return out, rows.Err()
}

// QueuedJobs returns queued jobs oldest-first (used to resume after restart).
func (s *Store) QueuedJobs(ctx context.Context) ([]*Job, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+jobCols+` FROM jobs WHERE status = ? ORDER BY created_at`, StatusQueued)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// ResumableJobs returns jobs that were queued or interrupted mid-flight,
// oldest first, so they can be resumed after a restart.
func (s *Store) ResumableJobs(ctx context.Context) ([]*Job, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+jobCols+` FROM jobs WHERE status IN (?, ?, ?) ORDER BY created_at`,
		StatusQueued, StatusDownloading, StatusUploading)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// ActiveJobsForChat lists unfinished jobs in a chat (newest last).
func (s *Store) ActiveJobsForChat(ctx context.Context, chatID int64) ([]*Job, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+jobCols+` FROM jobs WHERE chat_id = ? AND status IN (?, ?, ?) ORDER BY created_at`,
		chatID, StatusQueued, StatusDownloading, StatusUploading)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// SetArgs replaces the job's args JSON.
func (s *Store) SetArgs(ctx context.Context, id, args string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET args = ?, updated_at = ? WHERE id = ?`, args, now(), id)
	return err
}

// SetSplit updates the split-large-files flag.
func (s *Store) SetSplit(ctx context.Context, id string, split bool) error {
	v := 0
	if split {
		v = 1
	}
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET split_large_files = ?, updated_at = ? WHERE id = ?`, v, now(), id)
	return err
}

// IsTerminal reports whether the status is final.
func (s Status) IsTerminal() bool {
	return s == StatusDone || s == StatusFailed || s == StatusCancelled
}
