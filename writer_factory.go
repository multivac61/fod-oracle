package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// FODWithRebuild extends FOD with rebuild information
type FODWithRebuild struct {
	DrvPath       string `json:"DrvPath"`
	OutputPath    string `json:"OutputPath"`
	ExpectedHash  string `json:"ExpectedHash"`
	ActualHash    string `json:"ActualHash,omitempty"`
	RebuildStatus string `json:"RebuildStatus,omitempty"`
	HashMismatch  bool   `json:"HashMismatch"`
	ErrorMessage  string `json:"ErrorMessage,omitempty"`
}

// toSRIHash converts a hash algorithm and hex hash to SRI format using nix hash convert
func toSRIHash(algorithm, hexHash string) string {
	if hexHash == "" {
		return ""
	}

	// Handle recursive hash algorithms like "r:sha256"
	if strings.HasPrefix(algorithm, "r:") {
		algorithm = strings.TrimPrefix(algorithm, "r:")
	}

	// Use nix hash convert to get the canonical SRI format
	cmd := exec.Command("nix", "hash", "convert", "--hash-algo", algorithm, "--from", "base16", hexHash)
	output, err := cmd.Output()
	if err != nil {
		// Fallback to empty string if nix command fails
		return ""
	}

	// Trim whitespace and return
	return strings.TrimSpace(string(output))
}

// Writer is an interface for writing FOD data
type Writer interface {
	AddFOD(fod FOD)
	IncrementDrvCount()
	Flush()
	Close() error
	// Include methods for reevaluation integration
	AddRebuildInfo(drvPath string, status, actualHash, errorMessage string)
}

// JSONLinesWriter writes FODs with rebuild info as JSON Lines to stdout
type JSONLinesWriter struct {
	db         *sql.DB
	revisionID int64
	mutex      sync.Mutex
	rebuildMap map[string]*RebuildInfo // Cache for rebuild information
	fodMap     map[string]*FOD         // Cache for FOD information
}

// RebuildInfo holds rebuild status information
type RebuildInfo struct {
	Status       string
	ActualHash   string
	ErrorMessage string
}

// NewJSONLinesWriter creates a new JSON Lines writer
func NewJSONLinesWriter(db *sql.DB, revisionID int64) *JSONLinesWriter {
	return &JSONLinesWriter{
		db:         db,
		revisionID: revisionID,
		rebuildMap: make(map[string]*RebuildInfo),
		fodMap:     make(map[string]*FOD),
	}
}

// AddFOD outputs a FOD as JSON Lines immediately (for normal mode)
func (w *JSONLinesWriter) AddFOD(fod FOD) {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	// Always cache the FOD for potential later use
	w.fodMap[fod.DrvPath] = &fod

	// Always output FODs immediately for streaming
	if config.Reevaluate {
		// In reevaluate mode, store in database for rebuild queue but don't output yet
		w.insertFODToDatabase(fod)
	} else {
		// In normal mode, output FODs immediately without rebuild info
		w.outputBasicFODAsJSONLine(fod)
	}
}

// IncrementDrvCount is a no-op for JSON Lines writer
func (w *JSONLinesWriter) IncrementDrvCount() {
	// No-op
}

// AddRebuildInfo stores rebuild information for a FOD
func (w *JSONLinesWriter) AddRebuildInfo(drvPath string, status, actualHash, errorMessage string) {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	w.rebuildMap[drvPath] = &RebuildInfo{
		Status:       status,
		ActualHash:   actualHash,
		ErrorMessage: errorMessage,
	}

	// If FOD is not in cache, try to load it from database
	if _, exists := w.fodMap[drvPath]; !exists {
		w.loadFODFromDatabase(drvPath)
	}

	// Immediately output this FOD as JSON Lines
	w.outputFODAsJSONLine(drvPath)
}

