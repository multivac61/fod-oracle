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
// Uses chunked processing to prevent memory issues with large datasets
func ConvertToJSON(db *sql.DB, outputPath string, revisionID int64) error {
	log.Printf("Converting to JSON format with chunked processing: %s", outputPath)

	// Create directory if it doesn't exist
	dir := filepath.Dir(outputPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Create and open the output file
	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer file.Close()

	// Write the opening bracket for the JSON array
	if _, err := file.WriteString("[\n"); err != nil {
		return fmt.Errorf("failed to write JSON opening bracket: %w", err)
	}

	// Define the chunk size for processing
	const chunkSize = 5000
	var totalFODs int
	var isFirstChunk = true

	// Count total FODs to provide progress updates
	var totalCount int64
	countQuery := ""
	countArgs := []interface{}{}
	
	if config.IsNixExpr {
		countQuery = `SELECT COUNT(*) FROM fods`
	} else {
		countQuery = `SELECT COUNT(*) FROM fods f
			JOIN drv_revisions dr ON f.drv_path = dr.drv_path
			WHERE dr.revision_id = ?`
		countArgs = append(countArgs, revisionID)
	}
	
	if err := db.QueryRow(countQuery, countArgs...).Scan(&totalCount); err != nil {
		log.Printf("Warning: Failed to count total FODs: %v", err)
	} else {
		log.Printf("Processing %d FODs in chunks of %d", totalCount, chunkSize)
	}

	// Process in chunks using LIMIT and OFFSET
	offset := 0
	for {
		// Build the query for this chunk
		var query string
		var args []interface{}
		
		if config.IsNixExpr {
			query = `
				SELECT f.drv_path, f.output_path, f.hash_algorithm, f.hash,
					r.status as rebuild_status, r.actual_hash,
					CASE WHEN r.status = 'hash_mismatch' THEN 1 ELSE 0 END as hash_mismatch,
					r.error_message
				FROM fods f
				LEFT JOIN rebuild_queue r ON f.drv_path = r.drv_path
				ORDER BY f.drv_path
				LIMIT ? OFFSET ?
			`
			args = []interface{}{chunkSize, offset}
		} else {
			query = `
				SELECT f.drv_path, f.output_path, f.hash_algorithm, f.hash,
					r.status as rebuild_status, r.actual_hash,
					CASE WHEN r.status = 'hash_mismatch' THEN 1 ELSE 0 END as hash_mismatch,
					r.error_message
				FROM fods f
				JOIN drv_revisions dr ON f.drv_path = dr.drv_path
				LEFT JOIN rebuild_queue r ON f.drv_path = r.drv_path AND r.revision_id = ?
				WHERE dr.revision_id = ?
				ORDER BY f.drv_path
				LIMIT ? OFFSET ?
			`
			args = []interface{}{revisionID, revisionID, chunkSize, offset}
		}

		// Execute the query for this chunk
		rows, err := db.Query(query, args...)
		if err != nil {
			return fmt.Errorf("failed to query FODs (offset %d): %w", offset, err)
		}

		// Process the rows in this chunk
		chunkFODs := make([]FODWithRebuild, 0, chunkSize)
		
		for rows.Next() {
			var fod FODWithRebuild
			var hashMismatchInt int
			var rebuildStatus, actualHash, errorMessage sql.NullString

			err := rows.Scan(
				&fod.DrvPath, &fod.OutputPath, &fod.HashAlgorithm, &fod.Hash,
				&rebuildStatus, &actualHash, &hashMismatchInt, &errorMessage,
			)
			if err != nil {
				rows.Close()
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

			chunkFODs = append(chunkFODs, fod)
		}
		rows.Close()

		// If no rows were returned, we're done
		if len(chunkFODs) == 0 {
			break
		}

		// Encode this chunk to JSON
		encoder := json.NewEncoder(file)
		encoder.SetIndent("  ", "  ")
		encoder.SetEscapeHTML(false)

		// Write each FOD as a separate JSON object, with proper commas between items
		for i, fod := range chunkFODs {
			// Don't add a comma before the first item in the first chunk
			if !(isFirstChunk && i == 0) {
				if _, err := file.WriteString(",\n"); err != nil {
					return fmt.Errorf("failed to write JSON separator: %w", err)
				}
			}
			
			// Marshal the individual FOD
			fodJSON, err := json.MarshalIndent(fod, "  ", "  ")
			if err != nil {
				return fmt.Errorf("failed to marshal FOD: %w", err)
			}
			
			// Write the FOD JSON
			if _, err := file.Write(fodJSON); err != nil {
				return fmt.Errorf("failed to write FOD JSON: %w", err)
			}
		}

		// Update progress
		totalFODs += len(chunkFODs)
		if totalCount > 0 {
			progress := float64(totalFODs) / float64(totalCount) * 100
			log.Printf("JSON conversion progress: %.1f%% (%d/%d FODs)", progress, totalFODs, totalCount)
		}

		// Prepare for the next chunk
		offset += chunkSize
		isFirstChunk = false
	}

	// Write the closing bracket for the JSON array
	if _, err := file.WriteString("\n]\n"); err != nil {
		return fmt.Errorf("failed to write JSON closing bracket: %w", err)
	}

	log.Printf("Successfully wrote %d FODs to JSON file: %s", totalFODs, outputPath)
	return nil
}

// ConvertToCSV converts the in-memory database to a CSV file
// Uses chunked processing to prevent memory issues with large datasets
func ConvertToCSV(db *sql.DB, outputPath string, revisionID int64) error {
	log.Printf("Converting to CSV format with chunked processing: %s", outputPath)

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

	// Define the chunk size for processing
	const chunkSize = 5000
	var totalFODs int

	// Count total FODs to provide progress updates
	var totalCount int64
	countQuery := ""
	countArgs := []interface{}{}
	
	if config.IsNixExpr {
		countQuery = `SELECT COUNT(*) FROM fods`
	} else {
		countQuery = `SELECT COUNT(*) FROM fods f
			JOIN drv_revisions dr ON f.drv_path = dr.drv_path
			WHERE dr.revision_id = ?`
		countArgs = append(countArgs, revisionID)
	}
	
	if err := db.QueryRow(countQuery, countArgs...).Scan(&totalCount); err != nil {
		log.Printf("Warning: Failed to count total FODs: %v", err)
	} else {
		log.Printf("Processing %d FODs in chunks of %d", totalCount, chunkSize)
	}

	// Process in chunks using LIMIT and OFFSET
	offset := 0
	for {
		// Build the query for this chunk
		var query string
		var args []interface{}
		
		if config.IsNixExpr {
			query = `
				SELECT f.drv_path, f.output_path, f.hash_algorithm, f.hash,
					r.status as rebuild_status, r.actual_hash,
					CASE WHEN r.status = 'hash_mismatch' THEN 1 ELSE 0 END as hash_mismatch,
					r.error_message
				FROM fods f
				LEFT JOIN rebuild_queue r ON f.drv_path = r.drv_path
				ORDER BY f.drv_path
				LIMIT ? OFFSET ?
			`
			args = []interface{}{chunkSize, offset}
		} else {
			query = `
				SELECT f.drv_path, f.output_path, f.hash_algorithm, f.hash,
					r.status as rebuild_status, r.actual_hash,
					CASE WHEN r.status = 'hash_mismatch' THEN 1 ELSE 0 END as hash_mismatch,
					r.error_message
				FROM fods f
				JOIN drv_revisions dr ON f.drv_path = dr.drv_path
				LEFT JOIN rebuild_queue r ON f.drv_path = r.drv_path AND r.revision_id = ?
				WHERE dr.revision_id = ?
				ORDER BY f.drv_path
				LIMIT ? OFFSET ?
			`
			args = []interface{}{revisionID, revisionID, chunkSize, offset}
		}

		// Execute the query for this chunk
		rows, err := db.Query(query, args...)
		if err != nil {
			return fmt.Errorf("failed to query FODs (offset %d): %w", offset, err)
		}

		// Process each row in this chunk
		var chunkCount int
		for rows.Next() {
			var drvPath, outputPath, hashAlgo, hash string
			var rebuildStatus, actualHash, errorMessage sql.NullString
			var hashMismatchInt int

			err := rows.Scan(
				&drvPath, &outputPath, &hashAlgo, &hash,
				&rebuildStatus, &actualHash, &hashMismatchInt, &errorMessage,
			)
			if err != nil {
				rows.Close()
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
				rows.Close()
				return fmt.Errorf("failed to write CSV record: %w", err)
			}

			chunkCount++
		}
		rows.Close()
		
		// Flush after each chunk to ensure data is written to disk
		csvWriter.Flush()
		if err := csvWriter.Error(); err != nil {
			return fmt.Errorf("error flushing CSV data: %w", err)
		}

		// If no rows were returned, we're done
		if chunkCount == 0 {
			break
		}

		// Update progress
		totalFODs += chunkCount
		if totalCount > 0 {
			progress := float64(totalFODs) / float64(totalCount) * 100
			log.Printf("CSV conversion progress: %.1f%% (%d/%d FODs)", progress, totalFODs, totalCount)
		}

		// Prepare for the next chunk
		offset += chunkSize
	}

	log.Printf("Successfully wrote %d FODs to CSV file: %s", totalFODs, outputPath)
	return nil
}

// ConvertToParquet converts the in-memory database to a Parquet file
// Uses chunked processing to prevent memory issues with large datasets
func ConvertToParquet(db *sql.DB, outputPath string, revisionID int64) error {
	log.Printf("Converting to Parquet format with chunked processing: %s", outputPath)

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

	// Define the chunk size for processing
	const chunkSize = 5000
	var totalFODs int

	// Count total FODs to provide progress updates
	var totalCount int64
	countQuery := ""
	countArgs := []interface{}{}
	
	if config.IsNixExpr {
		countQuery = `SELECT COUNT(*) FROM fods`
	} else {
		countQuery = `SELECT COUNT(*) FROM fods f
			JOIN drv_revisions dr ON f.drv_path = dr.drv_path
			WHERE dr.revision_id = ?`
		countArgs = append(countArgs, revisionID)
	}
	
	if err := db.QueryRow(countQuery, countArgs...).Scan(&totalCount); err != nil {
		log.Printf("Warning: Failed to count total FODs: %v", err)
	} else {
		log.Printf("Processing %d FODs in chunks of %d", totalCount, chunkSize)
	}

	// Process in chunks using LIMIT and OFFSET
	offset := 0
	for {
		// Build the query for this chunk
		var query string
		var args []interface{}
		
		if config.IsNixExpr {
			query = `
				SELECT f.drv_path, f.output_path, f.hash_algorithm, f.hash,
					r.status as rebuild_status, r.actual_hash,
					CASE WHEN r.status = 'hash_mismatch' THEN 1 ELSE 0 END as hash_mismatch,
					r.error_message
				FROM fods f
				LEFT JOIN rebuild_queue r ON f.drv_path = r.drv_path
				ORDER BY f.drv_path
				LIMIT ? OFFSET ?
			`
			args = []interface{}{chunkSize, offset}
		} else {
			query = `
				SELECT f.drv_path, f.output_path, f.hash_algorithm, f.hash,
					r.status as rebuild_status, r.actual_hash,
					CASE WHEN r.status = 'hash_mismatch' THEN 1 ELSE 0 END as hash_mismatch,
					r.error_message
				FROM fods f
				JOIN drv_revisions dr ON f.drv_path = dr.drv_path
				LEFT JOIN rebuild_queue r ON f.drv_path = r.drv_path AND r.revision_id = ?
				WHERE dr.revision_id = ?
				ORDER BY f.drv_path
				LIMIT ? OFFSET ?
			`
			args = []interface{}{revisionID, revisionID, chunkSize, offset}
		}

		// Execute the query for this chunk
		rows, err := db.Query(query, args...)
		if err != nil {
			// Close the Parquet writer before returning an error
			if finalizeErr := pw.WriteStop(); finalizeErr != nil {
				log.Printf("Warning: Failed to finalize Parquet file: %v", finalizeErr)
			}
			return fmt.Errorf("failed to query FODs (offset %d): %w", offset, err)
		}

		// Process the rows in this chunk
		var chunkCount int
		for rows.Next() {
			var drvPath, outputPath, hashAlgo, hash string
			var rebuildStatus, actualHash, errorMessage sql.NullString
			var hashMismatchInt int

			err := rows.Scan(
				&drvPath, &outputPath, &hashAlgo, &hash,
				&rebuildStatus, &actualHash, &hashMismatchInt, &errorMessage,
			)
			if err != nil {
				rows.Close()
				// Close the Parquet writer before returning an error
				if finalizeErr := pw.WriteStop(); finalizeErr != nil {
					log.Printf("Warning: Failed to finalize Parquet file: %v", finalizeErr)
				}
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
				rows.Close()
				// Close the Parquet writer before returning an error
				if finalizeErr := pw.WriteStop(); finalizeErr != nil {
					log.Printf("Warning: Failed to finalize Parquet file: %v", finalizeErr)
				}
				return fmt.Errorf("failed to write Parquet record: %w", err)
			}

			chunkCount++
		}
		rows.Close()

		// If no rows were returned, we're done
		if chunkCount == 0 {
			break
		}

		// Update progress
		totalFODs += chunkCount
		if totalCount > 0 {
			progress := float64(totalFODs) / float64(totalCount) * 100
			log.Printf("Parquet conversion progress: %.1f%% (%d/%d FODs)", progress, totalFODs, totalCount)
		}

		// Prepare for the next chunk
		offset += chunkSize
	}

	// Finalize the Parquet file
	if err := pw.WriteStop(); err != nil {
		return fmt.Errorf("failed to finalize Parquet file: %w", err)
	}

	log.Printf("Successfully wrote %d FODs to Parquet file: %s", totalFODs, outputPath)
	return nil
}
