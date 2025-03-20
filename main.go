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
	dbDir := "./db"
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		log.Fatalf("Failed to create database directory: %v", err)
	}

	dbPath := filepath.Join(dbDir, "fods.db")
	log.Printf("Using database at: %s", dbPath)

	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_synchronous=NORMAL&_cache_size=100000&_temp_store=MEMORY")
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}

	db.Exec("PRAGMA foreign_keys = ON;")

	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA cache_size=100000",
		"PRAGMA temp_store=MEMORY",
		"PRAGMA mmap_size=30000000000",
		"PRAGMA page_size=32768",
		"PRAGMA foreign_keys=ON",
	}

	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			log.Printf("Warning: Failed to set pragma %s: %v", pragma, err)
		}
	}

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
	fodStmt       *sql.Stmt
	relStmt       *sql.Stmt
	revisionID    int64
	stats         struct {
		drvs int
		fods int
	}
	lastStatsTime time.Time
	mu            sync.Mutex
}

// NewDBBatcher creates a new database batcher
func NewDBBatcher(db *sql.DB, batchSize int, revisionID int64) (*DBBatcher, error) {
	fodStmt, err := db.Prepare(`
        INSERT INTO fods (drv_path, output_path, hash_algorithm, hash)
        VALUES (?, ?, ?, ?)
        ON CONFLICT(drv_path) DO UPDATE SET
        output_path = ?, hash_algorithm = ?, hash = ?
    `)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare FOD statement: %w", err)
	}

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
		fodStmt:       fodStmt,
		relStmt:       relStmt,
		revisionID:    revisionID,
		lastStatsTime: time.Now(),
	}

	return batcher, nil
}

func (b *DBBatcher) logStats() {
	// Only log stats every 3 seconds
	if time.Since(b.lastStatsTime) >= 3*time.Second {
		log.Printf("Stats: processed %d derivations, found %d FODs", b.stats.drvs, b.stats.fods)
		b.lastStatsTime = time.Now()
	}
}

func (b *DBBatcher) AddFOD(fod FOD) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.fodBatch = append(b.fodBatch, fod)
	relation := DrvRevision{
		DrvPath:    fod.DrvPath,
		RevisionID: b.revisionID,
	}
	b.relationBatch = append(b.relationBatch, relation)
	b.stats.fods++

	if len(b.fodBatch) >= b.batchSize {
		b.commitBatch()
	}
}

func (b *DBBatcher) IncrementDrvCount() {
	b.mu.Lock()
	b.stats.drvs++
	shouldLog := time.Since(b.lastStatsTime) >= 3*time.Second
	b.mu.Unlock()

	if shouldLog {
		b.mu.Lock()
		b.logStats()
		b.mu.Unlock()
	}
}

func (b *DBBatcher) commitBatch() {
	// Already locked by caller
	if len(b.fodBatch) == 0 {
		return
	}

	// Copy the batches to local variables
	fodBatch := make([]FOD, len(b.fodBatch))
	copy(fodBatch, b.fodBatch)
	relationBatch := make([]DrvRevision, len(b.relationBatch))
	copy(relationBatch, b.relationBatch)

	// Clear the batches
	b.fodBatch = b.fodBatch[:0]
	b.relationBatch = b.relationBatch[:0]

	// Release the lock before database operations
	b.mu.Unlock()
	defer b.mu.Lock()

	tx, err := b.db.Begin()
	if err != nil {
		log.Printf("Failed to begin transaction: %v", err)
		return
	}

	success := false
	defer func() {
		if !success {
			if rollbackErr := tx.Rollback(); rollbackErr != nil {
				log.Printf("Failed to rollback transaction: %v", rollbackErr)
			}
		}
	}()

	fodStmt := tx.Stmt(b.fodStmt)
	relStmt := tx.Stmt(b.relStmt)

	for _, fod := range fodBatch {
		_, err = fodStmt.Exec(
			fod.DrvPath, fod.OutputPath, fod.HashAlgorithm, fod.Hash,
			fod.OutputPath, fod.HashAlgorithm, fod.Hash,
		)
		if err != nil {
			log.Printf("Failed to insert FOD %s: %v", fod.DrvPath, err)
		}
	}

	for _, rel := range relationBatch {
		_, err = relStmt.Exec(rel.DrvPath, rel.RevisionID)
		if err != nil {
			log.Printf("Failed to insert relation for %s: %v", rel.DrvPath, err)
		}
	}

	if err = tx.Commit(); err != nil {
		log.Printf("Failed to commit transaction: %v", err)
		return
	}
	success = true
}

