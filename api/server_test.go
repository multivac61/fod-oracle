package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func setupTestDB(t *testing.T) *sql.DB {
	// Initialize in-memory SQLite database for testing
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("Failed to open test database: %v", err)
	}

	// Create test tables
	_, err = db.Exec(`
		CREATE TABLE revisions (
			id INTEGER PRIMARY KEY,
			rev TEXT UNIQUE NOT NULL,
			timestamp TEXT NOT NULL
		);
		
		CREATE TABLE fods (
			drv_path TEXT PRIMARY KEY,
			output_path TEXT NOT NULL,
			hash_algorithm TEXT NOT NULL,
			hash TEXT NOT NULL
		);
		
		CREATE TABLE drv_revisions (
			drv_path TEXT NOT NULL,
			revision_id INTEGER NOT NULL,
			PRIMARY KEY (drv_path, revision_id),
			FOREIGN KEY (drv_path) REFERENCES fods (drv_path),
			FOREIGN KEY (revision_id) REFERENCES revisions (id)
		);
	`)
	if err != nil {
		t.Fatalf("Failed to create test tables: %v", err)
	}

	return db
}

func TestHandleGetStats(t *testing.T) {
	// Setup test database
	db := setupTestDB(t)
	defer db.Close()

	// Insert test data
	_, err := db.Exec(`
		INSERT INTO revisions (id, rev, timestamp) VALUES 
		(1, 'abc123', '2025-03-20 17:01:42'),
		(2, 'def456', '2025-03-21 09:15:30');
		
		INSERT INTO fods (drv_path, output_path, hash_algorithm, hash) VALUES
		('/nix/store/a.drv', '/nix/store/a', 'sha256', 'hash1'),
		('/nix/store/b.drv', '/nix/store/b', 'sha256', 'hash2'),
		('/nix/store/c.drv', '/nix/store/c', 'sha256', 'hash2');
		
		INSERT INTO drv_revisions (drv_path, revision_id) VALUES
		('/nix/store/a.drv', 1),
		('/nix/store/b.drv', 1),
		('/nix/store/c.drv', 2),
		('/nix/store/a.drv', 2);
	`)
	if err != nil {
		t.Fatalf("Failed to insert test data: %v", err)
	}

	// Create server and request
	server := NewServer(db)
	req, err := http.NewRequest("GET", "/api/stats", nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	// Create response recorder
	rr := httptest.NewRecorder()

	// Serve the request
	server.ServeHTTP(rr, req)

	// Check response status
	if status := rr.Code; status != http.StatusOK {
		t.Errorf("Handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}

	// Parse response
	var stats Stats
	if err := json.Unmarshal(rr.Body.Bytes(), &stats); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	// Verify response data
	expectedTotalFODs := 3
	if stats.TotalFODs != expectedTotalFODs {
		t.Errorf("Incorrect total FODs: got %v want %v", stats.TotalFODs, expectedTotalFODs)
	}

	expectedTotalRevisions := 2
	if stats.TotalRevisions != expectedTotalRevisions {
		t.Errorf("Incorrect total revisions: got %v want %v", stats.TotalRevisions, expectedTotalRevisions)
	}

	expectedUniqueHashes := 2
	if stats.UniqueHashes != expectedUniqueHashes {
		t.Errorf("Incorrect unique hashes: got %v want %v", stats.UniqueHashes, expectedUniqueHashes)
	}

	// Check LastUpdated time was parsed correctly
	expectedTime := time.Date(2025, 3, 21, 9, 15, 30, 0, time.UTC)
	if !stats.LastUpdated.Equal(expectedTime) {
		t.Errorf("Incorrect last updated time: got %v want %v", stats.LastUpdated, expectedTime)
	}
}