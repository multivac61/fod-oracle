package main

import (
	"database/sql"
	"fmt"
	"path/filepath"
)

// Writer is an interface for writing FOD data
type Writer interface {
	AddFOD(fod FOD)
	IncrementDrvCount()
	Flush()
	Close() error
}

// GetWriter returns a writer based on the configured output format
func GetWriter(db *sql.DB, revisionID int64, rev string) (Writer, error) {
	switch config.OutputFormat {
	case "sqlite":
		// For SQLite, we just use the DBBatcher
		batcher, err := NewDBBatcher(db, 1000, revisionID)
		if err != nil {
			return nil, fmt.Errorf("failed to create database batcher: %w", err)
		}
		return batcher, nil
		
	case "json":
		// Determine output path for JSON
		outputPath := config.OutputPath
		if outputPath == "" {
			outputPath = fmt.Sprintf("output/%s/fods.json", rev)
		}
		return NewJSONWriter(outputPath, revisionID)
		
	case "csv":
		// Determine output path for CSV
		outputPath := config.OutputPath
		if outputPath == "" {
			outputPath = fmt.Sprintf("output/%s/fods.csv", rev)
		}
		return NewCSVWriter(outputPath, revisionID)
		
	case "parquet":
		// Determine output path for Parquet
		outputPath := config.OutputPath
		if outputPath == "" {
			outputPath = fmt.Sprintf("output/%s/fods.parquet", rev)
		}
		// Make sure it has .parquet extension
		if filepath.Ext(outputPath) != ".parquet" {
			outputPath += ".parquet"
		}
		return NewParquetWriter(outputPath, revisionID)
		
	default:
		// Default to SQLite
		batcher, err := NewDBBatcher(db, 1000, revisionID)
		if err != nil {
			return nil, fmt.Errorf("failed to create database batcher: %w", err)
		}
		return batcher, nil
	}
}