func (b *DBBatcher) Flush() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.fodBatch) > 0 {
		// Temporarily release lock during database operations
		b.commitBatch()
	}
}

func (b *DBBatcher) Close() error {
	b.Flush()
	if err := b.fodStmt.Close(); err != nil {
		return err
	}
	if err := b.relStmt.Close(); err != nil {
		return err
	}
	return nil
}

// ProcessingContext holds the context for processing derivations
type ProcessingContext struct {
	batcher   *DBBatcher
	visited   *sync.Map
	wg        *sync.WaitGroup
	semaphore chan struct{} // Limit concurrency
}

func processDerivation(inputFile string, ctx *ProcessingContext) {
	ctx.batcher.IncrementDrvCount()

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

	for name, out := range drv.Outputs {
		if out.HashAlgorithm != "" {
			fod := FOD{
				DrvPath:       inputFile,
				OutputPath:    out.Path,
				HashAlgorithm: out.HashAlgorithm,
				Hash:          out.Hash,
			}
			ctx.batcher.AddFOD(fod)
			if os.Getenv("VERBOSE") == "1" {
				log.Printf("Found FOD: %s (output: %s, hash: %s)",
					filepath.Base(inputFile), name, out.Hash)
			}
			break
		}
	}

	// Queue input derivations to process
	for path := range drv.InputDerivations {
		if _, loaded := ctx.visited.LoadOrStore(path, true); !loaded {
			// Acquire semaphore to limit concurrency
			select {
			case ctx.semaphore <- struct{}{}:
				// We got a slot, process in a new goroutine
				ctx.wg.Add(1)
				go func(drvPath string) {
					defer ctx.wg.Done()
					defer func() { <-ctx.semaphore }() // Release semaphore when done
					processDerivation(drvPath, ctx)
				}(path)
			default:
				// No slots available, process in current goroutine
				processDerivation(path, ctx)
			}
		}
	}
}

