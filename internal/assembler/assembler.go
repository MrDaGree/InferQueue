package assembler

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	dbpkg "inferqueue/internal/db"
	"inferqueue/internal/models"
)

// AssembleOutput collects per-task JSON files and splits them into:
//   - output JSONL  (successful responses)
//   - error JSONL   (failed responses, separate file per OpenAI spec)
//
// Token usage is aggregated from response bodies for the batch.usage field.
// No custom audit logging here — all request logging is handled by APIsix,
// which sees every batch task as a normal API call with the shim's API key.
func AssembleOutput(gdb *gorm.DB, batch *models.Batch, outputDir, fileStore string) error {
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return fmt.Errorf("read output dir: %w", err)
	}

	sort.Slice(entries, func(i, j int) bool {
		return taskIndex(entries[i].Name()) < taskIndex(entries[j].Name())
	})

	var (
		successLines []string
		errorLines   []string
		totalIn      int
		totalOut     int
	)

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(outputDir, e.Name()))
		if err != nil {
			slog.Warn("failed reading task output", "file", e.Name(), "err", err)
			continue
		}

		var line models.BatchResponseLine
		if err := json.Unmarshal(raw, &line); err != nil {
			slog.Warn("failed parsing task output", "file", e.Name(), "err", err)
			continue
		}

		// Accumulate token usage from the response body
		if line.Response != nil && line.Response.Body != nil {
			if usage, ok := line.Response.Body["usage"].(map[string]interface{}); ok {
				if v, ok := usage["prompt_tokens"].(float64); ok {
					totalIn += int(v)
				}
				if v, ok := usage["completion_tokens"].(float64); ok {
					totalOut += int(v)
				}
			}
		}

		cleaned, _ := json.Marshal(line)
		if line.Error != nil {
			errorLines = append(errorLines, string(cleaned))
		} else {
			successLines = append(successLines, string(cleaned))
		}
	}

	now := time.Now()

	if len(successLines) > 0 {
		content := []byte(strings.Join(successLines, "\n"))
		fileID := "file-out-" + batch.ID
		if err := os.WriteFile(filepath.Join(fileStore, fileID), content, 0o644); err != nil {
			return fmt.Errorf("write output file: %w", err)
		}
		expires := now.AddDate(0, 0, models.FileExpiryDays)
		f := models.NewFile(batch.ID+"_output.jsonl", int64(len(content)), models.PurposeBatchOutput, batch.CreatedBy)
		f.ID = fileID
		f.CreatedAt = now
		f.ExpiresAt = &expires
		if err := dbpkg.SaveFile(gdb, f); err != nil {
			return fmt.Errorf("save output file record: %w", err)
		}
		batch.OutputFileID = &fileID
	}

	if len(errorLines) > 0 {
		content := []byte(strings.Join(errorLines, "\n"))
		fileID := "file-err-" + batch.ID
		if err := os.WriteFile(filepath.Join(fileStore, fileID), content, 0o644); err != nil {
			return fmt.Errorf("write error file: %w", err)
		}
		expires := now.AddDate(0, 0, models.FileExpiryDays)
		f := models.NewFile(batch.ID+"_errors.jsonl", int64(len(content)), models.PurposeBatchOutput, batch.CreatedBy)
		f.ID = fileID
		f.CreatedAt = now
		f.ExpiresAt = &expires
		if err := dbpkg.SaveFile(gdb, f); err != nil {
			return fmt.Errorf("save error file record: %w", err)
		}
		batch.ErrorFileID = &fileID
	}

	batch.UsageInputTokens = totalIn
	batch.UsageOutputTokens = totalOut

	slog.Info("output assembled",
		"batch_id", batch.ID,
		"success_lines", len(successLines),
		"error_lines", len(errorLines),
		"input_tokens", totalIn,
		"output_tokens", totalOut,
	)
	return nil
}

func taskIndex(name string) int {
	base := strings.TrimSuffix(name, ".json")
	n, _ := strconv.Atoi(base)
	return n
}
