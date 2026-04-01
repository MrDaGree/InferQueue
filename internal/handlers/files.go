package handlers

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/labstack/echo/v4"
	"gorm.io/gorm"

	dbpkg "inferqueue/internal/db"
	"inferqueue/internal/models"
)

type FilesHandler struct {
	db        *gorm.DB
	fileStore string
}

func NewFilesHandler(db *gorm.DB, fileStore string) *FilesHandler {
	return &FilesHandler{db: db, fileStore: fileStore}
}

// GET /v1/files — list files (spec requires this endpoint)
func (h *FilesHandler) List(c echo.Context) error {
	purpose := c.QueryParam("purpose")
	limit := 10000 // spec default
	if l := c.QueryParam("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 10000 {
			limit = n
		}
	}
	afterID := c.QueryParam("after")
	order := c.QueryParam("order")
	if order == "" {
		order = "desc"
	}

	files, err := dbpkg.ListFiles(h.db, userID(c), purpose, limit+1, afterID, order)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	hasMore := len(files) > limit
	if hasMore {
		files = files[:limit]
	}

	responses := make([]models.FileResponse, len(files))
	for i, f := range files {
		responses[i] = f.ToResponse()
	}

	var firstID, lastID *string
	if len(files) > 0 {
		firstID = &files[0].ID
		lastID = &files[len(files)-1].ID
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"object":   "list",
		"data":     responses,
		"first_id": firstID,
		"last_id":  lastID,
		"has_more": hasMore,
	})
}

// POST /v1/files
func (h *FilesHandler) Upload(c echo.Context) error {
	purpose := c.FormValue("purpose")
	if purpose != "batch" {
		return echo.NewHTTPError(http.StatusBadRequest,
			fmt.Sprintf("unsupported purpose %q — use \"batch\"", purpose))
	}

	fh, err := c.FormFile("file")
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "missing file field")
	}

	// Enforce 200 MB limit at the upload boundary
	if fh.Size > models.MaxFileSizeBytes {
		return echo.NewHTTPError(http.StatusBadRequest,
			fmt.Sprintf("file exceeds 200 MB limit (%d bytes)", fh.Size))
	}

	src, err := fh.Open()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "cannot open uploaded file")
	}
	defer src.Close()

	content, err := io.ReadAll(src)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "cannot read uploaded file")
	}

	// Validate JSONL on upload — spec transitions file to "processed" synchronously
	lines, err := parseAndValidateJSONL(content)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	user := userID(c)
	fileObj := models.NewFile(fh.Filename, int64(len(content)), models.PurposeBatch, user)

	dest := filepath.Join(h.fileStore, fileObj.ID)
	if err := os.WriteFile(dest, content, 0o644); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "cannot store file")
	}

	if err := dbpkg.SaveFile(h.db, fileObj); err != nil {
		_ = os.Remove(dest)
		return echo.NewHTTPError(http.StatusInternalServerError, "cannot save file record")
	}

	slog.Info("file uploaded",
		"file_id", fileObj.ID,
		"user", user,
		"bytes", len(content),
		"lines", len(lines),
	)
	return c.JSON(http.StatusCreated, fileObj.ToResponse())
}

// GET /v1/files/:file_id
func (h *FilesHandler) GetMetadata(c echo.Context) error {
	f, err := dbpkg.GetFile(h.db, c.Param("file_id"))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, "file not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, f.ToResponse())
}

// GET /v1/files/:file_id/content
func (h *FilesHandler) GetContent(c echo.Context) error {
	f, err := dbpkg.GetFile(h.db, c.Param("file_id"))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, "file not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	path := filepath.Join(h.fileStore, f.ID)
	content, err := os.ReadFile(path)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "file content not found on disk")
	}
	return c.Blob(http.StatusOK, "application/jsonl", content)
}

// DELETE /v1/files/:file_id
func (h *FilesHandler) Delete(c echo.Context) error {
	fileID := c.Param("file_id")
	if _, err := dbpkg.GetFile(h.db, fileID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, "file not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	_ = os.Remove(filepath.Join(h.fileStore, fileID))
	if err := dbpkg.DeleteFile(h.db, fileID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"id": fileID, "object": "file", "deleted": true,
	})
}
