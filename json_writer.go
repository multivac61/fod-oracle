package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// JSONWriter writes FODs to a JSON file
type JSONWriter struct {
	outputPath string
	fods       []FOD
	revisionID int64
	mu         sync.Mutex
}

// NewJSONWriter creates a new JSON writer
func NewJSONWriter(outputPath string, revisionID int64) (*JSONWriter, error) {
	// Create directory if it doesn't exist
	dir := filepath.Dir(outputPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory: %w", err)
	}

	return &JSONWriter{
		outputPath: outputPath,
		fods:       make([]FOD, 0),
		revisionID: revisionID,
	}, nil
}

// AddFOD adds a FOD to the JSON file
func (w *JSONWriter) AddFOD(fod FOD) {
	w.mu.Lock()
	defer w.mu.Unlock()
	
	w.fods = append(w.fods, fod)
	
	if len(w.fods)%1000 == 0 {
		log.Printf("Collected %d FODs for JSON file", len(w.fods))
	}
}

// IncrementDrvCount is a no-op for JSON but implemented for compatibility
func (w *JSONWriter) IncrementDrvCount() {
	// No-op for JSON writer
}

// Flush is a no-op for JSON but implemented for compatibility
func (w *JSONWriter) Flush() {
	// Will write on Close
}

// Close writes the collected FODs to a JSON file
func (w *JSONWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	
	var data []byte
	var err error
	
	if config.IsNixExpr {
		// For Nix expressions, don't include revision ID
		data, err = json.MarshalIndent(w.fods, "", "  ")
	} else {
		// For nixpkgs revisions, include revision ID
		type JSONRecord struct {
			FOD
			RevisionID int64
		}
		
		records := make([]JSONRecord, len(w.fods))
		for i, fod := range w.fods {
			records[i] = JSONRecord{
				FOD:        fod,
				RevisionID: w.revisionID,
			}
		}
		
		data, err = json.MarshalIndent(records, "", "  ")
	}
	
	if err != nil {
		return fmt.Errorf("failed to marshal FODs to JSON: %w", err)
	}
	
	if err := os.WriteFile(w.outputPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write JSON file: %w", err)
	}
	
	log.Printf("Wrote %d FODs to JSON file: %s", len(w.fods), w.outputPath)
	return nil
}