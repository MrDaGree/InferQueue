package models

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ── BatchStatus ───────────────────────────────────────────────────────────────

type BatchStatus string

const (
	StatusValidating BatchStatus = "validating"
	StatusFailed     BatchStatus = "failed"
	StatusInProgress BatchStatus = "in_progress"
	StatusFinalizing BatchStatus = "finalizing"
	StatusCompleted  BatchStatus = "completed"
	StatusExpired    BatchStatus = "expired"
	StatusCancelling BatchStatus = "cancelling"
	StatusCancelled  BatchStatus = "cancelled"
)

func (s BatchStatus) IsTerminal() bool {
	switch s {
	case StatusCompleted, StatusFailed, StatusCancelled, StatusExpired:
		return true
	}
	return false
}

// ── Completion windows ────────────────────────────────────────────────────────
//
// completion_window accepts any Go duration string (e.g. "24h", "36h", "48h",
// "72h", "4h30m"). Values below the minimum of 24h are clamped up. The parsed
// duration drives both the Slurm deadline and the partition selection:
//
//   d <= 36h  → partition chosen by request count (batch-rt / batch-normal / overnight)
//   36h < d <= 72h  → batch-normal  (relaxed, Slurm backfills opportunistically)
//   d > 72h         → batch-deferred (lowest priority, fully opportunistic)
//
// The original string is echoed back verbatim in API responses; expires_at is
// set to created_at + parsed duration.

const MinCompletionWindow = 24 * time.Hour

// ParseCompletionWindow parses a duration string and clamps it to the minimum.
// It returns the effective duration and the string that will be stored/echoed.
func ParseCompletionWindow(s string) (time.Duration, string, error) {
	if s == "" {
		return MinCompletionWindow, "24h", nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, "", fmt.Errorf("completion_window: %q is not a valid duration (e.g. \"24h\", \"36h\", \"72h\"): %w", s, err)
	}
	if d < MinCompletionWindow {
		return MinCompletionWindow, "24h", nil
	}
	return d, s, nil
}

// DeadlineFor returns the absolute deadline for a duration string from a given start time.
// Falls back to MinCompletionWindow if parsing fails (should not happen after validation).
func DeadlineFor(windowStr string, from time.Time) time.Time {
	d, _, _ := ParseCompletionWindow(windowStr)
	return from.Add(d)
}

// ── Spec constants ────────────────────────────────────────────────────────────

const (
	MaxFileSizeBytes    = 200 * 1024 * 1024
	MaxRequestsPerBatch = 50_000
	FileExpiryDays      = 30
)

// ── File ──────────────────────────────────────────────────────────────────────

type FilePurpose string

const (
	PurposeBatch       FilePurpose = "batch"
	PurposeBatchOutput FilePurpose = "batch_output"
)

type FileStatus string

const (
	FileStatusUploaded  FileStatus = "uploaded"
	FileStatusProcessed FileStatus = "processed"
	FileStatusError     FileStatus = "error"
)

type File struct {
	ID        string      `gorm:"primaryKey;type:text"  json:"id"`
	CreatedAt time.Time   `                             json:"created_at"`
	UpdatedAt time.Time   `                             json:"-"`
	ExpiresAt *time.Time  `                             json:"expires_at"`
	Bytes     int64       `gorm:"not null"              json:"bytes"`
	Filename  string      `gorm:"not null;type:text"    json:"filename"`
	Purpose   FilePurpose `gorm:"not null;type:text"    json:"purpose"`
	Status    FileStatus  `gorm:"not null;type:text"    json:"status"`
	CreatedBy string      `gorm:"not null;type:text"    json:"-"`
}

func (File) TableName() string { return "files" }

func NewFile(filename string, size int64, purpose FilePurpose, createdBy string) *File {
	now := time.Now()
	expires := now.AddDate(0, 0, FileExpiryDays)
	return &File{
		ID:        "file-" + uuid.New().String(),
		Bytes:     size,
		Filename:  filename,
		Purpose:   purpose,
		Status:    FileStatusProcessed,
		CreatedBy: createdBy,
		ExpiresAt: &expires,
	}
}

