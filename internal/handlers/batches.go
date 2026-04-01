package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"gorm.io/gorm"

	"inferqueue/internal/assembler"
	"inferqueue/internal/config"
	dbpkg "inferqueue/internal/db"
	"inferqueue/internal/models"
	"inferqueue/internal/slurm"
)

type BatchesHandler struct {
	db  *gorm.DB
	cfg *config.Config
}

func NewBatchesHandler(db *gorm.DB, cfg *config.Config) *BatchesHandler {
	return &BatchesHandler{db: db, cfg: cfg}
}

// POST /v1/batches
func (h *BatchesHandler) Create(c echo.Context) error {
	var body struct {
		InputFileID      string            `json:"input_file_id"`
		Endpoint         string            `json:"endpoint"`
		CompletionWindow string            `json:"completion_window"`
		Metadata         map[string]string `json:"metadata"`
	}
	if err := c.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}

	// Parse completion_window — any valid Go duration string, minimum 24h.
	windowDur, windowStr, err := models.ParseCompletionWindow(body.CompletionWindow)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	deadline := time.Now().Add(windowDur)

	if body.Endpoint == "" {
		body.Endpoint = "/v1/chat/completions"
	}
	supported := map[string]bool{
		"/v1/chat/completions": true,
		"/v1/completions":      true,
		"/v1/embeddings":       true,
	}
	if !supported[body.Endpoint] {
		return echo.NewHTTPError(http.StatusBadRequest,
			fmt.Sprintf("unsupported endpoint %q", body.Endpoint))
	}

	fileObj, err := dbpkg.GetFile(h.db, body.InputFileID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return echo.NewHTTPError(http.StatusNotFound,
				fmt.Sprintf("input_file_id %q not found", body.InputFileID))
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	inputPath := filepath.Join(h.cfg.FileStore, fileObj.ID)
	content, err := os.ReadFile(inputPath)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "cannot read input file")
	}
	lines, err := parseAndValidateJSONL(content)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	model := resolveModel(lines)
	user := userID(c)

	var metaJSON []byte
	if body.Metadata != nil {
		metaJSON, _ = json.Marshal(body.Metadata)
	}

	batch := models.NewBatch(body.InputFileID, body.Endpoint, model, windowStr, metaJSON, user)
	batch.RequestTotal = len(lines)

	outputDir := filepath.Join(h.cfg.FileStore, batch.ID+"_output")

	jobID, submitErr := slurm.SubmitJob(h.cfg, slurm.SubmitParams{
		BatchID:            batch.ID,
		InputFilePath:      inputPath,
		OutputDir:          outputDir,
		RequestCount:       len(lines),
		Endpoint:           body.Endpoint,
		WindowDur:          windowDur,
		Deadline:           deadline,
		APIsixURL:          h.cfg.APIsixURL,
		BatchAPIKey:        h.cfg.BatchAPIKey,
		VLLMMetricsMap:     serialiseMap(h.cfg.VLLMMetricsMap),
		MaxRunningRequests: h.cfg.VLLMMaxRunningRequests,
		WaitTimeoutSec:     h.cfg.VLLMWaitTimeoutSec,
	})

	if submitErr != nil {
		slog.Error("slurm submission failed", "batch_id", batch.ID, "err", submitErr)
		batch.Status = models.StatusFailed
		now := time.Now()
		batch.FailedAt = &now
		errJSON, _ := json.Marshal([]map[string]string{
			{"code": "slurm_error", "message": submitErr.Error()},
		})
		batch.Errors = errJSON
	} else {
		batch.SlurmJobID = &jobID
		batch.Status = models.StatusInProgress
		now := time.Now()
		batch.InProgressAt = &now
	}

	if err := dbpkg.SaveBatch(h.db, batch); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "cannot save batch")
	}

	slog.Info("batch created",
		"batch_id", batch.ID,
		"user", user,
		"model", model,
		"requests", len(lines),
		"slurm_job", jobID,
		"partition", slurm.PickPartition(h.cfg, len(lines), windowDur, deadline),
	)

	return c.JSON(http.StatusCreated, batch.ToResponse())
}

