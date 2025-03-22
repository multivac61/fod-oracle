package tests

// Importing helper functions from db_test.go
// setupTestDB is defined in db_test.go

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"
)

// TestMetadataStorage tests the evaluation metadata storage
func TestMetadataStorage(t *testing.T) {
	db, cleanup := SetupTestDB(t)
	defer cleanup()

	// Insert a test revision
	testRev := "test-metadata-revision"
	result, err := db.Exec("INSERT INTO revisions (rev) VALUES (?)", testRev)
	if err != nil {
		t.Fatalf("Failed to insert test revision: %v", err)
	}
	revisionID, _ := result.LastInsertId()

	// Create mock evaluation stats
	stats := map[string]struct {
		exists           bool
		attempted        bool
		succeeded        bool
		errorMessage     string
		derivationsFound int
	}{
		"path1": {true, true, true, "", 50},
		"path2": {true, true, false, "evaluation error", 0},
		"path3": {true, false, false, "", 0},
		"path4": {false, false, false, "", 0},
	}

	// Store metadata for the revision
	storeErr := mockStoreEvaluationMetadata(
		db, revisionID, stats, "/test/nixpkgs",
		10*time.Second, true, 500,
	)
	if storeErr != nil {
		t.Fatalf("Failed to store evaluation metadata: %v", storeErr)
	}

	// Test that metadata was stored correctly
	
	// 1. Check evaluation_metadata entries
	rows, err := db.Query("SELECT file_path, file_exists, attempted, succeeded, error_message, derivations_found FROM evaluation_metadata WHERE revision_id = ?", revisionID)
	if err != nil {
		t.Fatalf("Failed to query evaluation metadata: %v", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		count++
		var filePath string
		var fileExists, attempted, succeeded int
		var errorMsg string
		var derivationsFound int
		
		if err := rows.Scan(&filePath, &fileExists, &attempted, &succeeded, &errorMsg, &derivationsFound); err != nil {
			t.Fatalf("Failed to scan metadata row: %v", err)
		}
		
		// Verify the values match our mock data
		mockStat, ok := stats[filePath]
		if !ok {
			t.Errorf("Got unexpected file_path: %s", filePath)
			continue
		}
		
		expectedExists := 0
		if mockStat.exists {
			expectedExists = 1
		}
		if fileExists != expectedExists {
			t.Errorf("For %s, expected file_exists=%d, got %d", filePath, expectedExists, fileExists)
		}
		
		expectedAttempted := 0
		if mockStat.attempted {
			expectedAttempted = 1
		}
		if attempted != expectedAttempted {
			t.Errorf("For %s, expected attempted=%d, got %d", filePath, expectedAttempted, attempted)
		}
		
		expectedSucceeded := 0
		if mockStat.succeeded {
			expectedSucceeded = 1
		}
		if succeeded != expectedSucceeded {
			t.Errorf("For %s, expected succeeded=%d, got %d", filePath, expectedSucceeded, succeeded)
		}
		
		if errorMsg != mockStat.errorMessage {
			t.Errorf("For %s, expected error_message=%q, got %q", filePath, mockStat.errorMessage, errorMsg)
		}
		
		if derivationsFound != mockStat.derivationsFound {
			t.Errorf("For %s, expected derivations_found=%d, got %d", filePath, mockStat.derivationsFound, derivationsFound)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("Error iterating metadata rows: %v", err)
	}
	if count != len(stats) {
		t.Errorf("Expected %d metadata rows, got %d", len(stats), count)
	}

	// 2. Check revision_stats entry
	var totalFound, totalAttempted, totalSucceeded, totalDerivations, fallbackUsed, procTime, workers, memPeak int
	var sysInfo, hostName, cpuModel, memTotal, kernelVer, osName string
	var cpuCores int
	
	err = db.QueryRow(`
		SELECT 
			total_expressions_found, total_expressions_attempted,
			total_expressions_succeeded, total_derivations_found,
			fallback_used, processing_time_seconds, worker_count, memory_mb_peak,
			system_info, host_name, cpu_model, cpu_cores, memory_total, kernel_version, os_name
		FROM revision_stats
		WHERE revision_id = ?
	`, revisionID).Scan(
		&totalFound, &totalAttempted, &totalSucceeded, &totalDerivations,
		&fallbackUsed, &procTime, &workers, &memPeak,
		&sysInfo, &hostName, &cpuModel, &cpuCores, &memTotal, &kernelVer, &osName,
	)
	if err != nil {
		t.Fatalf("Failed to query revision stats: %v", err)
	}

	// Check the values are as expected
	if totalFound != 3 { // We had 3 paths with exists=true
		t.Errorf("Expected total_expressions_found=3, got %d", totalFound)
	}
	if totalAttempted != 2 { // We had 2 paths with attempted=true
		t.Errorf("Expected total_expressions_attempted=2, got %d", totalAttempted)
	}
	if totalSucceeded != 1 { // We had 1 path with succeeded=true
		t.Errorf("Expected total_expressions_succeeded=1, got %d", totalSucceeded)
	}
	if totalDerivations != 50 { // We had 50 derivations total
		t.Errorf("Expected total_derivations_found=50, got %d", totalDerivations)
	}
	if fallbackUsed != 1 { // We set usedFallback=true
		t.Errorf("Expected fallback_used=1, got %d", fallbackUsed)
	}
	if procTime != 10 { // We set processingTime=10s
		t.Errorf("Expected processing_time_seconds=10, got %d", procTime)
	}
	if memPeak != 500 { // We set peakMemory=500MB
		t.Errorf("Expected memory_mb_peak=500, got %d", memPeak)
	}

	// Check that system_info is valid JSON
	var sysInfoObj map[string]interface{}
	if err := json.Unmarshal([]byte(sysInfo), &sysInfoObj); err != nil {
		t.Errorf("system_info is not valid JSON: %v", err)
	}
}

// Mock implementation of storeEvaluationMetadata for testing
func mockStoreEvaluationMetadata(
	db *sql.DB,
	revisionID int64,
	stats map[string]struct {
		exists           bool
		attempted        bool
		succeeded        bool
		errorMessage     string
		derivationsFound int
	},
	nixpkgsDir string,
	processingTime time.Duration,
	usedFallback bool,
	peakMemoryMB int,
) error {
	// Start a transaction
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Insert metadata for each file
	for path, stat := range stats {
		fileExists := 0
		if stat.exists {
			fileExists = 1
		}
		
		attempted := 0
		if stat.attempted {
			attempted = 1
		}
		
		succeeded := 0
		if stat.succeeded {
			succeeded = 1
		}

		_, err := tx.Exec(`
			INSERT INTO evaluation_metadata (
				revision_id, file_path, file_exists, attempted,
				succeeded, error_message, derivations_found
			) VALUES (?, ?, ?, ?, ?, ?, ?)
		`, revisionID, path, fileExists, attempted, succeeded, stat.errorMessage, stat.derivationsFound)
		if err != nil {
			return err
		}
	}

	// Count totals for revision stats
	var totalFound, totalAttempted, totalSucceeded, totalDerivations int
	for _, stat := range stats {
		if stat.exists {
			totalFound++
		}
		if stat.attempted {
			totalAttempted++
		}
		if stat.succeeded {
			totalSucceeded++
		}
		totalDerivations += stat.derivationsFound
	}

	// Create mock system info
	sysInfoObj := map[string]string{
		"Host":     "test-host",
		"CPU":      "Test CPU Model",
		"CPU Cores": "8 (16)",
		"Memory":   "32GB",
		"Kernel":   "test-kernel",
		"OS":       "test-os",
	}
	sysInfoBytes, _ := json.Marshal(sysInfoObj)
	sysInfoJSON := string(sysInfoBytes)

	// Insert revision stats
	fallbackInt := 0
	if usedFallback {
		fallbackInt = 1
	}

	_, err = tx.Exec(`
		INSERT INTO revision_stats (
			revision_id, total_expressions_found, total_expressions_attempted,
			total_expressions_succeeded, total_derivations_found,
			fallback_used, processing_time_seconds, worker_count, memory_mb_peak,
			system_info, host_name, cpu_model, cpu_cores, memory_total, kernel_version, os_name
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		revisionID, totalFound, totalAttempted, totalSucceeded, totalDerivations,
		fallbackInt, int(processingTime.Seconds()), 16, peakMemoryMB,
		sysInfoJSON, sysInfoObj["Host"], sysInfoObj["CPU"], 16, sysInfoObj["Memory"], sysInfoObj["Kernel"], sysInfoObj["OS"],
	)
	if err != nil {
		return err
	}

	return tx.Commit()
}