type FileResponse struct {
	ID        string      `json:"id"`
	Object    string      `json:"object"`
	Bytes     int64       `json:"bytes"`
	CreatedAt int64       `json:"created_at"`
	ExpiresAt *int64      `json:"expires_at"`
	Filename  string      `json:"filename"`
	Purpose   FilePurpose `json:"purpose"`
	Status    FileStatus  `json:"status"`
}

func (f *File) ToResponse() FileResponse {
	var expiresAt *int64
	if f.ExpiresAt != nil {
		u := f.ExpiresAt.Unix()
		expiresAt = &u
	}
	return FileResponse{
		ID: f.ID, Object: "file", Bytes: f.Bytes,
		CreatedAt: f.CreatedAt.Unix(), ExpiresAt: expiresAt,
		Filename: f.Filename, Purpose: f.Purpose, Status: f.Status,
	}
}

// ── Batch ─────────────────────────────────────────────────────────────────────

type Batch struct {
	ID               string      `gorm:"primaryKey;type:text"  json:"id"`
	CreatedAt        time.Time   `                             json:"created_at"`
	UpdatedAt        time.Time   `                             json:"-"`
	Endpoint         string      `gorm:"not null;type:text"    json:"endpoint"`
	Model            string      `gorm:"type:text"             json:"model"`
	InputFileID      string      `gorm:"not null;type:text"    json:"input_file_id"`
	// CompletionWindow stores the original string as provided by the client
	// (clamped to "24h" minimum). Echoed back verbatim in API responses.
	CompletionWindow string      `gorm:"not null;type:text"    json:"completion_window"`
	Status           BatchStatus `gorm:"not null;type:text"    json:"status"`
	OutputFileID     *string     `gorm:"type:text"             json:"output_file_id"`
	ErrorFileID      *string     `gorm:"type:text"             json:"error_file_id"`
	SlurmJobID       *string     `gorm:"type:text"             json:"-"`
	CreatedBy        string      `gorm:"not null;type:text"    json:"-"`
	Metadata         []byte      `gorm:"type:jsonb"            json:"-"`

	InProgressAt *time.Time `json:"in_progress_at"`
	FinalizingAt *time.Time `json:"finalizing_at"`
	CompletedAt  *time.Time `json:"completed_at"`
	FailedAt     *time.Time `json:"failed_at"`
	ExpiredAt    *time.Time `json:"expired_at"`
	// ExpiresAt is derived from created_at + completion_window duration.
	ExpiresAt    *time.Time `json:"expires_at"`
	CancellingAt *time.Time `json:"cancelling_at"`
	CancelledAt  *time.Time `json:"cancelled_at"`

	RequestTotal     int `gorm:"default:0" json:"-"`
	RequestCompleted int `gorm:"default:0" json:"-"`
	RequestFailed    int `gorm:"default:0" json:"-"`

	UsageInputTokens  int `gorm:"default:0" json:"-"`
	UsageOutputTokens int `gorm:"default:0" json:"-"`

	Errors []byte `gorm:"type:jsonb" json:"-"`
}

func (Batch) TableName() string { return "batches" }

// NewBatch creates a Batch with expires_at derived from the (already-validated) window string.
func NewBatch(inputFileID, endpoint, model, windowStr string, metadata []byte, createdBy string) *Batch {
	now := time.Now()
	expires := DeadlineFor(windowStr, now)
	return &Batch{
		ID:               "batch_" + uuid.New().String(),
		Endpoint:         endpoint,
		Model:            model,
		InputFileID:      inputFileID,
		CompletionWindow: windowStr,
		Status:           StatusValidating,
		CreatedBy:        createdBy,
		Metadata:         metadata,
		ExpiresAt:        &expires,
	}
}

// ── BatchResponse ─────────────────────────────────────────────────────────────

