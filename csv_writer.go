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

// CSVWriter writes FODs to a CSV file with batching support
type CSVWriter struct {
	outputPath string
	file       *os.File
	csvWriter  *csv.Writer
	batchSize  int
	fods       []FOD
	revisionID int64
	mu         sync.Mutex
	totalCount int
	isOpen     bool
}

// NewCSVWriter creates a new CSV writer
func NewCSVWriter(outputPath string, revisionID int64) (*CSVWriter, error) {
	// Create directory if it doesn't exist
	dir := filepath.Dir(outputPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create directory: %w", err)
	}

	// Open the file immediately for writing
	file, err := os.Create(outputPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create CSV file: %w", err)
	}

	// Create CSV writer
	csvWriter := csv.NewWriter(file)

	// Write header
	var header []string
	if config.IsNixExpr {
		header = []string{"drv_path", "output_path", "hash_algorithm", "hash"}
	} else {
		header = []string{"drv_path", "output_path", "hash_algorithm", "hash", "revision_id"}
	}

	if err := csvWriter.Write(header); err != nil {
		file.Close()
		return nil, fmt.Errorf("failed to write CSV header: %w", err)
	}
	csvWriter.Flush()

	return &CSVWriter{
		outputPath: outputPath,
		file:       file,
		csvWriter:  csvWriter,
		batchSize:  10000, // Flush to disk every 10,000 FODs
		fods:       make([]FOD, 0, 10000),
		revisionID: revisionID,
		isOpen:     true,
	}, nil
}

// AddFOD adds a FOD to the CSV file
func (w *CSVWriter) AddFOD(fod FOD) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.fods = append(w.fods, fod)

	// Flush to disk when batch size is reached
	if len(w.fods) >= w.batchSize {
		w.writeBatch()
	}
}

// writeBatch writes the current batch to disk and clears the memory buffer
func (w *CSVWriter) writeBatch() {
	if !w.isOpen || len(w.fods) == 0 {
		return
	}

	// Write the current batch to file
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

		if err := w.csvWriter.Write(record); err != nil {
			log.Printf("Warning: failed to write CSV record: %v", err)
			continue
		}
	}

	// Flush to disk
	w.csvWriter.Flush()

	// Update total count
	w.totalCount += len(w.fods)

	// Log progress
	log.Printf("Wrote batch of %d FODs to CSV file (total: %d)", len(w.fods), w.totalCount)

	// Clear the batch
	w.fods = w.fods[:0]
}

// IncrementDrvCount is a no-op for CSV but implemented for compatibility
func (w *CSVWriter) IncrementDrvCount() {
	// No-op for CSV writer
}

// Flush writes any pending FODs to disk
func (w *CSVWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.writeBatch()
}

// Close writes any remaining FODs and closes the file
func (w *CSVWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.isOpen {
		return nil
	}

	// Write any remaining FODs
	w.writeBatch()

	// Close the writer and file
	w.csvWriter.Flush()
	err := w.file.Close()
	w.isOpen = false

	log.Printf("Wrote total of %d FODs to CSV file: %s", w.totalCount, w.outputPath)
	return err
}