func getOrCreateRevision(db *sql.DB, rev string) (int64, error) {
	var id int64
	err := db.QueryRow("SELECT id FROM revisions WHERE rev = ?", rev).Scan(&id)
	if err == nil {
		return id, nil
	} else if err != sql.ErrNoRows {
		return 0, fmt.Errorf("error checking for existing revision: %w", err)
	}

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

// BatchProcessingContext holds the context for batch processing derivations
type BatchProcessingContext struct {
	batcher    *DBBatcher
	visited    *sync.Map
	wg         *sync.WaitGroup
	semaphore  chan struct{} // Limit concurrency
	inputBatch chan []string // Channel for batches of input derivations
	batchSize  int           // Size of batches for input derivations
}

// Modified findFODsForRevision function with batch processing
func findFODsForRevision(rev string, revisionID int64, db *sql.DB) error {
	revStartTime := time.Now()
	log.Printf("[%s] Starting to find all FODs...", rev)

	worktreeDir, err := prepareNixpkgsWorktree(rev)
	if err != nil {
		return fmt.Errorf("failed to prepare worktree: %w", err)
	}
	defer func() {
		log.Printf("[%s] Cleaning up worktree at %s", rev, worktreeDir)
		os.RemoveAll(worktreeDir)
	}()

	// Initialize the batcher
	batcher, err := NewDBBatcher(db, 5000, revisionID) // Increased batch size
	if err != nil {
		return fmt.Errorf("failed to create database batcher: %w", err)
	}
	defer batcher.Close()

	visited := &sync.Map{}
	var wg sync.WaitGroup

	// Start memory monitoring
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			log.Printf("[%s] Memory: Alloc=%dMB TotalAlloc=%dMB Sys=%dMB NumGoroutine=%d",
				rev, m.Alloc/1024/1024, m.TotalAlloc/1024/1024, m.Sys/1024/1024, runtime.NumGoroutine())
		}
	}()

	// Determine optimal number of worker goroutines
	numWorkers := 16 // Adjusted down from 32 for batch processing

	// Create a channel for derivation paths
	drvPathChan := make(chan string, 10000) // Increased buffer size

	// Create a channel for batches of input derivations
	inputBatchChan := make(chan []string, 100)

	// Create processing context with semaphore to limit concurrency
	maxConcurrency := 500 // Reduced from 2000 for batch processing
	batchSize := 50       // Size of batches for processing

	ctx := &BatchProcessingContext{
		batcher:    batcher,
		visited:    visited,
		wg:         &wg,
		semaphore:  make(chan struct{}, maxConcurrency),
		inputBatch: inputBatchChan,
		batchSize:  batchSize,
	}

	// Start worker goroutines to process derivations in batches
	for range numWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for {
				// Create a batch
				batch := make([]string, 0, batchSize)

				// Try to fill the batch from the channel
				// First get at least one item or exit if channel is closed
				select {
				case drvPath, ok := <-drvPathChan:
					if !ok {
						// Channel is closed, no more work
						return
					}
					batch = append(batch, drvPath)
				case <-time.After(5 * time.Second):
					// Timeout waiting for work, check if we should exit
					select {
					case drvPath, ok := <-drvPathChan:
						if !ok {
							return
						}
						batch = append(batch, drvPath)
					default:
						// No work and timed out, exit
						return
					}
				}

				// Try to get more items without blocking
			batchFilling:
				for len(batch) < batchSize {
					select {
					case path, ok := <-drvPathChan:
						if !ok {
							// Channel closed
							break batchFilling
						}
						batch = append(batch, path)
					default:
						// No more items available without blocking
						break batchFilling
					}
				}

				// Process the batch
				if len(batch) > 0 {
					processBatch(batch, ctx)
				}
			}
		}()
	}

	// Start a goroutine to process input derivation batches
	wg.Add(1)
	go func() {
		defer wg.Done()
		for batch := range inputBatchChan {
			// Process each batch of input derivations
			for _, path := range batch {
				if _, loaded := visited.LoadOrStore(path, true); !loaded {
					// Send to the main processing channel
					drvPathChan <- path
				}
			}
		}
	}()

	// Run nix-eval-jobs in a separate goroutine and feed the channel
	evalJobsErr := make(chan error, 1)
	go func() {
		defer close(drvPathChan)    // Close the channel when done
		defer close(inputBatchChan) // Close the input batch channel when done

		// Run nix-eval-jobs and stream results to the channel
		err := streamNixEvalJobs(rev, worktreeDir, 8, drvPathChan)
		evalJobsErr <- err
	}()

	// Wait for completion or timeout
	done := make(chan struct{})
	timeout := time.After(1 * time.Hour)

	// Start a goroutine to wait for workers to finish
	go func() {
		wg.Wait()
		close(done)
	}()

	// Wait for completion, timeout, or error
	select {
	case err := <-evalJobsErr:
		if err != nil {
			return fmt.Errorf("nix-eval-jobs failed: %w", err)
		}
	case <-done:
		log.Printf("[%s] All derivations processed", rev)
	case <-timeout:
		log.Printf("[%s] Processing timed out after 1 hour", rev)
	}

	batcher.Flush()
	revElapsed := time.Since(revStartTime)
	log.Printf("[%s] Process completed in %v", rev, revElapsed)

	var fodCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM drv_revisions WHERE revision_id = ?", revisionID).Scan(&fodCount); err != nil {
		log.Printf("[%s] Error counting FODs: %v", rev, err)
	}
	log.Printf("[%s] Final stats: %d FODs", rev, fodCount)
	return nil
}

