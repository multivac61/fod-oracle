package tests

import (
	"database/sql"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// TestHelloPackageFOD tests that the hello package is correctly identified as a FOD
func TestHelloPackageFOD(t *testing.T) {
	// Create test directory
	testDir, err := os.MkdirTemp("", "fod-oracle-test")
	if err != nil {
		t.Fatalf("Failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(testDir)

	// Create a temp database
	dbPath := filepath.Join(testDir, "test.db")
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Create required tables
	createTables := `
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
	_, err = db.Exec(createTables)
	if err != nil {
		t.Fatalf("Failed to create tables: %v", err)
	}

	// Set environment variables for the test
	os.Setenv("FOD_ORACLE_DB_PATH", dbPath)
	os.Setenv("FOD_ORACLE_NUM_WORKERS", "2") // Use minimal workers for test
	
	// Run nix-build to get the derivation path for hello
	cmd := exec.Command("nix-build", "--no-out-link", "<nixpkgs>", "-A", "hello")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to build hello package: %v\nOutput: %s", err, output)
	}
	
	outPath := strings.TrimSpace(string(output))
	t.Logf("Hello package output path: %s", outPath)
	
	// Get the derivation path
	cmd = exec.Command("nix-store", "--query", "--deriver", outPath)
	output, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to get derivation path: %v\nOutput: %s", err, output)
	}
	
	drvPath := strings.TrimSpace(string(output))
	t.Logf("Hello package derivation path: %s", drvPath)
	
	// Get current nixpkgs revision
	cmd = exec.Command("nix-instantiate", "--eval", "--expr", "builtins.nixVersion")
	output, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to get Nix version: %v\nOutput: %s", err, output)
	}
	
	nixVersion := strings.Trim(string(output), "\"\n ")
	t.Logf("Nix version: %s", nixVersion)
	
	// Use the nixVersion as a pseudo-revision
	testRevision := "test-" + nixVersion
	
	// Create a minimal test to run just the processing on the hello derivation
	// This avoids the complexity of the full nix-eval-jobs workflow
	
	// Run the main program with the test revision
	mainCmd := exec.Command("./fod-oracle", testRevision)
	mainCmd.Env = append(os.Environ(), 
		"FOD_ORACLE_DB_PATH="+dbPath,
		"FOD_ORACLE_NUM_WORKERS=2",
		"FOD_ORACLE_TEST_DRV_PATH="+drvPath)
	
	output, err = mainCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to run main program: %v\nOutput: %s", err, output)
	}
	
	t.Logf("Main program output: %s", output)
	
	// Query the database to see if the hello package was correctly identified as a FOD
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM fods WHERE drv_path = ?", drvPath).Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query database: %v", err)
	}
	
	if count == 0 {
		t.Errorf("Hello package was not identified as a FOD")
	} else {
		t.Logf("Hello package was correctly identified as a FOD")
	}
	
	// Also check if the derivation was correctly associated with the revision
	var revisionCount int
	err = db.QueryRow(`
		SELECT COUNT(*) FROM drv_revisions 
		JOIN revisions ON drv_revisions.revision_id = revisions.id
		WHERE revisions.rev = ? AND drv_revisions.drv_path = ?
	`, testRevision, drvPath).Scan(&revisionCount)
	
	if err != nil {
		t.Fatalf("Failed to query database for revision association: %v", err)
	}
	
	if revisionCount == 0 {
		t.Errorf("Hello package was not associated with the test revision")
	} else {
		t.Logf("Hello package was correctly associated with the test revision")
	}
}