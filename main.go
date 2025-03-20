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
	db.SetMaxOpenConns(runtime.NumCPU() * 2)
	db.SetMaxIdleConns(runtime.NumCPU())
	db.SetConnMaxLifetime(time.Hour)

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
	mutex         sync.Mutex
	commitTicker  *time.Ticker
	wg            sync.WaitGroup
	done          chan struct{}
	fodStmt       *sql.Stmt
	relStmt       *sql.Stmt
	revisionID    int64
	stats         struct {
		drvs int
		fods int
		sync.Mutex
	}
}

// NewDBBatcher creates a new database batcher
func NewDBBatcher(db *sql.DB, batchSize int, commitInterval time.Duration, revisionID int64) (*DBBatcher, error) {
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

func (b *DBBatcher) logStats() {
	b.stats.Lock()
	defer b.stats.Unlock()
	log.Printf("Stats: processed %d derivations, found %d FODs", b.stats.drvs, b.stats.fods)
}

func (b *DBBatcher) AddFOD(fod FOD) {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	b.fodBatch = append(b.fodBatch, fod)
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

func (b *DBBatcher) IncrementDrvCount() {
	b.stats.Lock()
	b.stats.drvs++
	b.stats.Unlock()
}

func (b *DBBatcher) commitBatch() {
	if len(b.fodBatch) == 0 {
		return
	}

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

	for _, fod := range b.fodBatch {
		_, err = fodStmt.Exec(
			fod.DrvPath, fod.OutputPath, fod.HashAlgorithm, fod.Hash,
			fod.OutputPath, fod.HashAlgorithm, fod.Hash,
		)
		if err != nil {
			log.Printf("Failed to insert FOD %s: %v", fod.DrvPath, err)
		}
	}

	for _, rel := range b.relationBatch {
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

	b.fodBatch = b.fodBatch[:0]
	b.relationBatch = b.relationBatch[:0]
}

func (b *DBBatcher) Flush() {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	b.commitBatch()
}

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

func processDerivation(inputFile string, batcher *DBBatcher, visited *sync.Map, workQueue chan<- string) {
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

	for name, out := range drv.Outputs {
		if out.HashAlgorithm != "" {
			fod := FOD{
				DrvPath:       inputFile,
				OutputPath:    out.Path,
				HashAlgorithm: out.HashAlgorithm,
				Hash:          out.Hash,
			}
			batcher.AddFOD(fod)
			if os.Getenv("VERBOSE") == "1" {
				log.Printf("Found FOD: %s (output: %s, hash: %s)",
					filepath.Base(inputFile), name, out.Hash)
			}
			break
		}
	}

	for path := range drv.InputDerivations {
		if _, alreadyVisited := visited.LoadOrStore(path, true); !alreadyVisited {
			select {
			case workQueue <- path:
			default:
				log.Printf("Warning: Queue full for %s, processing directly", path)
				go processDerivation(path, batcher, visited, workQueue)
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

	// Initialize the batcher only after Git setup is complete
	batcher, err := NewDBBatcher(db, 1000, 3*time.Second, revisionID)
	if err != nil {
		return fmt.Errorf("failed to create database batcher: %w", err)
	}
	defer batcher.Close()

	workQueue := make(chan string, 100000)
	visited := &sync.Map{}
	var wg sync.WaitGroup

	log.Printf("[%s] Running nix-eval-jobs to populate work queue", rev)
	if err := callNixEvalJobsWithGitWorktree(rev, worktreeDir, workQueue, visited); err != nil {
		return fmt.Errorf("failed to run nix-eval-jobs: %w", err)
	}

	numWorkers := runtime.NumCPU()
	log.Printf("[%s] Starting %d workers to process derivations", rev, numWorkers)
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for drvPath := range workQueue {
				processDerivation(drvPath, batcher, visited, workQueue)
			}
		}()
	}

	// Monitor memory usage
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			log.Printf("[%s] Memory: Alloc=%dMB TotalAlloc=%dMB Sys=%dMB",
				rev, m.Alloc/1024/1024, m.TotalAlloc/1024/1024, m.Sys/1024/1024)
		}
	}()

	// Wait for processing with timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-time.After(1 * time.Hour):
		log.Printf("[%s] Processing timed out after 1 hour", rev)
		close(workQueue)
	case <-done:
		log.Printf("[%s] All derivations processed", rev)
		close(workQueue)
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

func callNixEvalJobsWithGitWorktree(rev string, worktreeDir string, workQueue chan<- string, visited *sync.Map) error {
	log.Printf("[%s] Running nix-eval-jobs with worktree at %s", rev, worktreeDir)

	// Use the proper Nixpkgs import pattern
	expr := fmt.Sprintf(`
      let
        pkgs = import %s {
          config = { allowAliases = false; };
          overlays = [];
        };
      in pkgs
    `, worktreeDir)

	cmd := exec.Command("nix-eval-jobs",
		"--expr", expr,
		"--workers", fmt.Sprintf("%d", runtime.NumCPU()/2),
		"--max-memory-size", "4096",
		"--option", "allow-import-from-derivation", "false")

	// Capture stdout
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	// Redirect stderr to /dev/null instead of capturing it
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
			if _, alreadyVisited := visited.LoadOrStore(result.DrvPath, true); !alreadyVisited {
				select {
				case workQueue <- result.DrvPath:
				default:
					log.Printf("[%s] Warning: Work queue full, skipping %s", rev, result.DrvPath)
				}
			}
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

	mainRepoDir := filepath.Join(scriptDir, "nixpkgs-repo")
	worktreeDir := filepath.Join(scriptDir, fmt.Sprintf("nixpkgs-worktree-%s", rev))
	repoURL := "https://github.com/NixOS/nixpkgs.git"

	// Clean up Git worktree registry first
	pruneCmd := exec.Command("git", "-C", mainRepoDir, "worktree", "prune")
	pruneCmd.Stdout = os.Stdout
	pruneCmd.Stderr = os.Stderr
	if err := pruneCmd.Run(); err != nil {
		log.Printf("Warning: Failed to prune worktrees: %v", err)
	}

	// Remove existing worktree directory if it exists
	if _, err := os.Stat(worktreeDir); err == nil {
		removeCmd := exec.Command("git", "-C", mainRepoDir, "worktree", "remove", "--force", worktreeDir)
		removeCmd.Stdout = os.Stdout
		removeCmd.Stderr = os.Stderr
		if err := removeCmd.Run(); err != nil {
			log.Printf("Warning: Failed to remove worktree %s: %v", worktreeDir, err)
		}
		// Ensure directory is gone
		if err := os.RemoveAll(worktreeDir); err != nil {
			log.Printf("Warning: Failed to delete worktree directory %s: %v", worktreeDir, err)
		}
	}

	// Initialize or update the main repository
	if _, err := os.Stat(mainRepoDir); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(mainRepoDir), 0755); err != nil {
			return "", fmt.Errorf("failed to create parent directory: %w", err)
		}
		cloneCmd := exec.Command("git", "clone", "--mirror", repoURL, mainRepoDir)
		cloneCmd.Stdout = os.Stdout
		cloneCmd.Stderr = os.Stderr
		if err := cloneCmd.Run(); err != nil {
			return "", fmt.Errorf("failed to clone repository: %w", err)
		}
	}

	// Fetch the specific revision
	fetchCmd := exec.Command("git", "-C", mainRepoDir, "fetch", "origin", rev)
	fetchCmd.Stdout = os.Stdout
	fetchCmd.Stderr = os.Stderr
	if err := fetchCmd.Run(); err != nil {
		return "", fmt.Errorf("failed to fetch revision: %w", err)
	}

	// Create the worktree
	addCmd := exec.Command("git", "-C", mainRepoDir, "worktree", "add", "--force", worktreeDir, rev)
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

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Println("Starting FOD finder...")

	db := initDB()
	defer db.Close()

	if len(os.Args) < 2 {
		log.Fatalf("Usage: %s <nixpkgs-revision> [<nixpkgs-revision2> ...]", os.Args[0])
	}
	revisions := os.Args[1:]
	log.Printf("Processing %d nixpkgs revisions: %v", len(revisions), revisions)

	startTime := time.Now()
	var wg sync.WaitGroup
	for _, rev := range revisions {
		wg.Add(1)
		go func(revision string) {
			defer wg.Done()

			revisionID, err := getOrCreateRevision(db, revision)
			if err != nil {
				log.Printf("Failed to get or create revision %s: %v", revision, err)
				return
			}
			log.Printf("Using nixpkgs revision: %s (ID: %d)", revision, revisionID)

			if err := findFODsForRevision(revision, revisionID, db); err != nil {
				log.Printf("Error finding FODs for revision %s: %v", revision, err)
			}
		}(rev)
	}

	wg.Wait()
	totalElapsed := time.Since(startTime)
	log.Printf("All revisions processed in %v", totalElapsed)

	log.Println("Optimizing database...")
	for _, opt := range []string{"PRAGMA optimize", "VACUUM", "ANALYZE"} {
		if _, err := db.Exec(opt); err != nil {
			log.Printf("Warning: Failed to execute %s: %v", opt, err)
		}
	}

	log.Println("FOD finder completed successfully")
}