// GET /v1/batches
func (h *BatchesHandler) List(c echo.Context) error {
	limit := 20
	if l := c.QueryParam("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}

	batches, err := dbpkg.ListBatches(h.db, userID(c), limit+1, c.QueryParam("after"))
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	hasMore := len(batches) > limit
	if hasMore {
		batches = batches[:limit]
	}

	for _, b := range batches {
		h.refreshStatus(b)
	}

	responses := make([]models.BatchResponse, len(batches))
	for i, b := range batches {
		responses[i] = b.ToResponse()
	}

	var firstID, lastID *string
	if len(batches) > 0 {
		firstID = &batches[0].ID
		lastID = &batches[len(batches)-1].ID
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"object":   "list",
		"data":     responses,
		"first_id": firstID,
		"last_id":  lastID,
		"has_more": hasMore,
	})
}

// GET /v1/batches/:batch_id
func (h *BatchesHandler) Get(c echo.Context) error {
	b, err := dbpkg.GetBatch(h.db, c.Param("batch_id"))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, "batch not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	h.refreshStatus(b)
	return c.JSON(http.StatusOK, b.ToResponse())
}

// POST /v1/batches/:batch_id/cancel
func (h *BatchesHandler) Cancel(c echo.Context) error {
	b, err := dbpkg.GetBatch(h.db, c.Param("batch_id"))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, "batch not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if b.Status.IsTerminal() {
		return echo.NewHTTPError(http.StatusBadRequest,
			fmt.Sprintf("batch already in terminal state: %s", b.Status))
	}
	if b.SlurmJobID != nil {
		if err := slurm.CancelJob(*b.SlurmJobID); err != nil {
			slog.Warn("scancel failed", "job_id", b.SlurmJobID, "err", err)
		}
	}
	now := time.Now()
	b.Status = models.StatusCancelling
	b.CancellingAt = &now
	_ = dbpkg.UpdateBatchStatus(h.db, b)

	slog.Info("batch cancel", "batch_id", b.ID, "user", userID(c))
	return c.JSON(http.StatusOK, b.ToResponse())
}

// ── Internal helpers ──────────────────────────────────────────────────────────

func (h *BatchesHandler) refreshStatus(b *models.Batch) {
	if b.Status.IsTerminal() || b.SlurmJobID == nil {
		return
	}
	status, err := slurm.PollJob(*b.SlurmJobID)
	if err != nil {
		slog.Warn("sacct poll failed", "err", err)
		return
	}

	now := time.Now()
	b.RequestTotal = status.Total
	b.RequestCompleted = status.Completed
	b.RequestFailed = status.Failed

	switch status.Overall {
	case models.StatusInProgress:
		if b.InProgressAt == nil {
			b.InProgressAt = &now
		}
	case models.StatusFinalizing:
		if b.FinalizingAt == nil {
			b.FinalizingAt = &now
		}
	case models.StatusCompleted:
		if b.CompletedAt == nil {
			b.CompletedAt = &now
			h.tryAssemble(b)
		}
	case models.StatusFailed:
		if b.FailedAt == nil {
			b.FailedAt = &now
		}
	case models.StatusCancelled:
		if b.CancelledAt == nil {
			b.CancelledAt = &now
		}
	}

	b.Status = status.Overall
	_ = dbpkg.UpdateBatchStatus(h.db, b)
}

func (h *BatchesHandler) tryAssemble(b *models.Batch) {
	if b.OutputFileID != nil {
		return
	}
	outputDir := filepath.Join(h.cfg.FileStore, b.ID+"_output")
	if err := assembler.AssembleOutput(h.db, b, outputDir, h.cfg.FileStore); err != nil {
		slog.Error("output assembly failed", "batch_id", b.ID, "err", err)
	}
}

func serialiseMap(m map[string]string) string {
	parts := make([]string, 0, len(m))
	for k, v := range m {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, ",")
}
