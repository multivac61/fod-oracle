package main

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

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

// DrvRevision represents a relationship between a derivation path and a revision
type DrvRevision struct {
	DrvPath    string
	RevisionID int64
}

// initDB initializes the SQLite database
func initDB() *sql.DB {
	// Create the directory if it doesn't exist
	dbDir := "./db"
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		log.Fatalf("Failed to create database directory: %v", err)
	}

	dbPath := filepath.Join(dbDir, "fods.db")
	log.Printf("Using database at: %s", dbPath)

	// Open database with optimized settings
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_synchronous=NORMAL&_cache_size=100000&_temp_store=MEMORY")
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}

	// First set foreign keys explicitly
	if _, err := db.Exec("PRAGMA foreign_keys = ON;"); err != nil {
		log.Printf("Error enabling foreign keys: %v", err)
	}

	// Then verify it was set
	var fkEnabled int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&fkEnabled); err != nil {
		log.Printf("Error checking foreign_keys pragma: %v", err)
	} else {
		log.Printf("Foreign keys enabled: %v", fkEnabled == 1)
		if fkEnabled != 1 {
			log.Println("WARNING: Foreign keys are not enabled! This will cause referential integrity issues.")
			log.Println("Try opening a new connection or check your SQLite version.")
		}
	}

	// Set connection pool settings
	db.SetMaxOpenConns(runtime.NumCPU() * 2)
	db.SetMaxIdleConns(runtime.NumCPU())
	db.SetConnMaxLifetime(time.Hour)

	// Set pragmas for better performance
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA cache_size=100000",
		"PRAGMA temp_store=MEMORY",
		"PRAGMA mmap_size=30000000000",
		"PRAGMA page_size=32768",
		"PRAGMA foreign_keys=ON", // Enable foreign key constraints
	}

	// Platform-specific optimizations
	if runtime.GOOS == "darwin" {
		// macOS-specific settings
		pragmas = append(pragmas, "PRAGMA temp_store=FILE") // Use file instead of memory for temp storage on macOS
		// Adjust mmap size for macOS
		pragmas = append(pragmas, "PRAGMA mmap_size=8000000000") // Use a smaller mmap size on macOS
	}

	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			log.Printf("Warning: Failed to set pragma %s: %v", pragma, err)
		}
	}

	// Create tables with the new schema
	createTables := `
    -- Revisions table
    CREATE TABLE IF NOT EXISTS revisions (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        rev TEXT NOT NULL UNIQUE,
        timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
    );
    CREATE INDEX IF NOT EXISTS idx_rev ON revisions(rev);

    -- FODs table (without revision_id)
    CREATE TABLE IF NOT EXISTS fods (
        drv_path TEXT PRIMARY KEY,
        output_path TEXT NOT NULL,
        hash_algorithm TEXT NOT NULL,
        hash TEXT NOT NULL,
        timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
    );
    CREATE INDEX IF NOT EXISTS idx_hash ON fods(hash);
    CREATE INDEX IF NOT EXISTS idx_hash_algo ON fods(hash_algorithm);

    -- Relation table between drv_paths and revisions
    CREATE TABLE IF NOT EXISTS drv_revisions (
        drv_path TEXT NOT NULL,
        revision_id INTEGER NOT NULL,
        timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
        PRIMARY KEY (drv_path, revision_id),
        FOREIGN KEY (drv_path) REFERENCES fods(drv_path) ON DELETE CASCADE,
        FOREIGN KEY (revision_id) REFERENCES revisions(id) ON DELETE CASCADE
    );
    CREATE INDEX IF NOT EXISTS idx_drv_path ON drv_revisions(drv_path);
    CREATE INDEX IF NOT EXISTS idx_revision_id ON drv_revisions(revision_id);
    `

	_, err = db.Exec(createTables)
	if err != nil {
		log.Fatalf("Failed to create tables: %v", err)
	}

	return db
}

// DBBatcher handles batched database operations
type DBBatcher struct {
	db            *sql.DB
	fodBatch      []FOD
	relationBatch []DrvRevision
	batchSize     int
	mutex         sync.Mutex
	commitTicker  *time.Ticker
	wg            sync.WaitGroup
	done          chan struct{}
	fodStmt       *sql.Stmt
	relStmt       *sql.Stmt
	revisionID    int64 // Store the current revision ID
	stats         struct {
		drvs int
		fods int
		sync.Mutex
	}
}

