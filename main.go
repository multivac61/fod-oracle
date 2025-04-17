package main

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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

// exprFileStats tracks statistics for each expression file evaluation
type exprFileStats struct {
	exists           bool
	attempted        bool
	succeeded        bool
	errorMessage     string
	derivationsFound int
}

// NeofetchInfo represents the parsed JSON output from neofetch
type NeofetchInfo struct {
	Host         string `json:"Host"`
	CPU          string `json:"CPU"`
	Cores        string `json:"CPU Cores"`
	Memory       string `json:"Memory"`
	Kernel       string `json:"Kernel"`
	OS           string `json:"OS"`
	Shell        string `json:"Shell"`
	Resolution   string `json:"Resolution"`
	DE           string `json:"DE"`
	WM           string `json:"WM"`
	Terminal     string `json:"Terminal"`
	TerminalFont string `json:"Terminal Font"`
	CPUUsage     string `json:"CPU Usage"`
	DiskUsage    string `json:"Disk"`
	Battery      string `json:"Battery"`
	LocalIP      string `json:"Local IP"`
	PublicIP     string `json:"Public IP"`
	Uptime       string `json:"Uptime"`

	// Add other fields that might be present in neofetch output
	// The raw JSON string is also stored, so we don't lose any fields
}

// Default number of workers for nix-eval-jobs, will be set to NumCPU in init()
var workers int

// systemInfo holds the neofetch JSON data
var systemInfoJSON string

// systemInfo holds the parsed neofetch data
var systemInfo NeofetchInfo

// getCPUCores extracts the number of cores from neofetch CPU cores field
func getCPUCores(coresStr string) int {
	// Handle format like "8 (16)" (8 physical, 16 logical)
	if strings.Contains(coresStr, "(") {
		parts := strings.Split(coresStr, "(")
		if len(parts) >= 2 {
			logical := strings.TrimRight(parts[1], ")")
			if cores, err := strconv.Atoi(strings.TrimSpace(logical)); err == nil {
				return cores
			}
		}
	}

	// Try to parse as a number (logic cores)
	if cores, err := strconv.Atoi(strings.TrimSpace(coresStr)); err == nil {
		return cores
	}

	// Fallback to runtime.NumCPU()
	return runtime.NumCPU()
}

// getSystemInfo collects information about the system using neofetch
func getSystemInfo() (NeofetchInfo, string) {
	// Default fallback info
	info := NeofetchInfo{
		Host:   "unknown",
		CPU:    "unknown",
		Cores:  fmt.Sprintf("%d", runtime.NumCPU()),
		Memory: "unknown",
		Kernel: runtime.GOOS,
		OS:     runtime.GOOS + "/" + runtime.GOARCH,
	}

	// Get hostname as a fallback
	if hostname, err := os.Hostname(); err == nil {
		info.Host = hostname
	}

	// Try to run neofetch via nix
	cmd := exec.Command("neofetch", "--stdout", "--json")
	output, err := cmd.Output()
	if err != nil {
		log.Printf("Warning: Failed to run neofetch: %v. Using basic system info.", err)
		jsonData, _ := json.Marshal(info)
		return info, string(jsonData)
	}

	// Parse the JSON output
	jsonStr := string(output)
	if err := json.Unmarshal([]byte(jsonStr), &info); err != nil {
		log.Printf("Warning: Failed to parse neofetch JSON: %v. Using basic system info.", err)
		jsonData, _ := json.Marshal(info)
		return info, string(jsonData)
	}

	return info, jsonStr
}

// init initializes configuration from environment variables
func init() {
	// Collect system information
	var jsonStr string
	systemInfo, jsonStr = getSystemInfo()
	systemInfoJSON = jsonStr

	// Set the default number of workers to the number of CPU cores
	workers = runtime.NumCPU()
	
	// Check for custom worker count in environment
	if workersEnv := os.Getenv("FOD_ORACLE_NUM_WORKERS"); workersEnv != "" {
		if w, err := strconv.Atoi(workersEnv); err == nil && w > 0 {
			workers = w
			log.Printf("Using %d workers from FOD_ORACLE_NUM_WORKERS environment variable", workers)
		} else if err != nil {
			log.Printf("Warning: Invalid FOD_ORACLE_NUM_WORKERS value '%s': %v. Using default: %d (number of CPU cores)",
				workersEnv, err, workers)
		}
	} else {
		log.Printf("Using default worker count of %d (number of CPU cores)", workers)
	}
}