// processBatch processes a batch of derivation paths
func processBatch(batch []string, ctx *BatchProcessingContext) {
	// Filter out already visited paths
	var toProcess []string
	for _, path := range batch {
		if _, loaded := ctx.visited.LoadOrStore(path, true); !loaded {
			toProcess = append(toProcess, path)
		}
	}

	if len(toProcess) == 0 {
		return
	}

	// Process each derivation in the batch
	inputDrvs := make([][]string, 0, len(toProcess))

	for _, drvPath := range toProcess {
		ctx.batcher.IncrementDrvCount()

		file, err := os.Open(drvPath)
		if err != nil {
			log.Printf("Error opening file %s: %v", drvPath, err)
			continue
		}

		drv, err := derivation.ReadDerivation(file)
		file.Close() // Close file immediately after reading

		if err != nil {
			log.Printf("Error reading derivation %s: %v", drvPath, err)
			continue
		}

		// Check for FODs
		for name, out := range drv.Outputs {
			if out.HashAlgorithm != "" {
				fod := FOD{
					DrvPath:       drvPath,
					OutputPath:    out.Path,
					HashAlgorithm: out.HashAlgorithm,
					Hash:          out.Hash,
				}
				ctx.batcher.AddFOD(fod)
				if os.Getenv("VERBOSE") == "1" {
					log.Printf("Found FOD: %s (output: %s, hash: %s)",
						filepath.Base(drvPath), name, out.Hash)
				}
				break
			}
		}

		// Collect input derivations
		if len(drv.InputDerivations) > 0 {
			inputPaths := make([]string, 0, len(drv.InputDerivations))
			for path := range drv.InputDerivations {
				inputPaths = append(inputPaths, path)
			}

			if len(inputPaths) > 0 {
				inputDrvs = append(inputDrvs, inputPaths)
			}
		}
	}

	// Process input derivations in batches
	if len(inputDrvs) > 0 {
		// Flatten all input derivations
		allInputs := make([]string, 0, len(inputDrvs)*5) // Estimate
		for _, inputs := range inputDrvs {
			allInputs = append(allInputs, inputs...)
		}

		// Send in batches to the input batch channel
		for i := 0; i < len(allInputs); i += ctx.batchSize {
			end := i + ctx.batchSize
			if end > len(allInputs) {
				end = len(allInputs)
			}

			// Create a batch
			batch := allInputs[i:end]

			// Send the batch to be processed
			select {
			case ctx.inputBatch <- batch:
				// Batch sent successfully
			default:
				// Channel is full, process in current goroutine
				for _, path := range batch {
					if _, loaded := ctx.visited.LoadOrStore(path, true); !loaded {
						// Process directly
						file, err := os.Open(path)
						if err != nil {
							log.Printf("Error opening file %s: %v", path, err)
							continue
						}

						drv, err := derivation.ReadDerivation(file)
						file.Close()

						if err != nil {
							log.Printf("Error reading derivation %s: %v", path, err)
							continue
						}

						ctx.batcher.IncrementDrvCount()

						// Check for FODs
						for name, out := range drv.Outputs {
							if out.HashAlgorithm != "" {
								fod := FOD{
									DrvPath:       path,
									OutputPath:    out.Path,
									HashAlgorithm: out.HashAlgorithm,
									Hash:          out.Hash,
								}
								ctx.batcher.AddFOD(fod)
								if os.Getenv("VERBOSE") == "1" {
									log.Printf("Found FOD: %s (output: %s, hash: %s)",
										filepath.Base(path), name, out.Hash)
								}
								break
							}
						}
					}
				}
			}
		}
	}
}

// streamNixEvalJobs runs nix-eval-jobs and streams results to the provided channel
func streamNixEvalJobs(rev string, nixpkgsDir string, workers int, drvPathChan chan<- string) error {
	log.Printf("[%s] Running nix-eval-jobs with nixpkgs at %s", rev, nixpkgsDir)

	// Path to the release-outpaths.nix file
	releasePath := filepath.Join(nixpkgsDir, "pkgs/top-level/release-outpaths.nix")

	// Build the command with the specific Nix expression path and arguments
	cmd := exec.Command("nix-eval-jobs",
		releasePath,
		"--arg", "checkMeta", "false",
		"--workers", fmt.Sprintf("%d", workers),
		"--max-memory-size", "4096",
		"--option", "allow-import-from-derivation", "false")

	// Capture stdout
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	// Redirect stderr to /dev/null
	nullFile, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("failed to open null device: %w", err)
	}
	defer nullFile.Close()
	cmd.Stderr = nullFile

	err = cmd.Start()
	if err != nil {
		return fmt.Errorf("failed to start nix-eval-jobs: %w", err)
	}

	scanner := bufio.NewScanner(stdout)
	const maxScannerSize = 10 * 1024 * 1024
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
			drvPathChan <- result.DrvPath
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading stdout: %w", err)
	}

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("nix-eval-jobs command failed: %w", err)
	}

	log.Printf("[%s] nix-eval-jobs completed with %d jobs", rev, jobCount)
	return nil
}