// NewDBBatcher creates a new database batcher
func NewDBBatcher(db *sql.DB, batchSize int, commitInterval time.Duration, revisionID int64) (*DBBatcher, error) {
	// Prepare FOD statement
	fodStmt, err := db.Prepare(`
        INSERT INTO fods (drv_path, output_path, hash_algorithm, hash)
        VALUES (?, ?, ?, ?)
        ON CONFLICT(drv_path) DO UPDATE SET
        output_path = ?,
        hash_algorithm = ?,
        hash = ?
    `)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare FOD statement: %w", err)
	}

	// Prepare relation statement
	relStmt, err := db.Prepare(`
        INSERT INTO drv_revisions (drv_path, revision_id)
        VALUES (?, ?)
        ON CONFLICT(drv_path, revision_id) DO NOTHING
    `)
	if err != nil {
		fodStmt.Close()
		return nil, fmt.Errorf("failed to prepare relation statement: %w", err)
	}

	batcher := &DBBatcher{
		db:            db,
		fodBatch:      make([]FOD, 0, batchSize),
		relationBatch: make([]DrvRevision, 0, batchSize),
		batchSize:     batchSize,
		commitTicker:  time.NewTicker(commitInterval),
		done:          make(chan struct{}),
		fodStmt:       fodStmt,
		relStmt:       relStmt,
		revisionID:    revisionID,
	}

	batcher.wg.Add(1)
	go batcher.periodicCommit()

	return batcher, nil
}

// periodicCommit commits batches periodically
func (b *DBBatcher) periodicCommit() {
	defer b.wg.Done()

	for {
		select {
		case <-b.commitTicker.C:
			b.Flush()
			b.logStats()
		case <-b.done:
			b.Flush()
			b.logStats()
			return
		}
	}
}

// logStats logs the current statistics
func (b *DBBatcher) logStats() {
	b.stats.Lock()
	defer b.stats.Unlock()
	log.Printf("Stats: processed %d derivations, found %d FODs", b.stats.drvs, b.stats.fods)
}

// AddFOD adds a FOD to the batch
func (b *DBBatcher) AddFOD(fod FOD) {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	b.fodBatch = append(b.fodBatch, fod)

	// Also add to relation batch
	relation := DrvRevision{
		DrvPath:    fod.DrvPath,
		RevisionID: b.revisionID,
	}
	b.relationBatch = append(b.relationBatch, relation)

	b.stats.Lock()
	b.stats.fods++
	b.stats.Unlock()

	if len(b.fodBatch) >= b.batchSize {
		b.commitBatch()
	}
}

// IncrementDrvCount increments the derivation count (for stats only)
func (b *DBBatcher) IncrementDrvCount() {
	b.stats.Lock()
	b.stats.drvs++
	b.stats.Unlock()
}

// commitBatch commits the current batch
func (b *DBBatcher) commitBatch() {
	if len(b.fodBatch) == 0 {
		return
	}

	tx, err := b.db.Begin()
	if err != nil {
		log.Printf("Failed to begin transaction: %v", err)
		return
	}

	// Prepare for rollback in case of error
	defer func() {
		if err != nil {
			tx.Rollback()
			log.Printf("Transaction rolled back: %v", err)
		}
	}()

	fodStmt := tx.Stmt(b.fodStmt)
	relStmt := tx.Stmt(b.relStmt)

	// First, insert all FODs
	for _, fod := range b.fodBatch {
		_, err = fodStmt.Exec(
			fod.DrvPath, fod.OutputPath, fod.HashAlgorithm, fod.Hash,
			fod.OutputPath, fod.HashAlgorithm, fod.Hash,
		)
		if err != nil {
			log.Printf("Failed to insert FOD %s: %v", fod.DrvPath, err)
			// Continue with other FODs even if one fails
		}
	}

	// Then, insert all relations after FODs are inserted
	for _, rel := range b.relationBatch {
		_, err = relStmt.Exec(rel.DrvPath, rel.RevisionID)
		if err != nil {
			log.Printf("Failed to insert relation for %s: %v", rel.DrvPath, err)
			// Continue with other relations even if one fails
		}
	}

	// Commit the transaction
	if err = tx.Commit(); err != nil {
		log.Printf("Failed to commit transaction: %v", err)
		return
	}

	b.fodBatch = b.fodBatch[:0]
	b.relationBatch = b.relationBatch[:0]
}