// initDB initializes the SQLite database
func initDB() *sql.DB {
	dbPath := os.Getenv("FOD_ORACLE_DB_PATH")
	if dbPath == "" {
		// Default to the db directory in the current working directory
		currentDir, err := os.Getwd()
		if err != nil {
			log.Fatalf("Failed to get current directory: %v", err)
		}
		dbPath = filepath.Join(currentDir, "db", "fods.db")
	}

	// Add busy_timeout and other optimizations
	connString := dbPath + "?_journal_mode=WAL" +
		"&_synchronous=NORMAL" +
		"&_cache_size=100000" +
		"&_temp_store=MEMORY" +
		"&_busy_timeout=10000" + // 10 second timeout
		"&_locking_mode=NORMAL"

	db, err := sql.Open("sqlite3", connString)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}

	// Reduce connection pool size
	db.SetMaxOpenConns(8) // Significantly reduced
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(time.Minute * 10)

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
	writeChan     chan writeOperation
	done          chan struct{}
}

type writeOperation struct {
	fods       []FOD
	relations  []DrvRevision
	resultChan chan error
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
		writeChan:     make(chan writeOperation, 1000),
		done:          make(chan struct{}),
	}

	// Start a dedicated writer goroutine
	go func() {
		defer close(batcher.done)
		for op := range batcher.writeChan {
			err := batcher.executeWrite(op.fods, op.relations)
			if op.resultChan != nil {
				op.resultChan <- err
			}
		}
	}()

	return batcher, nil
}

func (b *DBBatcher) logStats() {
	// Only log stats every 3 seconds
	if time.Since(b.lastStatsTime) >= 3*time.Second {
		log.Printf("Stats: processed %d derivations, found %d FODs", b.stats.drvs, b.stats.fods)
		b.lastStatsTime = time.Now()
	}
}

// Add this method to execute writes with retries
func (b *DBBatcher) executeWrite(fods []FOD, relations []DrvRevision) error {
	var lastErr error

	// Try up to 5 times with exponential backoff
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(100*attempt) * time.Millisecond
			time.Sleep(backoff)
		}

		tx, err := b.db.Begin()
		if err != nil {
			lastErr = err
			continue
		}

		fodStmt := tx.Stmt(b.fodStmt)
		relStmt := tx.Stmt(b.relStmt)

		success := true

		// Insert FODs
		for _, fod := range fods {
			_, err = fodStmt.Exec(
				fod.DrvPath, fod.OutputPath, fod.HashAlgorithm, fod.Hash,
				fod.OutputPath, fod.HashAlgorithm, fod.Hash,
			)
			if err != nil {
				success = false
				lastErr = err
				break
			}
		}

		if !success {
			tx.Rollback()
			continue
		}

		// Insert relations
		for _, rel := range relations {
			_, err = relStmt.Exec(rel.DrvPath, rel.RevisionID)
			if err != nil {
				success = false
				lastErr = err
				break
			}
		}

		if !success {
			tx.Rollback()
			continue
		}

		// Commit transaction
		if err = tx.Commit(); err != nil {
			lastErr = err
			continue
		}

		// Success!
		return nil
	}

	return lastErr
}

// Modify AddFOD to use the writer goroutine
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
		// Copy batches to local variables
		fods := make([]FOD, len(b.fodBatch))
		copy(fods, b.fodBatch)
		relations := make([]DrvRevision, len(b.relationBatch))
		copy(relations, b.relationBatch)

		// Clear batches
		b.fodBatch = b.fodBatch[:0]
		b.relationBatch = b.relationBatch[:0]

		// Send to writer goroutine
		b.writeChan <- writeOperation{
			fods:      fods,
			relations: relations,
		}
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