func prepareNixpkgsWorktree(rev string) (string, error) {
	if len(rev) != 40 {
		return "", fmt.Errorf("invalid commit hash length: %s", rev)
	}

	scriptDir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get current directory: %w", err)
	}

	// Create a main repository directory if it doesn't exist
	mainRepoDir := filepath.Join(scriptDir, "nixpkgs-repo")
	worktreeDir := filepath.Join(scriptDir, fmt.Sprintf("nixpkgs-worktree-%s", rev))
	repoURL := "https://github.com/NixOS/nixpkgs.git"

	// Clean up existing worktree if it exists
	if _, err := os.Stat(worktreeDir); err == nil {
		log.Printf("Removing existing worktree directory: %s", worktreeDir)
		if err := os.RemoveAll(worktreeDir); err != nil {
			return "", fmt.Errorf("failed to remove existing worktree directory: %w", err)
		}
	}

	// Initialize or update the main repository with minimal history
	if _, err := os.Stat(mainRepoDir); os.IsNotExist(err) {
		log.Printf("Initializing shallow clone of nixpkgs repository")
		if err := os.MkdirAll(filepath.Dir(mainRepoDir), 0755); err != nil {
			return "", fmt.Errorf("failed to create parent directory: %w", err)
		}

		// Initialize a bare repository
		initCmd := exec.Command("git", "init", "--bare", mainRepoDir)
		initCmd.Stdout = os.Stdout
		initCmd.Stderr = os.Stderr
		if err := initCmd.Run(); err != nil {
			return "", fmt.Errorf("failed to initialize bare repository: %w", err)
		}

		// Add the remote
		remoteCmd := exec.Command("git", "-C", mainRepoDir, "remote", "add", "origin", repoURL)
		remoteCmd.Stdout = os.Stdout
		remoteCmd.Stderr = os.Stderr
		if err := remoteCmd.Run(); err != nil {
			return "", fmt.Errorf("failed to add remote: %w", err)
		}
	}

	// Prune any stale worktrees first
	pruneCmd := exec.Command("git", "-C", mainRepoDir, "worktree", "prune")
	pruneCmd.Stdout = os.Stdout
	pruneCmd.Stderr = os.Stderr
	if err := pruneCmd.Run(); err != nil {
		log.Printf("Warning: Failed to prune worktrees: %v", err)
	}

	// Fetch only the specific revision
	log.Printf("Fetching only commit %s from repository", rev)
	fetchCmd := exec.Command("git", "-C", mainRepoDir, "fetch", "--depth=1", "origin", rev)
	fetchCmd.Stdout = os.Stdout
	fetchCmd.Stderr = os.Stderr
	if err := fetchCmd.Run(); err != nil {
		return "", fmt.Errorf("failed to fetch revision: %w", err)
	}

	// Create the worktree with force flag
	log.Printf("Creating worktree for revision %s", rev)
	addCmd := exec.Command("git", "-C", mainRepoDir, "worktree", "add", "--force", "--detach", worktreeDir, rev)
	addCmd.Stdout = os.Stdout
	addCmd.Stderr = os.Stderr
	if err := addCmd.Run(); err != nil {
		return "", fmt.Errorf("failed to create worktree: %w", err)
	}

	// Verify worktree
	minverPath := filepath.Join(worktreeDir, "lib", "minver.nix")
	if _, err := os.Stat(minverPath); os.IsNotExist(err) {
		log.Printf("Warning: Could not find %s, structure may have changed", minverPath)
	}

	log.Printf("Prepared worktree for revision %s at %s", rev, worktreeDir)
	return worktreeDir, nil
}