// Flush commits all pending batches
func (b *DBBatcher) Flush() {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	b.commitBatch()
}

// Close closes the batcher
func (b *DBBatcher) Close() error {
	close(b.done)
	b.commitTicker.Stop()
	b.wg.Wait()

	if err := b.fodStmt.Close(); err != nil {
		return err
	}

	if err := b.relStmt.Close(); err != nil {
		return err
	}

	return nil
}

// processDerivation processes a derivation
func processDerivation(inputFile string, batcher *DBBatcher, visited *sync.Map, workQueue chan<- string) {
	// Increment derivation count for statistics
	batcher.IncrementDrvCount()

	file, err := os.Open(inputFile)
	if err != nil {
		log.Printf("Error opening file %s: %v", inputFile, err)
		return
	}
	defer file.Close()

	drv, err := derivation.ReadDerivation(file)
	if err != nil {
		log.Printf("Error reading derivation %s: %v", inputFile, err)
		return
	}

	// Find output hash and store FODs in the database
	for name, out := range drv.Outputs {
		if out.HashAlgorithm != "" {
			// Now we know it's a FOD, store it in the database
			fod := FOD{
				DrvPath:       inputFile,
				OutputPath:    out.Path,
				HashAlgorithm: out.HashAlgorithm,
				Hash:          out.Hash,
			}
			batcher.AddFOD(fod)

			// If we're in verbose mode, log the FOD
			if os.Getenv("VERBOSE") == "1" {
				log.Printf("Found FOD: %s (output: %s, hash: %s)",
					filepath.Base(inputFile), name, out.Hash)
			}

			// Since FODs typically have only one output, we can break after finding one
			break
		}
	}

	// Process input derivations
	for path := range drv.InputDerivations {
		// Only process if not already visited
		if _, alreadyVisited := visited.LoadOrStore(path, true); !alreadyVisited {
			select {
			case workQueue <- path:
				// Successfully added to queue
			default:
				// Queue is full, process it directly to avoid deadlock
				go processDerivation(path, batcher, visited, workQueue)
			}
		}
	}
}

// getOrCreateRevision gets or creates a revision in the database
func getOrCreateRevision(db *sql.DB, rev string) (int64, error) {
	var id int64

	// Check if revision already exists
	err := db.QueryRow("SELECT id FROM revisions WHERE rev = ?", rev).Scan(&id)
	if err == nil {
		// Revision already exists
		return id, nil
	} else if err != sql.ErrNoRows {
		// Unexpected error
		return 0, fmt.Errorf("error checking for existing revision: %w", err)
	}

	// Revision doesn't exist, create it
	result, err := db.Exec("INSERT INTO revisions (rev) VALUES (?)", rev)
	if err != nil {
		return 0, fmt.Errorf("failed to insert revision: %w", err)
	}

	id, err = result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get last insert ID: %w", err)
	}

	return id, nil
}