// Modify Flush to use the writer goroutine
func (b *DBBatcher) Flush() {
	b.mu.Lock()

	if len(b.fodBatch) > 0 {
		// Copy batches to local variables
		fods := make([]FOD, len(b.fodBatch))
		copy(fods, b.fodBatch)
		relations := make([]DrvRevision, len(b.relationBatch))
		copy(relations, b.relationBatch)

		// Clear batches
		b.fodBatch = b.fodBatch[:0]
		b.relationBatch = b.relationBatch[:0]

		b.mu.Unlock()

		// Send to writer goroutine and wait for result
		resultChan := make(chan error, 1)
		b.writeChan <- writeOperation{
			fods:       fods,
			relations:  relations,
			resultChan: resultChan,
		}
		<-resultChan
	} else {
		b.mu.Unlock()
	}
}

// Modify Close to shut down the writer goroutine
func (b *DBBatcher) Close() error {
	b.Flush()
	close(b.writeChan)
	<-b.done // Wait for writer to finish

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
	batcher     *DBBatcher
	visited     *sync.Map
	processedDrvs map[string]bool // Map to quickly check if we've already processed a derivation
	wg          *sync.WaitGroup
	workQueue   chan string       // Channel for queuing derivation paths
}

// processDerivation processes a single derivation
// It assumes the caller has already checked and marked the derivation as processed
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
		// Send to the queue - each worker will check if it's been processed
		ctx.workQueue <- path
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

	// Initialize the batcher
	batcher, err := NewDBBatcher(db, 1000, revisionID)
	if err != nil {
		return fmt.Errorf("failed to create database batcher: %w", err)
	}
	defer batcher.Close()

	// Use sync.Map for thread-safe sharing across goroutines
	visited := &sync.Map{}
	var wg sync.WaitGroup

	// Track peak memory usage
	var peakMemoryMB int
	var memoryMutex sync.Mutex

	// Start memory monitoring
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)

			// Update peak memory if higher
			allocMB := int(m.Alloc / 1024 / 1024)
			memoryMutex.Lock()
			if allocMB > peakMemoryMB {
				peakMemoryMB = allocMB
			}
			memoryMutex.Unlock()

			log.Printf("[%s] Memory: Alloc=%dMB TotalAlloc=%dMB Sys=%dMB (Peak=%dMB)",
				rev, allocMB, m.TotalAlloc/1024/1024, m.Sys/1024/1024, peakMemoryMB)
		}
	}()

	// Create a processing context with a work queue
	workQueue := make(chan string, 100000) // Large buffer to avoid blocking
	
	// Create a map for quick lookup to avoid processing the same path multiple times
	processedDrvs := make(map[string]bool)
	
	// Use number of CPU cores for worker pool size
	numCPUCores := runtime.NumCPU()
	log.Printf("[%s] Setting up worker pool with %d workers (matching CPU cores)", rev, numCPUCores)
	
	ctx := &ProcessingContext{
		batcher:     batcher,
		visited:     visited,
		processedDrvs: processedDrvs,
		wg:          &wg,
		workQueue:   workQueue,
	}

	// Create a channel to receive derivation paths from nix-eval-jobs
	drvPathChan := make(chan string, 50000)

	// Create a separate mutex for the processedDrvs map
	// since it will be accessed by multiple worker goroutines
	var processedMutex sync.Mutex

	// Start worker pool with number of workers matching CPU cores
	workerDone := make(chan struct{})
	for i := 0; i < numCPUCores; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			
			for drvPath := range workQueue {
				// Local check with mutex to avoid processing the same path multiple times
				processedMutex.Lock()
				if _, exists := processedDrvs[drvPath]; exists {
					processedMutex.Unlock()
					continue
				}
				processedDrvs[drvPath] = true
				processedMutex.Unlock()
				
				processDerivation(drvPath, ctx)
			}
			
			if workerID == 0 {
				log.Printf("[%s] Worker %d: Work queue closed, completing remaining tasks", rev, workerID)
			}
		}(i)
	}

	// Start a goroutine to process derivation paths from nix-eval-jobs
	go func() {
		for drvPath := range drvPathChan {
			// Check if we've already seen this path before sending to work queue
			processedMutex.Lock()
			if _, exists := processedDrvs[drvPath]; !exists {
				workQueue <- drvPath
			}
			processedMutex.Unlock()
		}
		
		// Close the work queue when all inputs have been processed
		close(workQueue)
		
		// Wait for all workers to complete
		wg.Wait()
		close(workerDone)
	}()

	// Variable to track if fallback was used
	usedFallback := false

	// Variable to hold evaluation metadata
	fileStats := make(map[string]*exprFileStats)

	// Run nix-eval-jobs and stream results to the channel
	log.Printf("[%s] Running nix-eval-jobs with %d workers to populate work queue", rev, workers)
	if err := streamNixEvalJobs(rev, worktreeDir, workers, drvPathChan, fileStats, &usedFallback); err != nil {
		close(drvPathChan)

		// Store metadata even if there was an error
		processingTime := time.Since(revStartTime)
		memoryMutex.Lock()
		currentPeakMemory := peakMemoryMB
		memoryMutex.Unlock()

		if storeErr := storeEvaluationMetadata(db, revisionID, fileStats, worktreeDir,
			processingTime, usedFallback, currentPeakMemory); storeErr != nil {
			log.Printf("[%s] Error storing evaluation metadata: %v", rev, storeErr)
		}

		return fmt.Errorf("failed to run nix-eval-jobs: %w", err)
	}
	close(drvPathChan)

	// Wait for completion or timeout
	timeout := time.After(1 * time.Hour)
	select {
	case <-workerDone:
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
	log.Printf("[%s] Final stats: %d FODs (%d derivations processed)", rev, fodCount, len(processedDrvs))

	// Store the evaluation metadata in the database
	memoryMutex.Lock()
	finalPeakMemory := peakMemoryMB
	memoryMutex.Unlock()

	if err := storeEvaluationMetadata(db, revisionID, fileStats, worktreeDir,
		revElapsed, usedFallback, finalPeakMemory); err != nil {
		log.Printf("[%s] Error storing evaluation metadata: %v", rev, err)
	} else {
		cpuCores := getCPUCores(systemInfo.Cores)
		log.Printf("[%s] Successfully stored evaluation metadata in database (workers=%d, peak memory=%dMB)",
			rev, numCPUCores, finalPeakMemory)
		log.Printf("[%s] System info: %s, %s, %s, %d cores",
			rev, systemInfo.Host, systemInfo.OS, systemInfo.CPU, cpuCores)
	}

	return nil
}

