package main

import (
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"

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
	// Rebuild information fields
	RebuildStatus string `parquet:"name=rebuild_status, type=BYTE_ARRAY, convertedtype=UTF8, repetitiontype=OPTIONAL"`
	ActualHash    string `parquet:"name=actual_hash, type=BYTE_ARRAY, convertedtype=UTF8, repetitiontype=OPTIONAL"`
	HashMismatch  bool   `parquet:"name=hash_mismatch, type=BOOLEAN, repetitiontype=OPTIONAL"`
	ErrorMessage  string `parquet:"name=error_message, type=BYTE_ARRAY, convertedtype=UTF8, repetitiontype=OPTIONAL"`
}

// ConvertToFormat converts the in-memory database to the specified output format
func ConvertToFormat(db *sql.DB, format string, outputPath string, revisionID int64) error {
	switch format {
	case "sqlite":
		// For SQLite, nothing to do - data is already in the database
		return nil
	case "json":
		return ConvertToJSON(db, outputPath, revisionID)
	case "csv":
		return ConvertToCSV(db, outputPath, revisionID)
	case "parquet":
		return ConvertToParquet(db, outputPath, revisionID)
	default:
		return fmt.Errorf("unsupported output format: %s", format)
	}
}

// ConvertToJSON converts the in-memory database to a JSON file
func ConvertToJSON(db *sql.DB, outputPath string, revisionID int64) error {
	log.Printf("Converting to JSON format: %s", outputPath)
	
	// Create directory if it doesn't exist
	dir := filepath.Dir(outputPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	
	// Query the database for FODs with rebuild info
	var rows *sql.Rows
	var err error
	
	if config.IsNixExpr {
		// For Nix expressions, get all FODs
		rows, err = db.Query(`
			SELECT f.drv_path, f.output_path, f.hash_algorithm, f.hash,
				   r.status as rebuild_status, r.actual_hash,
				   CASE WHEN r.status = 'hash_mismatch' THEN 1 ELSE 0 END as hash_mismatch,
				   r.error_message
			FROM fods f
			LEFT JOIN rebuild_queue r ON f.drv_path = r.drv_path
			ORDER BY f.drv_path
		`)
	} else {
		// For regular revisions, get FODs associated with this revision
		rows, err = db.Query(`
			SELECT f.drv_path, f.output_path, f.hash_algorithm, f.hash,
				   r.status as rebuild_status, r.actual_hash,
				   CASE WHEN r.status = 'hash_mismatch' THEN 1 ELSE 0 END as hash_mismatch,
				   r.error_message
			FROM fods f
			JOIN drv_revisions dr ON f.drv_path = dr.drv_path
			LEFT JOIN rebuild_queue r ON f.drv_path = r.drv_path AND r.revision_id = ?
			WHERE dr.revision_id = ?
			ORDER BY f.drv_path
		`, revisionID, revisionID)
	}
	
	if err != nil {
		return fmt.Errorf("failed to query FODs: %w", err)
	}
	defer rows.Close()
	
	// Build array of FODs with rebuild info
	var fodsWithRebuild []FODWithRebuild
	for rows.Next() {
		var fod FODWithRebuild
		var hashMismatchInt int
		var rebuildStatus, actualHash, errorMessage sql.NullString
		
		err := rows.Scan(
			&fod.DrvPath, &fod.OutputPath, &fod.HashAlgorithm, &fod.Hash,
			&rebuildStatus, &actualHash, &hashMismatchInt, &errorMessage,
		)
		if err != nil {
			return fmt.Errorf("failed to scan row: %w", err)
		}
		
		// Handle nullable fields
		if rebuildStatus.Valid {
			fod.RebuildStatus = rebuildStatus.String
		}
		if actualHash.Valid {
			fod.ActualHash = actualHash.String
		}
		if errorMessage.Valid {
			fod.ErrorMessage = errorMessage.String
		}
		
		fod.HashMismatch = hashMismatchInt == 1
		
		// Add revision ID if this is not a Nix expression
		if !config.IsNixExpr {
			fod.RevisionID = revisionID
		}
		
		fodsWithRebuild = append(fodsWithRebuild, fod)
	}
	
	// Write to JSON file
	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer file.Close()
	
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	
	if err := encoder.Encode(fodsWithRebuild); err != nil {
		return fmt.Errorf("failed to encode JSON: %w", err)
	}
	
	log.Printf("Successfully wrote %d FODs to JSON file: %s", len(fodsWithRebuild), outputPath)
	return nil
}

// ConvertToCSV converts the in-memory database to a CSV file
func ConvertToCSV(db *sql.DB, outputPath string, revisionID int64) error {
	log.Printf("Converting to CSV format: %s", outputPath)
	
	// Create directory if it doesn't exist
	dir := filepath.Dir(outputPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	
	// Create the CSV file
	file, err := os.Create(outputPath)
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
		header = []string{"drv_path", "output_path", "hash_algorithm", "hash", "rebuild_status", "actual_hash", "hash_mismatch", "error_message"}
	} else {
		header = []string{"drv_path", "output_path", "hash_algorithm", "hash", "revision_id", "rebuild_status", "actual_hash", "hash_mismatch", "error_message"}
	}
	
	if err := csvWriter.Write(header); err != nil {
		return fmt.Errorf("failed to write CSV header: %w", err)
	}
	
	// Query the database for FODs with rebuild info
	var rows *sql.Rows
	
	if config.IsNixExpr {
		// For Nix expressions, get all FODs
		rows, err = db.Query(`
			SELECT f.drv_path, f.output_path, f.hash_algorithm, f.hash,
				   r.status as rebuild_status, r.actual_hash,
				   CASE WHEN r.status = 'hash_mismatch' THEN 1 ELSE 0 END as hash_mismatch,
				   r.error_message
			FROM fods f
			LEFT JOIN rebuild_queue r ON f.drv_path = r.drv_path
			ORDER BY f.drv_path
		`)
	} else {
		// For regular revisions, get FODs associated with this revision
		rows, err = db.Query(`
			SELECT f.drv_path, f.output_path, f.hash_algorithm, f.hash,
				   r.status as rebuild_status, r.actual_hash,
				   CASE WHEN r.status = 'hash_mismatch' THEN 1 ELSE 0 END as hash_mismatch,
				   r.error_message
			FROM fods f
			JOIN drv_revisions dr ON f.drv_path = dr.drv_path
			LEFT JOIN rebuild_queue r ON f.drv_path = r.drv_path AND r.revision_id = ?
			WHERE dr.revision_id = ?
			ORDER BY f.drv_path
		`, revisionID, revisionID)
	}
	
	if err != nil {
		return fmt.Errorf("failed to query FODs: %w", err)
	}
	defer rows.Close()
	
	// Write each row
	var count int
	for rows.Next() {
		var drvPath, outputPath, hashAlgo, hash string
		var rebuildStatus, actualHash, errorMessage sql.NullString
		var hashMismatchInt int
		
		err := rows.Scan(
			&drvPath, &outputPath, &hashAlgo, &hash,
			&rebuildStatus, &actualHash, &hashMismatchInt, &errorMessage,
		)
		if err != nil {
			return fmt.Errorf("failed to scan row: %w", err)
		}
		
		// Prepare the record
		var record []string
		
		// Basic fields
		if config.IsNixExpr {
			record = []string{drvPath, outputPath, hashAlgo, hash}
		} else {
			record = []string{drvPath, outputPath, hashAlgo, hash, strconv.FormatInt(revisionID, 10)}
		}
		
		// Rebuild info fields with null handling
		rebuildStatusStr := ""
		if rebuildStatus.Valid {
			rebuildStatusStr = rebuildStatus.String
		}
		
		actualHashStr := ""
		if actualHash.Valid {
			actualHashStr = actualHash.String
		}
		
		hashMismatchStr := "false"
		if hashMismatchInt == 1 {
			hashMismatchStr = "true"
		}
		
		errorMessageStr := ""
		if errorMessage.Valid {
			errorMessageStr = errorMessage.String
		}
		
		record = append(record, rebuildStatusStr, actualHashStr, hashMismatchStr, errorMessageStr)
		
		// Write the record
		if err := csvWriter.Write(record); err != nil {
			return fmt.Errorf("failed to write CSV record: %w", err)
		}
		
		count++
	}
	
	log.Printf("Successfully wrote %d FODs to CSV file: %s", count, outputPath)
	return nil
}

// ConvertToParquet converts the in-memory database to a Parquet file
func ConvertToParquet(db *sql.DB, outputPath string, revisionID int64) error {
	log.Printf("Converting to Parquet format: %s", outputPath)
	
	// Create directory if it doesn't exist
	dir := filepath.Dir(outputPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	
	// Make sure output path has .parquet extension
	if filepath.Ext(outputPath) != ".parquet" {
		outputPath += ".parquet"
	}
	
	// Create Parquet file
	fw, err := local.NewLocalFileWriter(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create Parquet file: %w", err)
	}
	defer fw.Close()
	
	// Create Parquet writer
	pw, err := writer.NewParquetWriter(fw, new(ParquetFOD), 4)
	if err != nil {
		return fmt.Errorf("failed to create Parquet writer: %w", err)
	}
	
	// Set compression and other options
	pw.CompressionType = parquet.CompressionCodec_SNAPPY
	pw.PageSize = 32 * 1024            // 32K page size for better compression
	pw.RowGroupSize = 16 * 1024 * 1024 // 16MB row groups
	
	// Query the database for FODs with rebuild info
	var rows *sql.Rows
	
	if config.IsNixExpr {
		// For Nix expressions, get all FODs
		rows, err = db.Query(`
			SELECT f.drv_path, f.output_path, f.hash_algorithm, f.hash,
				   r.status as rebuild_status, r.actual_hash,
				   CASE WHEN r.status = 'hash_mismatch' THEN 1 ELSE 0 END as hash_mismatch,
				   r.error_message
			FROM fods f
			LEFT JOIN rebuild_queue r ON f.drv_path = r.drv_path
			ORDER BY f.drv_path
		`)
	} else {
		// For regular revisions, get FODs associated with this revision
		rows, err = db.Query(`
			SELECT f.drv_path, f.output_path, f.hash_algorithm, f.hash,
				   r.status as rebuild_status, r.actual_hash,
				   CASE WHEN r.status = 'hash_mismatch' THEN 1 ELSE 0 END as hash_mismatch,
				   r.error_message
			FROM fods f
			JOIN drv_revisions dr ON f.drv_path = dr.drv_path
			LEFT JOIN rebuild_queue r ON f.drv_path = r.drv_path AND r.revision_id = ?
			WHERE dr.revision_id = ?
			ORDER BY f.drv_path
		`, revisionID, revisionID)
	}
	
	if err != nil {
		return fmt.Errorf("failed to query FODs: %w", err)
	}
	defer rows.Close()
	
	// Write each row to the Parquet file
	var count int
	for rows.Next() {
		var drvPath, outputPath, hashAlgo, hash string
		var rebuildStatus, actualHash, errorMessage sql.NullString
		var hashMismatchInt int
		
		err := rows.Scan(
			&drvPath, &outputPath, &hashAlgo, &hash,
			&rebuildStatus, &actualHash, &hashMismatchInt, &errorMessage,
		)
		if err != nil {
			return fmt.Errorf("failed to scan row: %w", err)
		}
		
		// Create Parquet record
		parquetFod := ParquetFOD{
			DrvPath:       drvPath,
			OutputPath:    outputPath,
			HashAlgorithm: hashAlgo,
			Hash:          hash,
		}
		
		// Add revision ID if this is not a Nix expression
		if !config.IsNixExpr {
			parquetFod.RevisionID = revisionID
		}
		
		// Add rebuild info with null handling
		if rebuildStatus.Valid {
			parquetFod.RebuildStatus = rebuildStatus.String
		}
		
		if actualHash.Valid {
			parquetFod.ActualHash = actualHash.String
		}
		
		parquetFod.HashMismatch = hashMismatchInt == 1
		
		if errorMessage.Valid {
			parquetFod.ErrorMessage = errorMessage.String
		}
		
		// Write the record
		if err := pw.Write(parquetFod); err != nil {
			return fmt.Errorf("failed to write Parquet record: %w", err)
		}
		
		count++
	}
	
	// Finalize the Parquet file
	if err := pw.WriteStop(); err != nil {
		return fmt.Errorf("failed to finalize Parquet file: %w", err)
	}
	
	log.Printf("Successfully wrote %d FODs to Parquet file: %s", count, outputPath)
	return nil
}