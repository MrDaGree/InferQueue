package handlers

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/labstack/echo/v4"

	"inferqueue/internal/models"
)

func userID(c echo.Context) string {
	if uid := c.Request().Header.Get("X-User-Id"); uid != "" {
		return uid
	}
	return "anonymous"
}

// parseAndValidateJSONL parses and validates the input JSONL against the spec:
//   - Each line must be valid JSON with custom_id, method, url, body
//   - custom_id must be unique within the file
//   - File must not exceed MaxRequestsPerBatch lines
//   - File must not exceed MaxFileSizeBytes (enforced at upload, double-checked here)
func parseAndValidateJSONL(content []byte) ([]models.BatchRequestLine, error) {
	if len(content) > models.MaxFileSizeBytes {
		return nil, fmt.Errorf("file exceeds 200 MB limit (%d bytes)", len(content))
	}

	var lines []models.BatchRequestLine
	seen := map[string]bool{}
	lineNum := 0

	dec := json.NewDecoder(newBytesReader(content))
	for dec.More() {
		lineNum++

		if lineNum > models.MaxRequestsPerBatch {
			return nil, fmt.Errorf("file exceeds %d request limit", models.MaxRequestsPerBatch)
		}

		var line models.BatchRequestLine
		if err := dec.Decode(&line); err != nil {
			return nil, fmt.Errorf("invalid JSON at line %d: %w", lineNum, err)
		}
		if line.CustomID == "" {
			return nil, fmt.Errorf("missing custom_id at line %d", lineNum)
		}
		if seen[line.CustomID] {
			return nil, fmt.Errorf("duplicate custom_id %q at line %d", line.CustomID, lineNum)
		}
		if line.Body == nil {
			return nil, fmt.Errorf("missing body at line %d", lineNum)
		}
		if line.Method == "" {
			line.Method = "POST"
		}

		seen[line.CustomID] = true
		lines = append(lines, line)
	}

	if len(lines) == 0 {
		return nil, fmt.Errorf("input file contains no valid JSONL lines")
	}
	return lines, nil
}

// resolveModel inspects the JSONL lines and returns a representative model string.
// For single-model batches returns that model name.
// For multi-model batches returns "mixed".
func resolveModel(lines []models.BatchRequestLine) string {
	seen := map[string]int{}
	for _, l := range lines {
		if m, ok := l.Body["model"].(string); ok && m != "" {
			seen[m]++
		}
	}
	if len(seen) == 0 {
		return ""
	}
	if len(seen) == 1 {
		for m := range seen {
			return m
		}
	}
	return "mixed"
}

// bytesReader wraps []byte as an io.Reader for json.NewDecoder.
type bytesReader struct{ r io.Reader }

func newBytesReader(b []byte) io.Reader {
	// Use a simple wrapper so json.NewDecoder can stream line by line
	return newSimpleReader(b)
}

type simpleReader struct {
	data []byte
	pos  int
}

func newSimpleReader(b []byte) *simpleReader { return &simpleReader{data: b} }

func (r *simpleReader) Read(p []byte) (n int, err error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n = copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}