// Enhanced version using Git worktrees for multiple simultaneous revisions
func callNixEvalJobsWithGitWorktree(rev string, workQueue chan<- string, visited *sync.Map) error {
	// Main repository directory
	mainRepoDir := "./nixpkgs-repo"

	// Worktree directory for this specific revision
	worktreeDir := fmt.Sprintf("./nixpkgs-worktree-%s", rev)

	// Create a mutex for Git operations to prevent concurrent Git commands from interfering
	gitMutex := &sync.Mutex{}

	// Acquire the mutex for Git operations
	gitMutex.Lock()
	defer gitMutex.Unlock()

	// Check if the main repo directory already exists
	if _, err := os.Stat(mainRepoDir); os.IsNotExist(err) {
		// Create a fresh shallow clone with bare repository
		log.Printf("Creating shallow bare clone of nixpkgs repository...")
		cmd := exec.Command("git", "clone", "--depth=1", "--bare", "https://github.com/NixOS/nixpkgs.git", mainRepoDir)

		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to clone bare repository: %w", err)
		}
	}

	// Fetch the specific revision if it's not already available
	log.Printf("Fetching revision %s...", rev)
	cmd := exec.Command("git", "-C", mainRepoDir, "fetch", "--depth=1", "origin", rev)
	if err := cmd.Run(); err != nil {
		log.Fatal("git fetch failed")
	}

	// Remove existing worktree if it exists
	if _, err := os.Stat(worktreeDir); err == nil {
		log.Printf("Removing existing worktree at %s", worktreeDir)

		// First try to remove it via git worktree
		pruneCmd := exec.Command("git", "-C", mainRepoDir, "worktree", "prune")
		pruneCmd.Run() // Ignore errors, just try to clean up

		// Then remove the directory
		if err := os.RemoveAll(worktreeDir); err != nil {
			return fmt.Errorf("failed to remove existing worktree directory: %w", err)
		}
	}

	// Create a new worktree for this revision
	log.Printf("Creating worktree for revision %s at %s", rev, worktreeDir)
	cmd = exec.Command("git", "-C", mainRepoDir, "worktree", "add", "--detach", worktreeDir, rev)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to create worktree: %w", err)
	}

	// Release the mutex before the long-running nix-eval-jobs operation
	gitMutex.Unlock()

	// Now run nix-eval-jobs with the worktree
	expr := fmt.Sprintf("import %s { allowAliases = false; }", worktreeDir)
	cmd = exec.Command("nix-eval-jobs", "--expr", expr, "--workers", "8")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	log.Printf("Starting nix-eval-jobs with nixpkgs worktree for revision %s", rev)
	err = cmd.Start()
	if err != nil {
		return fmt.Errorf("failed to start nix-eval-jobs: %w", err)
	}

	// Process the output
	scanner := bufio.NewScanner(stdout)
	const maxScannerSize = 10 * 1024 * 1024 // 10MB
	buf := make([]byte, maxScannerSize)
	scanner.Buffer(buf, maxScannerSize)

	for scanner.Scan() {
		line := scanner.Text()
		var result struct {
			DrvPath string `json:"drvPath"`
		}
		if err := json.Unmarshal([]byte(line), &result); err != nil {
			log.Printf("Failed to parse JSON: %v", err)
			continue
		}
		if result.DrvPath != "" {
			if _, alreadyVisited := visited.LoadOrStore(result.DrvPath, true); !alreadyVisited {
				workQueue <- result.DrvPath
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading stdout: %w", err)
	}

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("nix-eval-jobs command failed: %w", err)
	}

	return nil
}

// verifyDatabase checks if data was properly inserted
func verifyDatabase(db *sql.DB, revisionID int64) {
	// Check FODs table
	var fodCount int
	err := db.QueryRow("SELECT COUNT(*) FROM fods").Scan(&fodCount)
	if err != nil {
		log.Printf("Error counting FODs: %v", err)
	} else {
		log.Printf("Total FODs in database: %d", fodCount)
	}

	// Check relations table
	var relCount int
	err = db.QueryRow("SELECT COUNT(*) FROM drv_revisions WHERE revision_id = ?", revisionID).Scan(&relCount)
	if err != nil {
		log.Printf("Error counting relations: %v", err)
	} else {
		log.Printf("Relations for current revision: %d", relCount)
	}

	// Check if counts match
	if fodCount != relCount {
		log.Printf("WARNING: FOD count (%d) doesn't match relation count (%d)", fodCount, relCount)

		// Check for orphaned relations
		var orphanedCount int
		err = db.QueryRow(`
			SELECT COUNT(*) FROM drv_revisions dr
			WHERE NOT EXISTS (SELECT 1 FROM fods f WHERE f.drv_path = dr.drv_path)
		`).Scan(&orphanedCount)
		if err != nil {
			log.Printf("Error checking for orphaned relations: %v", err)
		} else if orphanedCount > 0 {
			log.Printf("Found %d orphaned relations", orphanedCount)
		}
	}

	// Sample some data
	rows, err := db.Query(`
		SELECT f.drv_path, f.hash_algorithm, f.hash
		FROM fods f JOIN drv_revisions dr ON f.drv_path = dr.drv_path
		WHERE dr.revision_id = ? LIMIT 5
	`, revisionID)
	if err != nil {
		log.Printf("Error sampling data: %v", err)
		return
	}
	defer rows.Close()

	log.Println("Sample FODs from database:")
	for rows.Next() {
		var drvPath, hashAlgo, hash string
		if err := rows.Scan(&drvPath, &hashAlgo, &hash); err != nil {
			log.Printf("Error scanning row: %v", err)
			continue
		}
		log.Printf("  - %s: %s:%s", filepath.Base(drvPath), hashAlgo, hash)
	}
}