// streamNixEvalJobs runs nix-eval-jobs and streams results to the provided channel
func streamNixEvalJobs(rev string, nixpkgsDir string, workers int, drvPathChan chan<- string,
	fileStats map[string]*exprFileStats, usedFallback *bool,
) error {
	log.Printf("[%s] Running nix-eval-jobs with nixpkgs at %s (%d worker threads)", rev, nixpkgsDir, workers)

	// Define all possible Nix expression files that could be evaluated
	possiblePaths := []string{
		filepath.Join(nixpkgsDir, "pkgs/top-level/release-outpaths.nix"),
		filepath.Join(nixpkgsDir, "pkgs/top-level/release.nix"),
		filepath.Join(nixpkgsDir, "pkgs/top-level/release-small.nix"),
		filepath.Join(nixpkgsDir, "pkgs/top-level/all-packages.nix"),
		filepath.Join(nixpkgsDir, "nixos/release.nix"),
		filepath.Join(nixpkgsDir, "nixos/release-small.nix"),
		filepath.Join(nixpkgsDir, "nixos/release-combined.nix"),
		filepath.Join(nixpkgsDir, "all-packages.nix"),
		filepath.Join(nixpkgsDir, "default.nix"),
	}

	// // Begin collecting stats for the expression files
	// fileStats = make(map[string]*exprFileStats)

	// Check which expression files exist and initialize stats
	var existingPaths []string
	for _, path := range possiblePaths {
		relPath, _ := filepath.Rel(nixpkgsDir, path)
		fileStats[path] = &exprFileStats{}

		if _, err := os.Stat(path); err == nil {
			fileStats[path].exists = true
			existingPaths = append(existingPaths, path)
			log.Printf("[%s] Found Nix expression file: %s", rev, relPath)
		} else {
			log.Printf("[%s] Expression file does not exist: %s", rev, relPath)
		}
	}

	// We'll try using the fallback mechanism if no files exist
	if len(existingPaths) == 0 {
		log.Printf("[%s] Warning: No suitable Nix expression files found in %s, will try fallback mechanism", rev, nixpkgsDir)
	}

	totalJobCount := 0
	// Global deduplication map across all expression files
	globalVisited := make(map[string]bool)

	// Evaluate each expression file
	for _, expressionPath := range existingPaths {
		relPath, _ := filepath.Rel(nixpkgsDir, expressionPath)
		stats := fileStats[expressionPath]

		log.Printf("[%s] Attempting to evaluate expression file: %s", rev, relPath)
		stats.attempted = true

		// Build the command with the specific Nix expression path and arguments
		cmd := exec.Command("nix-eval-jobs",
			expressionPath,
			"--arg", "checkMeta", "false",
			"--workers", fmt.Sprintf("%d", workers),
			"--max-memory-size", "4096",
			"--option", "allow-import-from-derivation", "false")

		// Capture stdout
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			errMsg := fmt.Sprintf("Failed to create stdout pipe: %v", err)
			stats.errorMessage = errMsg
			log.Printf("[%s] %s for %s", rev, errMsg, relPath)
			continue
		}

		// Capture stderr
		stderr, err := cmd.StderrPipe()
		if err != nil {
			errMsg := fmt.Sprintf("Failed to create stderr pipe: %v", err)
			stats.errorMessage = errMsg
			log.Printf("[%s] %s for %s", rev, errMsg, relPath)
			continue
		}

		// Start capturing stderr in a goroutine
		stderrChan := make(chan string, 1)
		go func() {
			stderrBytes, _ := io.ReadAll(stderr)
			stderrChan <- string(stderrBytes)
		}()

		err = cmd.Start()
		if err != nil {
			errMsg := fmt.Sprintf("Failed to start nix-eval-jobs: %v", err)
			stats.errorMessage = errMsg
			log.Printf("[%s] %s for %s", rev, errMsg, relPath)
			continue
		}

		// Process stdout
		scanner := bufio.NewScanner(stdout)
		const maxScannerSize = 10 * 1024 * 1024
		buf := make([]byte, maxScannerSize)
		scanner.Buffer(buf, maxScannerSize)

		jobCount := 0
		fileVisited := make(map[string]bool)

		for scanner.Scan() {
			line := scanner.Text()
			var result struct {
				DrvPath string `json:"drvPath"`
			}
			if err := json.Unmarshal([]byte(line), &result); err != nil {
				log.Printf("[%s] Failed to parse JSON from %s: %v", rev, relPath, err)
				continue
			}

			// Only count each derivation once per file and globally
			if result.DrvPath != "" {
				fileVisited[result.DrvPath] = true
				if !globalVisited[result.DrvPath] {
					globalVisited[result.DrvPath] = true
					totalJobCount++
					drvPathChan <- result.DrvPath
				}

				jobCount++
				if jobCount%1000 == 0 {
					log.Printf("[%s] Processed %d jobs from %s (unique in this file: %d, global total: %d)",
						rev, jobCount, relPath, len(fileVisited), totalJobCount)
				}
			}
		}

		stats.derivationsFound = len(fileVisited)

		if err := scanner.Err(); err != nil {
			errMsg := fmt.Sprintf("Error reading stdout: %v", err)
			stats.errorMessage = errMsg
			log.Printf("[%s] %s from %s", rev, errMsg, relPath)
		}

		// Wait for command to complete
		err = cmd.Wait()
		stderrOutput := <-stderrChan

		if err != nil {
			if stderrOutput != "" {
				stats.errorMessage = fmt.Sprintf("Exit error: %v\nStderr: %s", err, stderrOutput)
				log.Printf("[%s] nix-eval-jobs stderr output for %s: %s", rev, relPath, stderrOutput)
			} else {
				stats.errorMessage = fmt.Sprintf("Exit error: %v", err)
			}
			log.Printf("[%s] nix-eval-jobs for %s exited with error, but processed %d derivations (found %d unique)",
				rev, relPath, jobCount, len(fileVisited))
		} else {
			stats.succeeded = true
			log.Printf("[%s] nix-eval-jobs for %s completed successfully with %d derivations (found %d unique)",
				rev, relPath, jobCount, len(fileVisited))
		}
	}

	// Print the summary table of all expression files and their status
	printExpressionSummary(rev, nixpkgsDir, possiblePaths, fileStats)

	// If we found some jobs, consider it a success regardless of errors
	if totalJobCount > 0 {
		log.Printf("[%s] Total unique derivations found across all expression files: %d", rev, totalJobCount)
		return nil
	}

	// If no jobs were found, try the fallback mechanism
	log.Printf("[%s] No jobs found with nix-eval-jobs, trying fallback with nix-instantiate...", rev)

	// Mark that we're using the fallback mechanism
	*usedFallback = true

	// Add fallback to stats tracking
	fallbackPath := filepath.Join(nixpkgsDir, "fallback-extract.nix")
	relFallbackPath := "fallback-extract.nix"
	fallbackStats := &exprFileStats{
		exists:    false,
		attempted: true,
	}
	fileStats[fallbackPath] = fallbackStats

	// Create a simple Nix file that tries to extract derivations from basic packages
	fallbackNixContent := `
let
  # Try to import with different approaches
  pkgsAttempt = builtins.tryEval (import ./. {});
  
  # Handle different Nixpkgs structures over the years
  pkgs = if pkgsAttempt.success then pkgsAttempt.value else {};
  
  # Try to get various basic utilities that exist in most Nixpkgs versions
  basicPkgsList = [
    (pkgs.stdenv or {}).cc or {}
    pkgs.bash or {}
    pkgs.coreutils or {}
    pkgs.gnutar or {}
    pkgs.gzip or {}
    pkgs.gnused or {}
    pkgs.gnugrep or {}
    # Try older package names too
    pkgs.gcc or {}
    pkgs.binutils or {}
    pkgs.perl or {}
    pkgs.python or {}
  ];
  
  # Try to handle various package structures over the years
  basicPkgs = builtins.filter (p: p != {}) basicPkgsList;
  
  # Just extract the derivation paths for these basic packages
  extractDrvPath = p: 
    if p ? drvPath then p.drvPath
    else if p ? outPath then p.outPath
    else "";
  
  drvPaths = map extractDrvPath (builtins.filter (p: p ? drvPath || p ? outPath) basicPkgs);
in drvPaths
`
	if err := os.WriteFile(fallbackPath, []byte(fallbackNixContent), 0o644); err != nil {
		errMsg := fmt.Sprintf("Failed to write fallback Nix file: %v", err)
		fallbackStats.errorMessage = errMsg
		log.Printf("[%s] %s", rev, errMsg)

		// Print the final status summary including fallback
		printExpressionSummary(rev, nixpkgsDir, possiblePaths, fileStats, fallbackPath)

		return fmt.Errorf("nix-eval-jobs failed and fallback creation failed: %w", err)
	}

	fallbackStats.exists = true

	// Try to evaluate with nix-instantiate
	log.Printf("[%s] Running fallback with nix-instantiate on %s", rev, relFallbackPath)
	fallbackCmd := exec.Command("nix-instantiate", "--eval", "--json", fallbackPath)
	fallbackCmd.Dir = nixpkgsDir // Set working directory to nixpkgs dir
	fallbackOutput, err := fallbackCmd.Output()
	if err != nil {
		errMsg := fmt.Sprintf("Fallback with nix-instantiate failed: %v", err)
		fallbackStats.errorMessage = errMsg
		log.Printf("[%s] %s", rev, errMsg)

		// Try one more fallback with a simpler expression for very old Nixpkgs
		log.Printf("[%s] Trying simpler fallback expression...", rev)
		simpleFallbackContent := `
let
  pkgs = import ./. {};
  drvPaths = [];
in drvPaths
`
		// Add simple fallback to stats
		simpleFallbackPath := filepath.Join(nixpkgsDir, "simple-fallback.nix")
		// simpleFallbackPath_rel := "simple-fallback.nix"
		simpleFallbackStats := &exprFileStats{
			exists:    false,
			attempted: true,
		}
		fileStats[simpleFallbackPath] = simpleFallbackStats

		if err := os.WriteFile(simpleFallbackPath, []byte(simpleFallbackContent), 0o644); err != nil {
			errMsg := fmt.Sprintf("Failed to write simple fallback Nix file: %v", err)
			simpleFallbackStats.errorMessage = errMsg
			log.Printf("[%s] %s", rev, errMsg)

			// Print the final status summary including all fallbacks
			printExpressionSummary(rev, nixpkgsDir, possiblePaths, fileStats, fallbackPath, simpleFallbackPath)

			// At least record that we tried this revision
			log.Printf("[%s] All evaluation methods failed, but recording revision in database", rev)
			return nil
		}

		simpleFallbackStats.exists = true

		// Skip trying to evaluate the simple fallback - it's just a last resort attempt
		// that would likely fail but makes sure we record the revision in the database
		log.Printf("[%s] All evaluation methods failed, but recording revision in database", rev)

		// Print the final status summary including all fallbacks
		printExpressionSummary(rev, nixpkgsDir, possiblePaths, fileStats, fallbackPath, simpleFallbackPath)

		return nil
	}

	// Parse the output
	var drvPaths []string
	if err := json.Unmarshal(fallbackOutput, &drvPaths); err != nil {
		errMsg := fmt.Sprintf("Failed to parse fallback output: %v", err)
		fallbackStats.errorMessage = errMsg
		log.Printf("[%s] %s", rev, errMsg)

		// Print the final status summary including fallback
		printExpressionSummary(rev, nixpkgsDir, possiblePaths, fileStats, fallbackPath)

		return fmt.Errorf("fallback output parsing failed: %w", err)
	}

	// Add the paths to our channel
	fallbackJobCount := 0
	fallbackVisited := make(map[string]bool)

	for _, path := range drvPaths {
		if path != "" {
			fallbackVisited[path] = true
			if !globalVisited[path] {
				globalVisited[path] = true
				totalJobCount++
				drvPathChan <- path
			}
			fallbackJobCount++
		}
	}

	fallbackStats.derivationsFound = len(fallbackVisited)

	if len(fallbackVisited) > 0 {
		fallbackStats.succeeded = true
		log.Printf("[%s] Fallback found %d basic derivations (%d unique)",
			rev, fallbackJobCount, len(fallbackVisited))

		// Print the final status summary including fallback
		printExpressionSummary(rev, nixpkgsDir, possiblePaths, fileStats, fallbackPath)

		return nil
	}

	log.Printf("[%s] Fallback did not find any derivations", rev)
	// Print the final status summary including fallback
	printExpressionSummary(rev, nixpkgsDir, possiblePaths, fileStats, fallbackPath)

	return fmt.Errorf("all evaluation methods failed for %s", rev)
}

