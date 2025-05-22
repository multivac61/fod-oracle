package main

import (
	"encoding/csv"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

// CSVWriter writes FODs to a CSV file
type CSVWriter struct {
	outputPath string
	fods       []FOD
	revisionID int64
	mu         sync.Mutex
}

// NewCSVWriter creates a new CSV writer
func NewCSVWriter(outputPath string, revisionID int64) (*CSVWriter, error) {
	// Create directory if it doesn't exist
	dir := filepath.Dir(outputPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory: %w", err)
	}

	return &CSVWriter{
		outputPath: outputPath,
		fods:       make([]FOD, 0),
		revisionID: revisionID,
	}, nil
}

// AddFOD adds a FOD to the CSV file
func (w *CSVWriter) AddFOD(fod FOD) {
	w.mu.Lock()
	defer w.mu.Unlock()
	
	w.fods = append(w.fods, fod)
	
	if len(w.fods)%1000 == 0 {
		log.Printf("Collected %d FODs for CSV file", len(w.fods))
	}
}

// IncrementDrvCount is a no-op for CSV but implemented for compatibility
func (w *CSVWriter) IncrementDrvCount() {
	// No-op for CSV writer
}

// Flush is a no-op for CSV but implemented for compatibility
func (w *CSVWriter) Flush() {
	// Will write on Close
}

// Close writes the collected FODs to a CSV file
func (w *CSVWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	
	if len(w.fods) == 0 {
		log.Printf("No FODs to write to CSV file")
		return nil
	}
	
	// Create the file
	file, err := os.Create(w.outputPath)
	if err != nil {
		return fmt.Errorf("failed to create CSV file: %w", err)
	}
	defer file.Close()
	
	// Create CSV writer
	csvWriter := csv.NewWriter(file)
	defer csvWriter.Flush()
	
	// Write header
	var header []string
	if config.IsNixExpr {
		header = []string{"drv_path", "output_path", "hash_algorithm", "hash"}
	} else {
		header = []string{"drv_path", "output_path", "hash_algorithm", "hash", "revision_id"}
	}
	
	if err := csvWriter.Write(header); err != nil {
		return fmt.Errorf("failed to write CSV header: %w", err)
	}
	
	// Write data
	for _, fod := range w.fods {
		var record []string
		if config.IsNixExpr {
			record = []string{
				fod.DrvPath,
				fod.OutputPath,
				fod.HashAlgorithm,
				fod.Hash,
			}
		} else {
			record = []string{
				fod.DrvPath,
				fod.OutputPath,
				fod.HashAlgorithm,
				fod.Hash,
				strconv.FormatInt(w.revisionID, 10),
			}
		}
		
		if err := csvWriter.Write(record); err != nil {
			return fmt.Errorf("failed to write CSV record: %w", err)
		}
	}
	
	log.Printf("Wrote %d FODs to CSV file: %s", len(w.fods), w.outputPath)
	return nil
}