// prepareNixpkgsWorktree ensures the nixpkgs repository is available and creates a worktree for a specific revision
func prepareNixpkgsWorktree(rev string) (string, error) {
	// Validate the commit hash
	if len(rev) != 40 {
		return "", fmt.Errorf("invalid commit hash length: %s (expected 40 characters)", rev)
	}
	for _, c := range rev {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return "", fmt.Errorf("invalid commit hash format: %s (expected hexadecimal)", rev)
		}
	}

	// Get absolute paths for directories
	scriptDir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get current directory: %w", err)
	}

	// Main repository directory
	mainRepoDir := filepath.Join(scriptDir, "nixpkgs-repo")
	worktreeDir := filepath.Join(scriptDir, fmt.Sprintf("nixpkgs-worktree-%s", rev))
	repoURL := "https://github.com/NixOS/nixpkgs.git"

	log.Printf("Repository directory: %s", mainRepoDir)
	log.Printf("Worktree directory: %s", worktreeDir)

	// FIRST: Always try to remove the worktree if it exists
	if _, err := os.Stat(worktreeDir); err == nil {
		log.Printf("Directory %s already exists", worktreeDir)

		// Check if Git knows about this worktree
		if _, err := os.Stat(mainRepoDir); err == nil {
			listCmd := exec.Command("git", "-C", mainRepoDir, "worktree", "list")
			listOutput, _ := listCmd.Output()
			log.Printf("Current worktrees:\n%s", string(listOutput))

			if strings.Contains(string(listOutput), worktreeDir) {
				log.Printf("Removing worktree from Git's perspective...")
				removeCmd := exec.Command("git", "-C", mainRepoDir, "worktree", "remove", "--force", worktreeDir)
				removeCmd.Run() // Ignore errors
			}
		}

		// Prune any stale worktrees
		pruneCmd := exec.Command("git", "-C", mainRepoDir, "worktree", "prune")
		pruneCmd.Run() // Ignore errors

		// Regardless of whether Git knew about it, remove the directory
		log.Printf("Forcibly removing directory...")
		if err := os.RemoveAll(worktreeDir); err != nil {
			log.Printf("Warning: Failed to remove directory: %v", err)
			// Try with a more aggressive approach
			rmCmd := exec.Command("rm", "-rf", worktreeDir)
			if err := rmCmd.Run(); err != nil {
				return "", fmt.Errorf("failed to remove existing worktree directory: %w", err)
			}
		}

		// Double-check that it's gone
		if _, err := os.Stat(worktreeDir); err == nil {
			return "", fmt.Errorf("failed to remove existing worktree directory %s", worktreeDir)
		}

		log.Printf("Successfully removed existing worktree directory")
	}

	// Check if the main repo directory exists and is valid
	repoExists := false
	if _, err := os.Stat(mainRepoDir); err == nil {
		// Check if it's a valid git repository
		checkCmd := exec.Command("git", "-C", mainRepoDir, "rev-parse", "--is-bare-repository")
		if output, err := checkCmd.Output(); err == nil && string(output) == "true\n" {
			repoExists = true
			log.Printf("Using existing bare repository at %s", mainRepoDir)

			// Prune any stale worktrees
			pruneCmd := exec.Command("git", "-C", mainRepoDir, "worktree", "prune")
			pruneCmd.Run() // Ignore errors

			// List current worktrees
			listCmd := exec.Command("git", "-C", mainRepoDir, "worktree", "list")
			listOutput, _ := listCmd.Output()
			log.Printf("Current worktrees:\n%s", string(listOutput))
		} else {
			log.Printf("Existing directory is not a valid bare repository, will recreate")
			repoExists = false
		}
	}

	// If repository doesn't exist or is invalid, remove it and create a new one
	if !repoExists {
		// Remove the existing directory if it exists
		if _, err := os.Stat(mainRepoDir); err == nil {
			log.Printf("Removing invalid repository at %s", mainRepoDir)
			if err := os.RemoveAll(mainRepoDir); err != nil {
				return "", fmt.Errorf("failed to remove existing repository directory: %w", err)
			}
		}

		// Create a fresh bare clone
		log.Printf("Creating bare clone of nixpkgs repository...")
		cmd := exec.Command("git", "clone", "--bare", "--filter=blob:none", repoURL, mainRepoDir)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("failed to clone bare repository: %w", err)
		}
	}

	// Fetch the specific revision
	log.Printf("Fetching revision %s...", rev)
	fetchCmd := exec.Command("git", "-C", mainRepoDir, "fetch", "--depth=1", "origin", rev)
	fetchCmd.Stdout = os.Stdout
	fetchCmd.Stderr = os.Stderr

	if err := fetchCmd.Run(); err != nil {
		// Try again without depth limit if it fails
		log.Printf("Retrying fetch without depth limit...")
		fetchCmd = exec.Command("git", "-C", mainRepoDir, "fetch", "origin", rev)
		fetchCmd.Stdout = os.Stdout
		fetchCmd.Stderr = os.Stderr

		if err := fetchCmd.Run(); err != nil {
			return "", fmt.Errorf("failed to fetch revision: %w", err)
		}
	}

	// Double-check the directory doesn't exist before creating worktree
	if _, err := os.Stat(worktreeDir); err == nil {
		return "", fmt.Errorf("directory %s still exists despite cleanup", worktreeDir)
	}

	// Create a worktree for this revision
	log.Printf("Creating worktree for revision %s at %s", rev, worktreeDir)
	worktreeCmd := exec.Command("git", "-C", mainRepoDir, "worktree", "add", "--detach", worktreeDir, rev)
	worktreeCmd.Stdout = os.Stdout
	worktreeCmd.Stderr = os.Stderr

	if err := worktreeCmd.Run(); err != nil {
		return "", fmt.Errorf("failed to create worktree: %w", err)
	}

	// Immediately check if the directory exists
	if _, err := os.Stat(worktreeDir); err != nil {
		log.Printf("ERROR: Worktree directory was not created")

		// Check what Git thinks about the worktrees
		listCmd := exec.Command("git", "-C", mainRepoDir, "worktree", "list")
		listOutput, _ := listCmd.Output()
		log.Printf("Git worktree list:\n%s", string(listOutput))

		// Try to find the worktree in a different location
		findCmd := exec.Command("find", scriptDir, "-type", "d", "-name", fmt.Sprintf("nixpkgs-worktree-*"))
		findOutput, _ := findCmd.Output()
		log.Printf("Found worktree directories:\n%s", string(findOutput))

		return "", fmt.Errorf("worktree directory was not created")
	}

	// Verify critical files exist
	criticalFiles := []string{
		"default.nix",
		"pkgs/top-level/all-packages.nix",
		"lib/default.nix",
	}

	// Wait for the worktree to be fully populated
	maxRetries := 10
	retryDelay := 1 * time.Second

	for i := 0; i < maxRetries; i++ {
		allFilesExist := true

		for _, file := range criticalFiles {
			filePath := filepath.Join(worktreeDir, file)
			if _, err := os.Stat(filePath); err != nil {
				allFilesExist = false
				log.Printf("Waiting for file %s to be available (attempt %d/%d)", file, i+1, maxRetries)
				break
			}
		}

		if allFilesExist {
			log.Printf("Worktree is fully populated after %d attempts", i+1)

			// List all worktrees to see what Git knows about
			listCmd := exec.Command("git", "-C", mainRepoDir, "worktree", "list")
			listOutput, _ := listCmd.Output()
			log.Printf("Current worktrees:\n%s", string(listOutput))

			return worktreeDir, nil
		}

		if i == maxRetries-1 {
			// List directory contents to help diagnose the issue
			log.Printf("Directory contents of %s:", worktreeDir)
			lsCmd := exec.Command("ls", "-la", worktreeDir)
			lsCmd.Stdout = os.Stdout
			lsCmd.Stderr = os.Stderr
			lsCmd.Run() // Ignore errors

			return "", fmt.Errorf("worktree appears incomplete after %d attempts", maxRetries)
		}

		log.Printf("Waiting for worktree to be fully populated (attempt %d/%d)...", i+1, maxRetries)
		time.Sleep(retryDelay)
	}

	return worktreeDir, nil
}

