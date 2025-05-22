package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/xitongsys/parquet-go-source/local"
	"github.com/xitongsys/parquet-go/parquet"
	"github.com/xitongsys/parquet-go/writer"
)

// ParquetFOD is a struct specifically for Parquet file format
// We need to define struct tags for the Parquet writer
type ParquetFOD struct {
	DrvPath       string `parquet:"name=drv_path, type=BYTE_ARRAY, convertedtype=UTF8, repetitiontype=REQUIRED"`
	OutputPath    string `parquet:"name=output_path, type=BYTE_ARRAY, convertedtype=UTF8, repetitiontype=REQUIRED"`
	HashAlgorithm string `parquet:"name=hash_algorithm, type=BYTE_ARRAY, convertedtype=UTF8, repetitiontype=REQUIRED"`
	Hash          string `parquet:"name=hash, type=BYTE_ARRAY, convertedtype=UTF8, repetitiontype=REQUIRED"`
	RevisionID    int64  `parquet:"name=revision_id, type=INT64, repetitiontype=OPTIONAL"`
}

// ParquetWriter handles writing FODs to a Parquet file with batching support
type ParquetWriter struct {
	outputPath  string
	fods        []ParquetFOD
	revisionID  int64
	batchSize   int
	fileWriter  *local.LocalFileWriter
	parquetWriter *writer.ParquetWriter
	mu          sync.Mutex
	totalCount  int
	isOpen      bool
}

// NewParquetWriter creates a new Parquet writer
func NewParquetWriter(outputPath string, revisionID int64) (*ParquetWriter, error) {
	// Create directory if it doesn't exist
	dir := filepath.Dir(outputPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory: %w", err)
	}

	// Create Parquet file using the local file source
	fw, err := local.NewLocalFileWriter(outputPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create Parquet file: %w", err)
	}

	// Define Parquet writer with proper schema
	pw, err := writer.NewParquetWriter(fw, new(ParquetFOD), 4)
	if err != nil {
		fw.Close()
		return nil, fmt.Errorf("failed to create Parquet writer: %w", err)
	}

	// Set compression and other options
	pw.CompressionType = parquet.CompressionCodec_SNAPPY
	pw.PageSize = 32 * 1024           // 32K page size for better compression
	pw.RowGroupSize = 16 * 1024 * 1024 // 16MB row groups (smaller is better for this data size)

	return &ParquetWriter{
		outputPath:     outputPath,
		fods:           make([]ParquetFOD, 0, 8000),
		revisionID:     revisionID,
		batchSize:      8000, // Flush to disk every 8,000 FODs
		fileWriter:     fw,
		parquetWriter:  pw,
		isOpen:         true,
	}, nil
}

// AddFOD adds a FOD to the Parquet file
func (w *ParquetWriter) AddFOD(fod FOD) {
	w.mu.Lock()
	defer w.mu.Unlock()
	
	if !w.isOpen {
		log.Printf("Warning: Attempted to add FOD to closed Parquet writer")
		return
	}
	
	// Ensure we have valid strings for all fields (Parquet can be picky)
	drvPath := fod.DrvPath
	if drvPath == "" {
		drvPath = "unknown"
	}
	
	outputPath := fod.OutputPath
	if outputPath == "" {
		outputPath = "unknown"
	}
	
	hashAlgo := fod.HashAlgorithm
	if hashAlgo == "" {
		hashAlgo = "unknown"
	}
	
	hash := fod.Hash
	if hash == "" {
		hash = "unknown"
	}
	
	// Convert to ParquetFOD format
	parquetFod := ParquetFOD{
		DrvPath:       drvPath,
		OutputPath:    outputPath,
		HashAlgorithm: hashAlgo,
		Hash:          hash,
	}
	
	// Only add revision ID if not in Nix expression mode
	if !config.IsNixExpr {
		parquetFod.RevisionID = w.revisionID
	}
	
	w.fods = append(w.fods, parquetFod)
	
	// Flush to disk when batch size is reached
	if len(w.fods) >= w.batchSize {
		w.writeBatch()
	}
}

// writeBatch writes the current batch to disk and clears the memory buffer
func (w *ParquetWriter) writeBatch() {
	if !w.isOpen || len(w.fods) == 0 {
		return
	}

	// Write each FOD to the Parquet file
	for _, fod := range w.fods {
		if err := w.parquetWriter.Write(fod); err != nil {
			log.Printf("Warning: failed to write FOD to Parquet: %v", err)
			continue
		}
	}

	// Update total count
	w.totalCount += len(w.fods)
	
	// Log progress
	log.Printf("Wrote batch of %d FODs to Parquet file (total: %d)", len(w.fods), w.totalCount)
	
	// Clear the batch
	w.fods = w.fods[:0]
}

// IncrementDrvCount is a no-op for Parquet but implemented for compatibility
func (w *ParquetWriter) IncrementDrvCount() {
	// No-op for Parquet writer
}

// Flush writes any pending FODs to disk
func (w *ParquetWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	
	w.writeBatch()
}

// Close writes any remaining FODs and closes the file
func (w *ParquetWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	
	if !w.isOpen {
		return nil
	}
	
	// Write any remaining FODs
	w.writeBatch()
	
	// Close the Parquet writer properly
	if err := w.parquetWriter.WriteStop(); err != nil {
		log.Printf("Warning: failed to finalize Parquet file: %v", err)
	}
	
	// Close the file writer
	if err := w.fileWriter.Close(); err != nil {
		log.Printf("Warning: failed to close Parquet file: %v", err)
	}
	
	w.isOpen = false
	
	log.Printf("Wrote total of %d FODs to Parquet file: %s", w.totalCount, w.outputPath)
	return nil
}