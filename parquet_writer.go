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

// ParquetWriter handles writing FODs to a Parquet file
type ParquetWriter struct {
	outputPath string
	fods       []ParquetFOD
	revisionID int64
	mu         sync.Mutex
}

// NewParquetWriter creates a new Parquet writer
func NewParquetWriter(outputPath string, revisionID int64) (*ParquetWriter, error) {
	// Create directory if it doesn't exist
	dir := filepath.Dir(outputPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory: %w", err)
	}

	return &ParquetWriter{
		outputPath: outputPath,
		fods:       make([]ParquetFOD, 0),
		revisionID: revisionID,
	}, nil
}

// AddFOD adds a FOD to the Parquet file
func (w *ParquetWriter) AddFOD(fod FOD) {
	w.mu.Lock()
	defer w.mu.Unlock()
	
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
	
	if len(w.fods)%1000 == 0 {
		log.Printf("Collected %d FODs for Parquet file", len(w.fods))
	}
}

// IncrementDrvCount is a no-op for Parquet but implemented for compatibility
func (w *ParquetWriter) IncrementDrvCount() {
	// No-op for Parquet writer
}

// Flush is a no-op for Parquet but implemented for compatibility
func (w *ParquetWriter) Flush() {
	// Will write on Close
}

// Close writes the collected FODs to a Parquet file
func (w *ParquetWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	
	if len(w.fods) == 0 {
		log.Printf("No FODs to write to Parquet file")
		return nil
	}
	
	// Create Parquet file using the local file source
	fw, err := local.NewLocalFileWriter(w.outputPath)
	if err != nil {
		return fmt.Errorf("failed to create Parquet file: %w", err)
	}
	defer fw.Close()
	
	// Define Parquet writer with proper schema
	pw, err := writer.NewParquetWriter(fw, new(ParquetFOD), 4)
	if err != nil {
		return fmt.Errorf("failed to create Parquet writer: %w", err)
	}
	
	// Set compression and other options
	pw.CompressionType = parquet.CompressionCodec_SNAPPY
	pw.PageSize = 32 * 1024          // 32K page size for better compression
	pw.RowGroupSize = 16 * 1024 * 1024 // 16MB row groups (smaller is better for this data size)
	
	// Write data
	for i, fod := range w.fods {
		if err := pw.Write(fod); err != nil {
			return fmt.Errorf("failed to write FOD #%d to Parquet: %w", i, err)
		}
	}
	
	// Close writer
	if err := pw.WriteStop(); err != nil {
		return fmt.Errorf("failed to finalize Parquet file: %w", err)
	}
	
	log.Printf("Wrote %d FODs to Parquet file: %s", len(w.fods), w.outputPath)
	return nil
}