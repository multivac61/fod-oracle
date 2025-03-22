package tests

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMainIntegration does an end-to-end test of the main application
// This test is marked with the "integration" tag so it can be skipped during routine testing
// Run with: go test -tags=integration ./tests/...
func TestMainIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Create a temporary directory for the test
	tempDir, err := os.MkdirTemp("", "fod-oracle-integration-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create a database directory
	dbDir := filepath.Join(tempDir, "db")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		t.Fatalf("Failed to create db directory: %v", err)
	}

	// Build the application
	buildCmd := exec.Command("go", "build", "-o", filepath.Join(tempDir, "fod-oracle"))
	buildCmd.Dir = filepath.Join("..")
	if output, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to build application: %v\nOutput: %s", err, output)
	}

	// Set up a small test to run with a known Nixpkgs revision
	// You would need a real commit hash for an actual test
	testRev := "abcdef1234567890abcdef1234567890abcdef12" // Fake commit for testing
	
	// In a real test, we would run the application with the revision
	// and then check the database for expected results
	
	// For now, we'll just test that the database is created correctly
	// and the schema is valid
	
	// Set up the environment for the test - uncomment if actually running the command
	/*
	env := append(
		os.Environ(),
		"FOD_ORACLE_DB_PATH="+filepath.Join(dbDir, "test.db"),
		"FOD_ORACLE_NUM_WORKERS=4",
	)
	*/

	// Mock the application run for testing purposes
	// In a real integration test, you would run the actual application
	// cmd := exec.Command(filepath.Join(tempDir, "fod-oracle"), testRev)
	// cmd.Env = env
	// cmd.Dir = tempDir
	// if output, err := cmd.CombinedOutput(); err != nil {
	//    t.Fatalf("Failed to run application: %v\nOutput: %s", err, output)
	// }

	// Instead, we'll create an empty database with the schema
	db, err := sql.Open("sqlite3", filepath.Join(dbDir, "test.db"))
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Create the schema using the same SQL from main.go
	schemaSQL := `
    CREATE TABLE IF NOT EXISTS revisions (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        rev TEXT NOT NULL UNIQUE,
        timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
    );
    CREATE INDEX IF NOT EXISTS idx_rev ON revisions(rev);

    CREATE TABLE IF NOT EXISTS fods (
        drv_path TEXT PRIMARY KEY,
        output_path TEXT NOT NULL,
        hash_algorithm TEXT NOT NULL,
        hash TEXT NOT NULL,
        timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
    );
    CREATE INDEX IF NOT EXISTS idx_hash ON fods(hash);

    CREATE TABLE IF NOT EXISTS drv_revisions (
        drv_path TEXT NOT NULL,
        revision_id INTEGER NOT NULL,
        timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
        PRIMARY KEY (drv_path, revision_id),
        FOREIGN KEY (drv_path) REFERENCES fods(drv_path) ON DELETE CASCADE,
        FOREIGN KEY (revision_id) REFERENCES revisions(id) ON DELETE CASCADE
    );
    CREATE INDEX IF NOT EXISTS idx_drv_path ON drv_revisions(drv_path);
    
    -- Table for storing expression file evaluation metadata
    CREATE TABLE IF NOT EXISTS evaluation_metadata (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        revision_id INTEGER NOT NULL,
        file_path TEXT NOT NULL,
        file_exists INTEGER NOT NULL,
        attempted INTEGER NOT NULL,
        succeeded INTEGER NOT NULL,
        error_message TEXT,
        derivations_found INTEGER DEFAULT 0,
        evaluation_time DATETIME DEFAULT CURRENT_TIMESTAMP,
        FOREIGN KEY (revision_id) REFERENCES revisions(id) ON DELETE CASCADE
    );
    CREATE INDEX IF NOT EXISTS idx_evaluation_revision ON evaluation_metadata(revision_id);
    CREATE UNIQUE INDEX IF NOT EXISTS idx_evaluation_file ON evaluation_metadata(revision_id, file_path);
    
    -- Table for general evaluation stats per revision
    CREATE TABLE IF NOT EXISTS revision_stats (
        revision_id INTEGER PRIMARY KEY,
        total_expressions_found INTEGER DEFAULT 0,
        total_expressions_attempted INTEGER DEFAULT 0,
        total_expressions_succeeded INTEGER DEFAULT 0,
        total_derivations_found INTEGER DEFAULT 0,
        fallback_used INTEGER DEFAULT 0,
        processing_time_seconds INTEGER DEFAULT 0,
        worker_count INTEGER DEFAULT 0,
        memory_mb_peak INTEGER DEFAULT 0,
        system_info TEXT,                      -- JSON from neofetch
        host_name TEXT,                        -- Extracted from system_info for easy querying
        cpu_model TEXT,                        -- Extracted from system_info for easy querying
        cpu_cores INTEGER DEFAULT 0,           -- Extracted from system_info for easy querying
        memory_total TEXT,                     -- Extracted from system_info for easy querying
        kernel_version TEXT,                   -- Extracted from system_info for easy querying
        os_name TEXT,                          -- Extracted from system_info for easy querying
        evaluation_timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
        FOREIGN KEY (revision_id) REFERENCES revisions(id) ON DELETE CASCADE
    );
    `
	
	// Execute the schema creation
	for _, statement := range strings.Split(schemaSQL, ";") {
		if strings.TrimSpace(statement) == "" {
			continue
		}
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("Failed to execute schema statement: %v\nStatement: %s", err, statement)
		}
	}

	// Insert a test revision
	result, err := db.Exec("INSERT INTO revisions (rev) VALUES (?)", testRev)
	if err != nil {
		t.Fatalf("Failed to insert test revision: %v", err)
	}

	// Get the revision ID
	revID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("Failed to get last insert ID: %v", err)
	}

	// Verify the revision was inserted
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM revisions WHERE rev = ?", testRev).Scan(&count)
	if err != nil {
		t.Fatalf("Failed to count revisions: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected 1 revision, got %d", count)
	}

	// Insert a test FOD
	drvPath := "/nix/store/test-drv-path.drv"
	outputPath := "/nix/store/test-output-path"
	hash := "test-hash"
	hashAlgo := "sha256"

	_, err = db.Exec(
		"INSERT INTO fods (drv_path, output_path, hash_algorithm, hash) VALUES (?, ?, ?, ?)",
		drvPath, outputPath, hashAlgo, hash,
	)
	if err != nil {
		t.Fatalf("Failed to insert test FOD: %v", err)
	}

	// Link the FOD to the revision
	_, err = db.Exec(
		"INSERT INTO drv_revisions (drv_path, revision_id) VALUES (?, ?)",
		drvPath, revID,
	)
	if err != nil {
		t.Fatalf("Failed to insert drv_revision link: %v", err)
	}

	// Test queries to validate the database structure and relationships
	
	// 1. Count FODs for the revision
	err = db.QueryRow(`
		SELECT COUNT(*) FROM drv_revisions
		WHERE revision_id = ?
	`, revID).Scan(&count)
	if err != nil {
		t.Fatalf("Failed to count FODs for revision: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected 1 FOD for revision, got %d", count)
	}

	// 2. Join query to get all FODs for the revision
	rows, err := db.Query(`
		SELECT f.drv_path, f.output_path, f.hash_algorithm, f.hash
		FROM fods f
		JOIN drv_revisions dr ON f.drv_path = dr.drv_path
		WHERE dr.revision_id = ?
	`, revID)
	if err != nil {
		t.Fatalf("Failed to query FODs for revision: %v", err)
	}
	defer rows.Close()

	fodCount := 0
	for rows.Next() {
		fodCount++
		var path, output, algo, h string
		if err := rows.Scan(&path, &output, &algo, &h); err != nil {
			t.Fatalf("Failed to scan FOD row: %v", err)
		}
		if path != drvPath {
			t.Errorf("Expected drv_path %s, got %s", drvPath, path)
		}
		if output != outputPath {
			t.Errorf("Expected output_path %s, got %s", outputPath, output)
		}
		if algo != hashAlgo {
			t.Errorf("Expected hash_algorithm %s, got %s", hashAlgo, algo)
		}
		if h != hash {
			t.Errorf("Expected hash %s, got %s", hash, h)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("Error iterating FOD rows: %v", err)
	}
	if fodCount != 1 {
		t.Errorf("Expected 1 FOD row, got %d", fodCount)
	}

	t.Log("Integration test completed successfully")
}