// insertFODToDatabase inserts a FOD into the database
func (w *JSONLinesWriter) insertFODToDatabase(fod FOD) {
	_, err := w.db.Exec(
		"INSERT OR REPLACE INTO fods (drv_path, output_path, hash_algorithm, hash) VALUES (?, ?, ?, ?)",
		fod.DrvPath, fod.OutputPath, fod.HashAlgorithm, fod.Hash,
	)
	if err != nil {
		// Silently ignore database errors to not break JSON output
		return
	}

	// Also insert into drv_revisions table for rebuild queue
	_, err = w.db.Exec(
		"INSERT OR REPLACE INTO drv_revisions (drv_path, revision_id) VALUES (?, ?)",
		fod.DrvPath, w.revisionID,
	)
	if err != nil {
		// Silently ignore database errors to not break JSON output
		return
	}
}

// loadFODFromDatabase loads FOD information from the database into the cache
func (w *JSONLinesWriter) loadFODFromDatabase(drvPath string) {
	var fod FOD
	err := w.db.QueryRow(
		"SELECT drv_path, output_path, hash_algorithm, hash FROM fods WHERE drv_path = ?",
		drvPath,
	).Scan(&fod.DrvPath, &fod.OutputPath, &fod.HashAlgorithm, &fod.Hash)

	if err == nil {
		w.fodMap[drvPath] = &fod
	}
}

// outputFODAsJSONLine outputs a single FOD with rebuild info as a JSON line
func (w *JSONLinesWriter) outputFODAsJSONLine(drvPath string) {
	// Get FOD info from cache
	fodPtr, exists := w.fodMap[drvPath]
	if !exists {
		return // Skip if FOD not found in cache
	}
	fod := *fodPtr

	// Get rebuild info from cache
	rebuildInfo, exists := w.rebuildMap[drvPath]
	if !exists {
		return // Skip if no rebuild info
	}

	// Create FODWithRebuild struct with simplified hash fields
	fodWithRebuild := FODWithRebuild{
		DrvPath:       fod.DrvPath,
		OutputPath:    fod.OutputPath,
		ExpectedHash:  toSRIHash(fod.HashAlgorithm, fod.Hash),
		ActualHash:    toSRIHash(fod.HashAlgorithm, rebuildInfo.ActualHash),
		RebuildStatus: rebuildInfo.Status,
		ErrorMessage:  rebuildInfo.ErrorMessage,
	}

	// Determine if there's a hash mismatch
	hashMismatch := rebuildInfo.Status == "hash_mismatch" ||
		(rebuildInfo.ActualHash != "" && rebuildInfo.ActualHash != fod.Hash)
	fodWithRebuild.HashMismatch = hashMismatch

	// Output as JSON line to stdout
	jsonBytes, err := json.Marshal(fodWithRebuild)
	if err != nil {
		return // Skip if JSON marshaling fails
	}

	fmt.Println(string(jsonBytes))
	// Force flush to ensure immediate output
	os.Stdout.Sync()

	// Debug: Log when FOD is output (only when debug is enabled)
	if config.Debug {
		debugLog("OUTPUT: FOD %s", fod.DrvPath)
	}
}

// outputBasicFODAsJSONLine outputs a FOD without rebuild info (for normal mode)
func (w *JSONLinesWriter) outputBasicFODAsJSONLine(fod FOD) {
	// Create basic FOD struct without rebuild info
	basicFOD := struct {
		DrvPath      string `json:"DrvPath"`
		OutputPath   string `json:"OutputPath"`
		ExpectedHash string `json:"ExpectedHash"`
	}{
		DrvPath:      fod.DrvPath,
		OutputPath:   fod.OutputPath,
		ExpectedHash: toSRIHash(fod.HashAlgorithm, fod.Hash),
	}

	// Output as JSON line to stdout
	jsonBytes, err := json.Marshal(basicFOD)
	if err != nil {
		return // Skip if JSON marshaling fails
	}

	fmt.Println(string(jsonBytes))
	// Force flush to ensure immediate output
	os.Stdout.Sync()
}

// Flush is a no-op for JSON Lines writer
func (w *JSONLinesWriter) Flush() {
	// No-op
}

// Close is a no-op for JSON Lines writer
func (w *JSONLinesWriter) Close() error {
	return nil
}

// GetWriter returns a JSON Lines writer for streaming output
func GetWriter(db *sql.DB, revisionID int64, rev string) (Writer, error) {
	// Always use JSON Lines writer for streaming output
	return NewJSONLinesWriter(db, revisionID), nil
}