type BatchResponse struct {
	ID               string            `json:"id"`
	Object           string            `json:"object"`
	Endpoint         string            `json:"endpoint"`
	Model            string            `json:"model"`
	Errors           interface{}       `json:"errors"`
	InputFileID      string            `json:"input_file_id"`
	CompletionWindow string            `json:"completion_window"`
	Status           BatchStatus       `json:"status"`
	OutputFileID     *string           `json:"output_file_id"`
	ErrorFileID      *string           `json:"error_file_id"`
	CreatedAt        int64             `json:"created_at"`
	InProgressAt     *int64            `json:"in_progress_at"`
	ExpiresAt        *int64            `json:"expires_at"`
	FinalizingAt     *int64            `json:"finalizing_at"`
	CompletedAt      *int64            `json:"completed_at"`
	FailedAt         *int64            `json:"failed_at"`
	ExpiredAt        *int64            `json:"expired_at"`
	CancellingAt     *int64            `json:"cancelling_at"`
	CancelledAt      *int64            `json:"cancelled_at"`
	RequestCounts    RequestCounts     `json:"request_counts"`
	Usage            *BatchUsage       `json:"usage"`
	Metadata         map[string]string `json:"metadata"`
}

type RequestCounts struct {
	Total     int `json:"total"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
}

type BatchUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

func toUnixPtr(t *time.Time) *int64 {
	if t == nil {
		return nil
	}
	u := t.Unix()
	return &u
}

func (b *Batch) ToResponse() BatchResponse {
	var errors interface{} = nil
	if len(b.Errors) > 0 && string(b.Errors) != "null" {
		errors = map[string]interface{}{"object": "list", "data": b.Errors}
	}
	var usage *BatchUsage
	if b.UsageInputTokens > 0 || b.UsageOutputTokens > 0 {
		usage = &BatchUsage{
			InputTokens:  b.UsageInputTokens,
			OutputTokens: b.UsageOutputTokens,
			TotalTokens:  b.UsageInputTokens + b.UsageOutputTokens,
		}
	}
	return BatchResponse{
		ID: b.ID, Object: "batch", Endpoint: b.Endpoint, Model: b.Model,
		Errors: errors, InputFileID: b.InputFileID,
		CompletionWindow: b.CompletionWindow, Status: b.Status,
		OutputFileID: b.OutputFileID, ErrorFileID: b.ErrorFileID,
		CreatedAt:    b.CreatedAt.Unix(),
		InProgressAt: toUnixPtr(b.InProgressAt),
		ExpiresAt:    toUnixPtr(b.ExpiresAt),
		FinalizingAt: toUnixPtr(b.FinalizingAt),
		CompletedAt:  toUnixPtr(b.CompletedAt),
		FailedAt:     toUnixPtr(b.FailedAt),
		ExpiredAt:    toUnixPtr(b.ExpiredAt),
		CancellingAt: toUnixPtr(b.CancellingAt),
		CancelledAt:  toUnixPtr(b.CancelledAt),
		RequestCounts: RequestCounts{
			Total:     b.RequestTotal,
			Completed: b.RequestCompleted,
			Failed:    b.RequestFailed,
		},
		Usage: usage,
	}
}

// ── JSONL line types ──────────────────────────────────────────────────────────

type BatchRequestLine struct {
	CustomID string                 `json:"custom_id"`
	Method   string                 `json:"method"`
	URL      string                 `json:"url"`
	Body     map[string]interface{} `json:"body"`
}

type BatchResponseLine struct {
	ID       string                  `json:"id"`
	CustomID string                  `json:"custom_id"`
	Response *BatchResponseLineBody  `json:"response"`
	Error    *BatchResponseLineError `json:"error"`
}

type BatchResponseLineBody struct {
	StatusCode int                    `json:"status_code"`
	RequestID  string                 `json:"request_id"`
	Body       map[string]interface{} `json:"body"`
}

type BatchResponseLineError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ── AutoMigrate ───────────────────────────────────────────────────────────────

func AutoMigrate(db *gorm.DB) error {
	return db.AutoMigrate(&File{}, &Batch{})
}
