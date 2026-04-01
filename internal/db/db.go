package db

import (
	"fmt"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"inferqueue/internal/models"
)

func Connect(dsn string) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Info),
	})
	if err != nil {
		return nil, fmt.Errorf("db connect: %w", err)
	}
	if err := models.AutoMigrate(db); err != nil {
		return nil, fmt.Errorf("db migrate: %w", err)
	}
	return db, nil
}

// ── File queries ──────────────────────────────────────────────────────────────

func SaveFile(db *gorm.DB, f *models.File) error {
	return db.Save(f).Error
}

func GetFile(db *gorm.DB, id string) (*models.File, error) {
	var f models.File
	if err := db.First(&f, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &f, nil
}

func DeleteFile(db *gorm.DB, id string) error {
	return db.Delete(&models.File{}, "id = ?", id).Error
}

// ListFiles returns files for a user with optional purpose filter, pagination, and sort order.
// Implements GET /v1/files per spec.
func ListFiles(db *gorm.DB, createdBy, purpose string, limit int, afterID, order string) ([]*models.File, error) {
	q := db.Where("created_by = ?", createdBy)

	if purpose != "" {
		q = q.Where("purpose = ?", purpose)
	}

	if afterID != "" {
		var pivot models.File
		if err := db.Select("created_at").First(&pivot, "id = ?", afterID).Error; err == nil {
			if order == "asc" {
				q = q.Where("created_at > ?", pivot.CreatedAt)
			} else {
				q = q.Where("created_at < ?", pivot.CreatedAt)
			}
		}
	}

	sortDir := "DESC"
	if order == "asc" {
		sortDir = "ASC"
	}

	var files []*models.File
	return files, q.Order("created_at " + sortDir).Limit(limit).Find(&files).Error
}

// ── Batch queries ─────────────────────────────────────────────────────────────

func SaveBatch(db *gorm.DB, b *models.Batch) error {
	return db.Save(b).Error
}

func GetBatch(db *gorm.DB, id string) (*models.Batch, error) {
	var b models.Batch
	if err := db.First(&b, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &b, nil
}

func ListBatches(db *gorm.DB, createdBy string, limit int, afterID string) ([]*models.Batch, error) {
	q := db.Where("created_by = ?", createdBy).Order("created_at DESC").Limit(limit)
	if afterID != "" {
		var pivot models.Batch
		if err := db.Select("created_at").First(&pivot, "id = ?", afterID).Error; err == nil {
			q = q.Where("created_at < ?", pivot.CreatedAt)
		}
	}
	var batches []*models.Batch
	return batches, q.Find(&batches).Error
}

func UpdateBatchStatus(db *gorm.DB, b *models.Batch) error {
	return db.Model(b).Updates(map[string]interface{}{
		"status":              b.Status,
		"slurm_job_id":        b.SlurmJobID,
		"output_file_id":      b.OutputFileID,
		"error_file_id":       b.ErrorFileID,
		"request_total":       b.RequestTotal,
		"request_completed":   b.RequestCompleted,
		"request_failed":      b.RequestFailed,
		"usage_input_tokens":  b.UsageInputTokens,
		"usage_output_tokens": b.UsageOutputTokens,
		"in_progress_at":      b.InProgressAt,
		"finalizing_at":       b.FinalizingAt,
		"completed_at":        b.CompletedAt,
		"failed_at":           b.FailedAt,
		"cancelling_at":       b.CancellingAt,
		"cancelled_at":        b.CancelledAt,
		"errors":              b.Errors,
	}).Error
}
