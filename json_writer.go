package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// JSONWriter writes FODs to a JSON file with batching support
type JSONWriter struct {
	outputPath string
	file       *os.File
	batchSize  int
	fods       []FOD
	revisionID int64
	mu         sync.Mutex
	totalCount int
	isOpen     bool
	isFirst    bool
}

// NewJSONWriter creates a new JSON writer
func NewJSONWriter(outputPath string, revisionID int64) (*JSONWriter, error) {
	// Create directory if it doesn't exist
	dir := filepath.Dir(outputPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create directory: %w", err)
	}

	// Open the file immediately for writing
	file, err := os.Create(outputPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create JSON file: %w", err)
	}

	// Write the opening bracket for the JSON array
	if _, err := file.WriteString("[\n"); err != nil {
		file.Close()
		return nil, fmt.Errorf("failed to write JSON opening bracket: %w", err)
	}

	return &JSONWriter{
		outputPath: outputPath,
		file:       file,
		batchSize:  5000, // Flush to disk every 5,000 FODs (smaller than CSV because JSON serialization is more memory intensive)
		fods:       make([]FOD, 0, 5000),
		revisionID: revisionID,
		isOpen:     true,
		isFirst:    true,
	}, nil
}

// AddFOD adds a FOD to the JSON file
func (w *JSONWriter) AddFOD(fod FOD) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.fods = append(w.fods, fod)

	// Flush to disk when batch size is reached
	if len(w.fods) >= w.batchSize {
		w.writeBatch()
	}
}

// writeBatch writes the current batch to disk and clears the memory buffer
func (w *JSONWriter) writeBatch() {
	if !w.isOpen || len(w.fods) == 0 {
		return
	}

	// Prepare the batch for JSON serialization
	var records interface{}

	if config.IsNixExpr {
		// For Nix expressions, don't include revision ID
		records = w.fods
	} else {
		// For nixpkgs revisions, include revision ID
		type JSONRecord struct {
			FOD
			RevisionID int64 `json:"revision_id"`
		}

		jsonRecords := make([]JSONRecord, len(w.fods))
		for i, fod := range w.fods {
			jsonRecords[i] = JSONRecord{
				FOD:        fod,
				RevisionID: w.revisionID,
			}
		}
		records = jsonRecords
	}

	// Marshal to JSON
	data, err := json.Marshal(records)
	if err != nil {
		log.Printf("Warning: failed to marshal FODs to JSON: %v", err)
		return
	}

	// Write to file
	if !w.isFirst {
		// If not the first batch, prepend with a comma and newline
		if _, err := w.file.WriteString(",\n"); err != nil {
			log.Printf("Warning: failed to write JSON separator: %v", err)
			return
		}
	} else {
		w.isFirst = false
	}

	// Remove the opening and closing brackets from the JSON array since we're writing
	// in batches and will add our own brackets at the beginning and end
	dataStr := string(data)
	if len(dataStr) > 2 {
		dataStr = dataStr[1 : len(dataStr)-1] // Remove [ and ]
	}

	if _, err := w.file.WriteString(dataStr); err != nil {
		log.Printf("Warning: failed to write JSON data: %v", err)
		return
	}

	// Update total count
	w.totalCount += len(w.fods)

	// Log progress
	log.Printf("Wrote batch of %d FODs to JSON file (total: %d)", len(w.fods), w.totalCount)

	// Clear the batch
	w.fods = w.fods[:0]
}

// IncrementDrvCount is a no-op for JSON but implemented for compatibility
func (w *JSONWriter) IncrementDrvCount() {
	// No-op for JSON writer
}

// Flush writes any pending FODs to disk
func (w *JSONWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.writeBatch()
}

// Close writes any remaining FODs and closes the file
func (w *JSONWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.isOpen {
		return nil
	}

	// Write any remaining FODs
	w.writeBatch()

	// Write the closing bracket for the JSON array
	if _, err := w.file.WriteString("\n]"); err != nil {
		log.Printf("Warning: failed to write JSON closing bracket: %v", err)
	}

	// Close the file
	err := w.file.Close()
	w.isOpen = false

	log.Printf("Wrote total of %d FODs to JSON file: %s", w.totalCount, w.outputPath)
	return err
}
