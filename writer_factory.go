package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
)

// FODWithRebuild extends FOD with rebuild information
type FODWithRebuild struct {
	DrvPath       string `json:"DrvPath"`
	OutputPath    string `json:"OutputPath"`
	HashAlgorithm string `json:"HashAlgorithm"`
	Hash          string `json:"Hash"`
	RebuildStatus string `json:"rebuild_status,omitempty"`
	ActualHash    string `json:"actual_hash,omitempty"`
	HashMismatch  bool   `json:"hash_mismatch"`
	ErrorMessage  string `json:"error_message,omitempty"`
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
	
	// In normal mode (non-reevaluate), output FODs immediately without rebuild info
	if !config.Reevaluate {
		w.outputBasicFODAsJSONLine(fod)
	}
	// In reevaluate mode, FODs are output when rebuild info is added
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
	
	// Immediately output this FOD as JSON Lines
	w.outputFODAsJSONLine(drvPath)
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
	
	// Create FODWithRebuild struct with exact field names matching the example
	fodWithRebuild := FODWithRebuild{
		DrvPath:       fod.DrvPath,
		OutputPath:    fod.OutputPath,
		HashAlgorithm: fod.HashAlgorithm,
		Hash:          fod.Hash,
		RebuildStatus: rebuildInfo.Status,
		ActualHash:    rebuildInfo.ActualHash,
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
}

// outputBasicFODAsJSONLine outputs a FOD without rebuild info (for normal mode)
func (w *JSONLinesWriter) outputBasicFODAsJSONLine(fod FOD) {
	// Create basic FOD struct without rebuild info
	basicFOD := struct {
		DrvPath       string `json:"DrvPath"`
		OutputPath    string `json:"OutputPath"`
		HashAlgorithm string `json:"HashAlgorithm"`
		Hash          string `json:"Hash"`
	}{
		DrvPath:       fod.DrvPath,
		OutputPath:    fod.OutputPath,
		HashAlgorithm: fod.HashAlgorithm,
		Hash:          fod.Hash,
	}
	
	// Output as JSON line to stdout
	jsonBytes, err := json.Marshal(basicFOD)
	if err != nil {
		return // Skip if JSON marshaling fails
	}
	
	fmt.Println(string(jsonBytes))
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