// storeEvaluationMetadata persists the evaluation metadata to the database
func storeEvaluationMetadata(db *sql.DB, revisionID int64, stats map[string]*exprFileStats,
	nixpkgsDir string, processingTime time.Duration, usedFallback bool, peakMemoryMB int,
) error {
	// Begin transaction
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	// Deferred function to handle transaction commit/rollback
	var success bool
	defer func() {
		if !success {
			tx.Rollback()
		}
	}()

	// Prepare statement for inserting metadata
	metadataStmt, err := tx.Prepare(`
		INSERT INTO evaluation_metadata (
			revision_id, file_path, file_exists, attempted,
			succeeded, error_message, derivations_found
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(revision_id, file_path) DO UPDATE SET
		file_exists = ?, attempted = ?, succeeded = ?, 
		error_message = ?, derivations_found = ?
	`)
	if err != nil {
		return fmt.Errorf("failed to prepare metadata statement: %w", err)
	}
	defer metadataStmt.Close()

	// Counters for revision-level stats
	var totalFound, totalAttempted, totalSucceeded, totalDerivations int

	// Insert metadata for each file
	for path, stat := range stats {
		relPath, _ := filepath.Rel(nixpkgsDir, path)

		// Convert boolean to int for SQLite
		fileExists := 0
		if stat.exists {
			fileExists = 1
			totalFound++
		}

		attempted := 0
		if stat.attempted {
			attempted = 1
			totalAttempted++
		}

		succeeded := 0
		if stat.succeeded {
			succeeded = 1
			totalSucceeded++
		}

		totalDerivations += stat.derivationsFound

		// Execute the insert
		_, err := metadataStmt.Exec(
			revisionID, relPath, fileExists, attempted,
			succeeded, stat.errorMessage, stat.derivationsFound,
			fileExists, attempted, succeeded,
			stat.errorMessage, stat.derivationsFound)
		if err != nil {
			return fmt.Errorf("failed to insert metadata for %s: %w", relPath, err)
		}
	}

	// Insert revision-level stats
	fallbackUsed := 0
	if usedFallback {
		fallbackUsed = 1
	}

	// Parse CPU cores as integer
	cpuCores := getCPUCores(systemInfo.Cores)

	_, err = tx.Exec(`
		INSERT INTO revision_stats (
			revision_id, total_expressions_found, total_expressions_attempted,
			total_expressions_succeeded, total_derivations_found,
			fallback_used, processing_time_seconds, worker_count, memory_mb_peak,
			system_info, host_name, cpu_model, cpu_cores, memory_total, kernel_version, os_name
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(revision_id) DO UPDATE SET
		total_expressions_found = ?, total_expressions_attempted = ?,
		total_expressions_succeeded = ?, total_derivations_found = ?,
		fallback_used = ?, processing_time_seconds = ?, worker_count = ?, memory_mb_peak = ?,
		system_info = ?, host_name = ?, cpu_model = ?, cpu_cores = ?, memory_total = ?, kernel_version = ?, os_name = ?
	`,
		revisionID, totalFound, totalAttempted, totalSucceeded, totalDerivations,
		fallbackUsed, int(processingTime.Seconds()), workers, peakMemoryMB,
		systemInfoJSON, systemInfo.Host, systemInfo.CPU, cpuCores, systemInfo.Memory, systemInfo.Kernel, systemInfo.OS,
		totalFound, totalAttempted, totalSucceeded, totalDerivations,
		fallbackUsed, int(processingTime.Seconds()), workers, peakMemoryMB,
		systemInfoJSON, systemInfo.Host, systemInfo.CPU, cpuCores, systemInfo.Memory, systemInfo.Kernel, systemInfo.OS)
	if err != nil {
		return fmt.Errorf("failed to insert revision stats: %w", err)
	}

	// Commit the transaction
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	success = true
	return nil
}