func callNixEvalJobs(rev string, nixpkgsDir string, workers int) ([]string, error) {
	log.Printf("[%s] Running nix-eval-jobs with nixpkgs at %s", rev, nixpkgsDir)

	// Use the proper Nixpkgs import pattern
	expr := fmt.Sprintf(`
      let
        pkgs = import %s {
          config = { allowAliases = false; };
          overlays = [];
        };
      in pkgs
    `, nixpkgsDir)

	cmd := exec.Command("nix-eval-jobs",
		"--expr", expr,
		"--workers", fmt.Sprintf("%d", workers),
		"--max-memory-size", "4096",
		"--option", "allow-import-from-derivation", "false")

	// Capture stdout
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	// Redirect stderr to /dev/null
	nullFile, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to open null device: %w", err)
	}
	defer nullFile.Close()
	cmd.Stderr = nullFile

	err = cmd.Start()
	if err != nil {
		return nil, fmt.Errorf("failed to start nix-eval-jobs: %w", err)
	}

	scanner := bufio.NewScanner(stdout)
	const maxScannerSize = 10 * 1024 * 1024
	buf := make([]byte, maxScannerSize)
	scanner.Buffer(buf, maxScannerSize)

	var drvPaths []string
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
			drvPaths = append(drvPaths, result.DrvPath)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading stdout: %w", err)
	}

	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("nix-eval-jobs command failed: %w", err)
	}

	log.Printf("[%s] nix-eval-jobs completed with %d jobs", rev, jobCount)
	return drvPaths, nil
}

// isWorktreeDir checks if a directory is a nixpkgs worktree directory
func isWorktreeDir(name string) bool {
	return strings.HasPrefix(name, "nixpkgs-worktree-")
}

func cleanupWorktrees() error {
	scriptDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get current directory: %w", err)
	}

	mainRepoDir := filepath.Join(scriptDir, "nixpkgs-repo")
	if _, err := os.Stat(mainRepoDir); os.IsNotExist(err) {
		// No repository to clean up
		return nil
	}

	// Prune worktrees
	pruneCmd := exec.Command("git", "-C", mainRepoDir, "worktree", "prune")
	pruneCmd.Stdout = os.Stdout
	pruneCmd.Stderr = os.Stderr
	if err := pruneCmd.Run(); err != nil {
		return fmt.Errorf("failed to prune worktrees: %w", err)
	}

	// Find and remove any leftover worktree directories
	entries, err := os.ReadDir(scriptDir)
	if err != nil {
		return fmt.Errorf("failed to read directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() && isWorktreeDir(entry.Name()) {
			worktreePath := filepath.Join(scriptDir, entry.Name())
			log.Printf("Removing leftover worktree directory: %s", worktreePath)
			if err := os.RemoveAll(worktreePath); err != nil {
				log.Printf("Warning: Failed to remove directory %s: %v", worktreePath, err)
			}
		}
	}

	return nil
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Println("Starting FOD finder...")

	// Clean up any leftover worktrees from previous runs
	if err := cleanupWorktrees(); err != nil {
		log.Printf("Warning: Failed to clean up worktrees: %v", err)
	}

	db := initDB()
	defer db.Close()

	if len(os.Args) < 2 {
		log.Fatalf("Usage: %s <nixpkgs-revision> [<nixpkgs-revision2> ...]", os.Args[0])
	}
	revisions := os.Args[1:]
	log.Printf("Processing %d nixpkgs revisions: %v", len(revisions), revisions)

	startTime := time.Now()

	// Process revisions sequentially
	for _, rev := range revisions {
		revisionID, err := getOrCreateRevision(db, rev)
		if err != nil {
			log.Printf("Failed to get or create revision %s: %v", rev, err)
			continue
		}
		log.Printf("Using nixpkgs revision: %s (ID: %d)", rev, revisionID)

		if err := findFODsForRevision(rev, revisionID, db); err != nil {
			log.Printf("Error finding FODs for revision %s: %v", rev, err)
		}
	}

	totalElapsed := time.Since(startTime)
	log.Printf("All revisions processed in %v", totalElapsed)

	log.Println("Optimizing database...")
	for _, opt := range []string{"PRAGMA optimize", "VACUUM", "ANALYZE"} {
		if _, err := db.Exec(opt); err != nil {
			log.Printf("Warning: Failed to execute %s: %v", opt, err)
		}
	}

	// Final cleanup
	if err := cleanupWorktrees(); err != nil {
		log.Printf("Warning: Failed to clean up worktrees: %v", err)
	}

	log.Println("FOD finder completed successfully")
}
