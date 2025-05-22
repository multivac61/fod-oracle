package tests

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"

	_ "github.com/mattn/go-sqlite3"
	"github.com/nix-community/go-nix/pkg/derivation"
)

// FOD represents a fixed-output derivation
type FOD struct {
	DrvPath       string
	OutputPath    string
	HashAlgorithm string
	Hash          string
}

func RunHelloDeps() {
	// Get hello derivation path
	helloDrvCmd := exec.Command("nix-instantiate", "<nixpkgs>", "-A", "hello")
	helloDrvOutput, err := helloDrvCmd.Output()
	if err != nil {
		log.Fatalf("Failed to get hello derivation path: %v", err)
	}
	helloDrvPath := strings.TrimSpace(string(helloDrvOutput))
	fmt.Printf("Hello derivation path: %s\n", helloDrvPath)

	// Create temporary directory for database
	tmpDir, err := os.MkdirTemp("", "fod-test")
	if err != nil {
		log.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create SQLite database
	dbPath := fmt.Sprintf("%s/fods.db", tmpDir)
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Create tables
	_, err = db.Exec(`
		CREATE TABLE fods (
			drv_path TEXT PRIMARY KEY,
			output_path TEXT NOT NULL,
			hash_algorithm TEXT NOT NULL,
			hash TEXT NOT NULL
		);
	`)
	if err != nil {
		log.Fatalf("Failed to create tables: %v", err)
	}

	// Process hello dependencies
	processedDrvs := &sync.Map{}
	fodsMutex := &sync.Mutex{}
	fodsFound := []FOD{}

	fmt.Println("Searching for FODs in hello dependencies...")
	processDrvAndDeps(helloDrvPath, processedDrvs, fodsMutex, &fodsFound)

	// Save FODs to database
	for _, fod := range fodsFound {
		_, err := db.Exec(
			"INSERT INTO fods (drv_path, output_path, hash_algorithm, hash) VALUES (?, ?, ?, ?)",
			fod.DrvPath, fod.OutputPath, fod.HashAlgorithm, fod.Hash,
		)
		if err != nil {
			log.Printf("Error inserting FOD %s: %v", fod.DrvPath, err)
		}
	}

	// Print summary
	fmt.Printf("\n=== Summary ===\n")
	fmt.Printf("Total FODs found: %d\n\n", len(fodsFound))

	// Print all FODs found
	fmt.Printf("FODs that hello depends on:\n")
	fmt.Printf("%-50s %-10s %s\n", "Derivation", "Algorithm", "Hash")
	fmt.Printf("%-50s %-10s %s\n", "----------", "---------", "----")

	rows, err := db.Query("SELECT drv_path, hash_algorithm, hash FROM fods ORDER BY drv_path")
	if err != nil {
		log.Fatalf("Failed to query database: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var drvPath, hashAlgo, hash string
		if err := rows.Scan(&drvPath, &hashAlgo, &hash); err != nil {
			log.Printf("Error scanning row: %v", err)
			continue
		}

		// Print just the basename for cleaner output
		parts := strings.Split(drvPath, "/")
		basename := parts[len(parts)-1]

		fmt.Printf("%-50s %-10s %s\n", basename, hashAlgo, hash)
	}
}

// processDrvAndDeps processes a derivation and its dependencies
func processDrvAndDeps(drvPath string, processedDrvs *sync.Map, fodsMutex *sync.Mutex, fodsFound *[]FOD) {
	// Check if already processed
	if _, loaded := processedDrvs.LoadOrStore(drvPath, true); loaded {
		return
	}

	// Open and read the derivation
	file, err := os.Open(drvPath)
	if err != nil {
		log.Printf("Error opening file %s: %v", drvPath, err)
		return
	}
	defer file.Close()

	drv, err := derivation.ReadDerivation(file)
	if err != nil {
		log.Printf("Error reading derivation %s: %v", drvPath, err)
		return
	}

	// Check if this is a FOD
	isFOD := false
	for name, out := range drv.Outputs {
		if out.HashAlgorithm != "" {
			fodsMutex.Lock()
			*fodsFound = append(*fodsFound, FOD{
				DrvPath:       drvPath,
				OutputPath:    out.Path,
				HashAlgorithm: out.HashAlgorithm,
				Hash:          out.Hash,
			})
			fodsMutex.Unlock()
			isFOD = true
			fmt.Printf("Found FOD: %s (output: %s, hash algo: %s)\n", drvPath, name, out.HashAlgorithm)
			break
		}
	}

	if !isFOD {
		// Process input derivations
		for inputDrvPath := range drv.InputDerivations {
			processDrvAndDeps(inputDrvPath, processedDrvs, fodsMutex, fodsFound)
		}
	}
}