func printExpressionSummary(rev, nixpkgsDir string, basePaths []string, stats map[string]*exprFileStats, extraPaths ...string) {
	log.Printf("[%s] Expression file evaluation summary:", rev)
	log.Printf("[%s] %-50s %-10s %-10s %-10s %-10s %s",
		rev, "FILE", "EXISTS", "ATTEMPTED", "SUCCEEDED", "DERIVATIONS", "ERROR")

	// First print the base paths
	for _, path := range basePaths {
		relPath, _ := filepath.Rel(nixpkgsDir, path)
		if s, ok := stats[path]; ok {
			var errorSummary string
			if s.errorMessage != "" {
				// Truncate very long error messages
				if len(s.errorMessage) > 100 {
					errorSummary = s.errorMessage[:97] + "..."
				} else {
					errorSummary = s.errorMessage
				}
			}
			log.Printf("[%s] %-50s %-10t %-10t %-10t %-10d %s",
				rev, relPath, s.exists, s.attempted, s.succeeded,
				s.derivationsFound, errorSummary)
		}
	}

	// Then print any extra paths (fallbacks)
	for _, path := range extraPaths {
		relPath, _ := filepath.Rel(nixpkgsDir, path)
		if s, ok := stats[path]; ok {
			var errorSummary string
			if s.errorMessage != "" {
				// Truncate very long error messages
				if len(s.errorMessage) > 100 {
					errorSummary = s.errorMessage[:97] + "..."
				} else {
					errorSummary = s.errorMessage
				}
			}
			log.Printf("[%s] %-50s %-10t %-10t %-10t %-10d %s",
				rev, relPath, s.exists, s.attempted, s.succeeded,
				s.derivationsFound, errorSummary)
		}
	}
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
		if err := os.MkdirAll(filepath.Dir(mainRepoDir), 0o755); err != nil {
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

	// Get CPU cores as an integer
	// cpuCores := getCPUCores(systemInfo.Cores)

	log.Printf("Configuration: %d worker threads", workers)
	log.Printf("System: %s, CPU: %s (%s), Memory: %s",
		systemInfo.Host, systemInfo.CPU, systemInfo.Cores, systemInfo.Memory)
	log.Printf("OS: %s, Kernel: %s", systemInfo.OS, systemInfo.Kernel)

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