// findFODsForRevision processes a revision to find all FODs
// findFODsForRevision processes a revision to find all FODs
func findFODsForRevision(rev string, revisionID int64, db *sql.DB) error {
	revStartTime := time.Now()
	log.Printf("[%s] Starting to find all FODs...", rev)

	// Create batcher for this revision
	batcher, err := NewDBBatcher(db, 5000, 3*time.Second, revisionID)
	if err != nil {
		return fmt.Errorf("failed to create database batcher: %w", err)
	}
	defer batcher.Close()

	// Create a shared visited map and work queue for this revision
	visited := &sync.Map{}
	workQueue := make(chan string, 100000) // Large buffer to avoid blocking

	// Prepare the worktree first, before starting any workers
	log.Printf("[%s] Preparing worktree...", rev)
	worktreeDir, err := prepareNixpkgsWorktree(rev)
	if err != nil {
		return fmt.Errorf("failed to prepare worktree: %w", err)
	}
	defer func() {
		// Clean up the worktree
		log.Printf("[%s] Cleaning up worktree at %s", rev, worktreeDir)

		// First prune the worktree reference
		pruneCmd := exec.Command("git", "-C", "./nixpkgs-repo", "worktree", "prune")
		pruneCmd.Run() // Ignore errors

		// Then remove the directory
		if err := os.RemoveAll(worktreeDir); err != nil {
			log.Printf("[%s] Warning: Failed to remove worktree directory: %v", rev, err)
		}
	}()

	// Verify the worktree is ready by checking for critical files
	log.Printf("[%s] Verifying worktree is ready...", rev)
	criticalFiles := []string{
		"default.nix",
		"pkgs/top-level/all-packages.nix",
		"lib/default.nix",
	}
	for _, file := range criticalFiles {
		path := filepath.Join(worktreeDir, file)
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("critical file %s not found in worktree: %w", file, err)
		}
		log.Printf("[%s] Verified file exists: %s", rev, file)
	}

	// Now that the worktree is ready, start worker goroutines
	numWorkers := runtime.NumCPU()
	log.Printf("[%s] Starting %d worker goroutines", rev, numWorkers)

	var workerWg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		workerWg.Add(1)
		go func(workerID int) {
			defer workerWg.Done()
			log.Printf("[%s] Worker %d started", rev, workerID)
			for drvPath := range workQueue {
				processDerivation(drvPath, batcher, visited, workQueue)
			}
			log.Printf("[%s] Worker %d finished", rev, workerID)
		}(i)
	}

	// Run nix-eval-jobs with the worktree
	log.Printf("[%s] Running nix-eval-jobs with worktree at %s", rev, worktreeDir)
	expr := fmt.Sprintf("import %s { allowAliases = false; }", worktreeDir)
	cmd := exec.Command("nix-eval-jobs",
		"--expr", expr,
		"--workers", fmt.Sprintf("%d", runtime.NumCPU()),
		"--max-memory-size", "8192")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	cmd.Stderr = os.Stderr

	log.Printf("[%s] Starting nix-eval-jobs process...", rev)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start nix-eval-jobs: %w", err)
	}

	// Process the output
	log.Printf("[%s] Processing nix-eval-jobs output...", rev)
	scanner := bufio.NewScanner(stdout)
	const maxScannerSize = 10 * 1024 * 1024 // 10MB
	buf := make([]byte, maxScannerSize)
	scanner.Buffer(buf, maxScannerSize)

	jobCount := 0
	for scanner.Scan() {
		line := scanner.Text()
		var result struct {
			DrvPath string `json:"drvPath"`
		}
		if err := json.Unmarshal([]byte(line), &result); err != nil {
			log.Printf("[%s] Failed to parse JSON: %v", rev, err)
			continue
		}
		if result.DrvPath != "" {
			jobCount++
			if jobCount%1000 == 0 {
				log.Printf("[%s] Processed %d jobs from nix-eval-jobs", rev, jobCount)
			}
			if _, alreadyVisited := visited.LoadOrStore(result.DrvPath, true); !alreadyVisited {
				select {
				case workQueue <- result.DrvPath:
					// Successfully added to queue
				default:
					// Queue is full, log and continue
					log.Printf("[%s] Warning: Work queue full, skipping derivation %s", rev, result.DrvPath)
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading stdout: %w", err)
	}

	log.Printf("[%s] nix-eval-jobs completed with %d jobs, waiting for command to exit...", rev, jobCount)
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("nix-eval-jobs command failed: %w", err)
	}

	// Wait a bit to ensure all jobs are processed
	log.Printf("[%s] Waiting for workers to finish processing...", rev)

	// Check if there are still jobs being processed
	for {
		time.Sleep(1 * time.Second)

		// Get current stats
		batcher.stats.Lock()
		currentDrvs := batcher.stats.drvs
		currentFods := batcher.stats.fods
		batcher.stats.Unlock()

		log.Printf("[%s] Current progress: processed %d derivations, found %d FODs",
			rev, currentDrvs, currentFods)

		time.Sleep(2 * time.Second)

		batcher.stats.Lock()
		newDrvs := batcher.stats.drvs
		batcher.stats.Unlock()

		// If no new derivations were processed in 2 seconds, we're done
		if newDrvs == currentDrvs {
			log.Printf("[%s] No new derivations processed in 2 seconds, closing work queue", rev)
			close(workQueue)
			break
		}
	}

	// Wait for all workers to finish
	log.Printf("[%s] Waiting for all workers to complete...", rev)
	workerWg.Wait()

	// Ensure all data is written
	log.Printf("[%s] Flushing database...", rev)
	batcher.Flush()

	// Print final statistics
	revElapsed := time.Since(revStartTime)
	log.Printf("[%s] Process completed in %v", rev, revElapsed)

	var fodCount int
	if err := db.QueryRow(`
        SELECT COUNT(*) FROM drv_revisions WHERE revision_id = ?
    `, revisionID).Scan(&fodCount); err != nil {
		log.Printf("[%s] Error counting FODs: %v", rev, err)
	}

	log.Printf("[%s] Final database stats: %d FODs", rev, fodCount)
	log.Printf("[%s] Average processing rate: %.2f derivations/second",
		rev, float64(batcher.stats.drvs)/revElapsed.Seconds())

	return nil
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Println("Starting FOD finder...")

	// Initialize database
	db := initDB()
	defer db.Close()

	// Get revisions from command line arguments
	if len(os.Args) < 2 {
		log.Fatalf("Usage: %s <nixpkgs-revision> [<nixpkgs-revision2> ...]", os.Args[0])
	}
	revisions := os.Args[1:]
	log.Printf("Processing %d nixpkgs revisions: %v", len(revisions), revisions)

	// Start the process
	startTime := time.Now()

	// Process each revision in parallel
	var wg sync.WaitGroup
	for _, rev := range revisions {
		wg.Add(1)
		go func(revision string) {
			defer wg.Done()

			// Get or create the revision in the database
			revisionID, err := getOrCreateRevision(db, revision)
			if err != nil {
				log.Printf("Failed to get or create revision %s: %v", revision, err)
				return
			}
			log.Printf("Using nixpkgs revision: %s (ID: %d)", revision, revisionID)

			// Find all FODs for this revision
			if err := findFODsForRevision(revision, revisionID, db); err != nil {
				log.Printf("Error finding FODs for revision %s: %v", revision, err)
				return
			}

			// Verify database
			verifyDatabase(db, revisionID)
		}(rev)
	}

	// Wait for all revisions to be processed
	wg.Wait()

	totalElapsed := time.Since(startTime)
	log.Printf("All revisions processed in %v", totalElapsed)

	// Perform final database optimizations
	log.Println("Optimizing database...")
	optimizations := []string{
		"PRAGMA optimize",
		"VACUUM",
		"ANALYZE",
	}

	for _, opt := range optimizations {
		if _, err := db.Exec(opt); err != nil {
			log.Printf("Warning: Failed to execute %s: %v", opt, err)
		}
	}

	log.Println("FOD finder completed successfully")
}

// func main() {
// 	if len(os.Args) < 2 {
// 		log.Fatalf("Usage: %s <nixpkgs-revision>", os.Args[0])
// 	}
// 	rev := os.Args[1]
//
// 	log.Printf("Testing Git operations for revision: %s", rev)
//
// 	worktreeDir, err := prepareNixpkgsWorktree(rev)
// 	if err != nil {
// 		log.Fatalf("Error: %v", err)
// 	}
//
// 	log.Printf("Success! Worktree created at: %s", worktreeDir)
//
// 	// Verify files exist
// 	criticalFiles := []string{
// 		"default.nix",
// 		"pkgs/top-level/all-packages.nix",
// 		"lib/default.nix",
// 	}
//
// 	for _, file := range criticalFiles {
// 		path := filepath.Join(worktreeDir, file)
// 		if _, err := os.Stat(path); err != nil {
// 			log.Printf("WARNING: File not found: %s", path)
// 		} else {
// 			log.Printf("File exists: %s", path)
// 		}
// 	}
// }
