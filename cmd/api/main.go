package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/multivac61/fod-oracle/api"
	_ "github.com/mattn/go-sqlite3"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Println("Starting FOD Oracle API server...")

	// Get database path from environment variable or use default
	dbPath := os.Getenv("FOD_ORACLE_DB_PATH")
	if dbPath == "" {
		// Default to the db directory in the current working directory
		currentDir, err := os.Getwd()
		if err != nil {
			log.Fatalf("Failed to get current directory: %v", err)
		}
		dbPath = filepath.Join(currentDir, "db", "fods.db")
	}

	// Check if the database file exists
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		log.Fatalf("Database file not found at %s. Please run the FOD Oracle CLI to populate the database first.", dbPath)
	}

	// Connect to the database
	log.Printf("Connecting to database at %s", dbPath)
	connString := dbPath + "?_journal_mode=WAL" +
		"&_synchronous=NORMAL" +
		"&_cache_size=100000" +
		"&_temp_store=MEMORY" +
		"&_busy_timeout=10000" +
		"&_locking_mode=NORMAL"

	db, err := sql.Open("sqlite3", connString)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Configure connection pool
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)

	// Test the database connection
	if err := db.Ping(); err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}

	// Create the API server
	server := api.NewServer(db)

	// Get port from environment variable or use default
	port := os.Getenv("FOD_ORACLE_API_PORT")
	if port == "" {
		port = "8080"
	}

	// Start the server
	addr := fmt.Sprintf(":%s", port)
	log.Printf("API server listening on %s", addr)
	if err := http.ListenAndServe(addr, server); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}