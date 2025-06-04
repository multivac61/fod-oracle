package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/multivac61/fod-oracle/pkg/fod"
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
	db             *sql.DB
	revisionID     int64
	mutex          sync.Mutex
	rebuildMap     map[string]*RebuildInfo // Cache for rebuild information
	fodMap         map[string]*FOD         // Cache for FOD information
	rebuildChan    chan FOD                // Channel for concurrent rebuilding
	workersWG      sync.WaitGroup          // Wait group for workers
	workersStarted bool                    // Track if workers are started
}

// RebuildInfo holds rebuild status information
type RebuildInfo struct {
	Status       string
	ActualHash   string
	ErrorMessage string
}

// NewJSONLinesWriter creates a new JSON Lines writer
func NewJSONLinesWriter(db *sql.DB, revisionID int64) *JSONLinesWriter {
	w := &JSONLinesWriter{
		db:             db,
		revisionID:     revisionID,
		rebuildMap:     make(map[string]*RebuildInfo),
		fodMap:         make(map[string]*FOD),
		rebuildChan:    make(chan FOD, 10000), // Large buffer for discovered FODs
		workersStarted: false,
	}

	return w
}

// AddFOD outputs a FOD as JSON Lines immediately (for normal mode)
func (w *JSONLinesWriter) AddFOD(fod FOD) {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	// Always cache the FOD for potential later use
	w.fodMap[fod.DrvPath] = &fod

	// Output behavior depends on reevaluate mode
	if config.Reevaluate {
		// In reevaluate mode, store in database and send to rebuild workers
		w.insertFODToDatabase(fod)

		// Start workers on first FOD
		if !w.workersStarted {
			w.startRebuildWorkers()
			w.workersStarted = true
		}

		// Send to rebuild workers immediately (non-blocking)
		select {
		case w.rebuildChan <- fod:
			debugLog("DISCOVERY: Queued FOD for immediate rebuild: %s", fod.DrvPath)
		default:
			debugLog("DISCOVERY: Channel full, skipping FOD: %s", fod.DrvPath)
		}

		// Don't output basic FOD - only output rebuild results
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

	// Output as JSON line to stdout - THIS IS THE REBUILD RESULT
	jsonBytes, err := json.Marshal(fodWithRebuild)
	if err != nil {
		return // Skip if JSON marshaling fails
	}

	fmt.Println(string(jsonBytes))
	// Force flush to ensure immediate output
	os.Stdout.Sync()

	// Debug: Log when rebuild result is output
	if config.Debug {
		debugLog("REBUILD RESULT: FOD %s - %s", fod.DrvPath, rebuildInfo.Status)
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

// Close waits for all rebuild workers to finish
func (w *JSONLinesWriter) Close() error {
	if config.Reevaluate && w.workersStarted {
		debugLog("Discovery complete, waiting for all rebuilds to finish...")

		// Close the channel to signal no more FODs
		close(w.rebuildChan)

		// Wait for all workers to finish
		w.workersWG.Wait()

		debugLog("All rebuild workers completed")
	}
	return nil
}

// startRebuildWorkers starts concurrent rebuild workers
func (w *JSONLinesWriter) startRebuildWorkers() {
	numWorkers := config.ParallelWorkers
	debugLog("Starting %d concurrent rebuild workers", numWorkers)

	for i := 0; i < numWorkers; i++ {
		w.workersWG.Add(1)
		go w.rebuildWorker(i)
	}
}

// rebuildWorker processes FODs from the channel concurrently
func (w *JSONLinesWriter) rebuildWorker(workerID int) {
	defer w.workersWG.Done()
	debugLog("Rebuild worker %d started", workerID)

	for fod := range w.rebuildChan {
		// Apply build delay if configured
		if config.BuildDelay > 0 {
			time.Sleep(time.Duration(config.BuildDelay) * time.Second)
		}

		// Rebuild the FOD
		status, actualHash, errorMsg := w.rebuildSingleFOD(fod.DrvPath)

		// Output rebuild result immediately
		w.AddRebuildInfo(fod.DrvPath, status, actualHash, errorMsg)
	}

	debugLog("Rebuild worker %d finished", workerID)
}

// rebuildSingleFOD rebuilds a single FOD and returns the result
func (w *JSONLinesWriter) rebuildSingleFOD(drvPath string) (status, actualHash, errorMessage string) {
	debugLog("Rebuilding FOD: %s", drvPath)

	// Set timeout
	timeoutSeconds := 300 // 5 minutes
	if config.BuildDelay <= 0 {
		timeoutSeconds = 30 // 30 seconds in testing mode
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	// Use the existing rebuild logic
	result, err := fod.RebuildFOD(ctx, drvPath, config.Debug)
	if err != nil {
		return "failure", "", fmt.Sprintf("Rebuild failed: %v", err)
	}

	switch result.Status {
	case "success":
		return "success", result.ActualHash, ""
	case "hash_mismatch":
		return "hash_mismatch", result.ActualHash, "Hash mismatch detected"
	case "timeout":
		return "timeout", "", "Rebuild timed out"
	default:
		return "failure", "", result.ErrorMessage
	}
}

// GetWriter returns a JSON Lines writer for streaming output
func GetWriter(db *sql.DB, revisionID int64, rev string) (Writer, error) {
	// Always use JSON Lines writer for streaming output
	return NewJSONLinesWriter(db, revisionID), nil
}
