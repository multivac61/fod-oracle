package main

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"flag"
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

// debugLog logs a message only if debug mode is enabled
func debugLog(format string, v ...interface{}) {
	if config.Debug {
		log.Printf(format, v...)
	}
}

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

// systemInfo holds the neofetch JSON data
var systemInfoJSON string

// systemInfo holds the parsed neofetch data
var systemInfo NeofetchInfo

// Default number of workers for nix-eval-jobs based on available RAM
var workers = 1

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

// getSystemInfo collects information about the system
func getSystemInfo() (NeofetchInfo, string) {
	// Default info based on Go runtime
	info := NeofetchInfo{
		Host:   "unknown",
		CPU:    "unknown",
		Cores:  fmt.Sprintf("%d", runtime.NumCPU()),
		Memory: "unknown",
		Kernel: runtime.GOOS,
		OS:     runtime.GOOS + "/" + runtime.GOARCH,
	}

	// Get hostname
	if hostname, err := os.Hostname(); err == nil {
		info.Host = hostname
	}

	// Try to run neofetch for more detailed info
	cmd := exec.Command("neofetch", "--stdout", "--json")
	output, err := cmd.Output()
	if err != nil {
		jsonData, _ := json.Marshal(info)
		return info, string(jsonData)
	}

	// Parse the JSON output
	jsonStr := string(output)
	if err := json.Unmarshal([]byte(jsonStr), &info); err != nil {
		jsonData, _ := json.Marshal(info)
		return info, string(jsonData)
	}

	return info, jsonStr
}

// init initializes configuration from environment variables
func init() {
	// Collect system information
	systemInfo, systemInfoJSON = getSystemInfo()

	// Initialize config with defaults
	config = Config{
		IsNixExpr:   false,
		WorkerCount: 1,
		Reevaluate:  false,
		BuildDelay:  0, // Default build delay set to 0 for testing (no delay)
	}

	// Check for custom worker count in environment
	if workersEnv := os.Getenv("FOD_ORACLE_NUM_WORKERS"); workersEnv != "" {
		if w, err := strconv.Atoi(workersEnv); err == nil && w > 0 {
			workers = w
			config.WorkerCount = w
		}
	}

	// Format removed - always JSON Lines to stdout

	// Check for build delay in environment
	if buildDelayStr := os.Getenv("FOD_ORACLE_BUILD_DELAY"); buildDelayStr != "" {
		if delay, err := strconv.Atoi(buildDelayStr); err == nil {
			config.BuildDelay = delay
			log.Printf("Using build delay from environment: %d seconds", delay)
		} else {
			log.Printf("Warning: Invalid build delay value in environment: %s", buildDelayStr)
		}
	}
}

// SQL schema for creating tables
var createTables = `
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
    CREATE INDEX IF NOT EXISTS idx_drv_rev_revision_id ON drv_revisions(revision_id);
    
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

    -- Table for FOD rebuild queue and results
    CREATE TABLE IF NOT EXISTS rebuild_queue (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        drv_path TEXT NOT NULL,
        revision_id INTEGER NOT NULL,
        status TEXT NOT NULL DEFAULT 'pending', -- 'pending', 'running', 'success', 'failure', 'timeout', 'hash_mismatch'
        queue_timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
        started_at DATETIME,
        finished_at DATETIME,
        expected_hash TEXT,                    -- From FOD table
        actual_hash TEXT,                      -- Computed during rebuild
        log TEXT,                              -- Output from rebuild-fod
        attempts INTEGER DEFAULT 0,            -- Number of attempts made
        error_message TEXT,                    -- Error message if failed
        FOREIGN KEY (drv_path) REFERENCES fods(drv_path) ON DELETE CASCADE,
        FOREIGN KEY (revision_id) REFERENCES revisions(id) ON DELETE CASCADE
    );
    CREATE INDEX IF NOT EXISTS idx_queue_status ON rebuild_queue(status);
    CREATE INDEX IF NOT EXISTS idx_queue_drv_path ON rebuild_queue(drv_path);
    CREATE INDEX IF NOT EXISTS idx_queue_revision_id ON rebuild_queue(revision_id);
    CREATE UNIQUE INDEX IF NOT EXISTS idx_queue_drv_rev ON rebuild_queue(drv_path, revision_id);
    
    -- Add indexing for better joining performance in reports and file conversions
    CREATE INDEX IF NOT EXISTS idx_rebuild_queue_drv_path_revision_id ON rebuild_queue(drv_path, revision_id);
`

// initInMemoryDB initializes an in-memory SQLite database with temp file storage
func initInMemoryDB() *sql.DB {
	debugLog("Initializing in-memory SQLite database with temp file storage")

	// Connection string for in-memory database with shared cache
	// But using FILE for temp_store to prevent OOM with large datasets
	connString := "file:?mode=memory&cache=shared" +
		"&_journal_mode=MEMORY" +
		"&_synchronous=OFF" +
		"&_cache_size=100000" +
		"&_temp_store=FILE" + // Use FILE instead of MEMORY for temp storage
		"&_busy_timeout=10000" + // 10 second timeout
		"&_locking_mode=NORMAL"

	db, err := sql.Open("sqlite3", connString)
	if err != nil {
		log.Fatalf("Failed to open in-memory database: %v", err)
	}

	// Set connection pool limits
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(time.Minute * 10)

	// Apply optimizations for in-memory database with file-based temp storage
	pragmas := []string{
		"PRAGMA journal_mode=MEMORY",
		"PRAGMA synchronous=OFF",
		"PRAGMA cache_size=100000",
		"PRAGMA temp_store=FILE", // Ensure temp_store is set to FILE
		"PRAGMA foreign_keys=ON",
	}

	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			log.Printf("Warning: Failed to set pragma %s: %v", pragma, err)
		}
	}

	// Create the tables
	_, err = db.Exec(createTables)
	if err != nil {
		log.Fatalf("Failed to create tables in in-memory database: %v", err)
	}

	// Create a connection-specific copy of rebuild_queue table if it doesn't exist
	// This ensures the table exists for all concurrent goroutines
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS rebuild_queue (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			drv_path TEXT NOT NULL,
			revision_id INTEGER NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			queue_timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
			started_at DATETIME,
			finished_at DATETIME,
			expected_hash TEXT,
			actual_hash TEXT,
			log TEXT,
			attempts INTEGER DEFAULT 0,
			error_message TEXT,
			FOREIGN KEY (drv_path) REFERENCES fods(drv_path) ON DELETE CASCADE,
			FOREIGN KEY (revision_id) REFERENCES revisions(id) ON DELETE CASCADE
		);
		CREATE INDEX IF NOT EXISTS idx_queue_status ON rebuild_queue(status);
		CREATE INDEX IF NOT EXISTS idx_queue_drv_path ON rebuild_queue(drv_path);
		CREATE INDEX IF NOT EXISTS idx_queue_revision_id ON rebuild_queue(revision_id);
		CREATE INDEX IF NOT EXISTS idx_queue_drv_rev ON rebuild_queue(drv_path, revision_id);
	`)
	if err != nil {
		log.Printf("Warning: Failed to create rebuild_queue table: %v", err)
	}

	return db
}

// initPersistentDB initializes a persistent SQLite database file
func initPersistentDB(dbFile string) *sql.DB {
	debugLog("Initializing persistent SQLite database: %s", dbFile)

	// Connection string for file-based database with optimizations
	connString := dbFile +
		"?_journal_mode=WAL" +
		"&_synchronous=NORMAL" +
		"&_cache_size=100000" +
		"&_temp_store=MEMORY" +
		"&_busy_timeout=10000" +
		"&_locking_mode=NORMAL"

	db, err := sql.Open("sqlite3", connString)
	if err != nil {
		log.Fatalf("Failed to open persistent database: %v", err)
	}

	// Set connection pool limits
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(time.Minute * 10)

	// Apply optimizations for persistent database
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA cache_size=100000",
		"PRAGMA temp_store=MEMORY",
		"PRAGMA busy_timeout=10000",
		"PRAGMA foreign_keys=ON",
	}

	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			debugLog("Warning: Failed to set pragma %s: %v", pragma, err)
		}
	}

	// Create tables
	if _, err := db.Exec(createTables); err != nil {
		log.Fatalf("Failed to create tables: %v", err)
	}

	debugLog("Persistent database initialized successfully")
	return db
}

// initDB initializes the SQLite database
func initDB() *sql.DB {
	// For SQLite output format, use the normal file-based database
	dbPath := os.Getenv("FOD_ORACLE_DB_PATH")
	if dbPath == "" {
		// Default to the db directory in the current working directory
		currentDir, err := os.Getwd()
		if err != nil {
			log.Fatalf("Failed to get current directory: %v", err)
		}
		dbPath = filepath.Join(currentDir, "db", "fods.db")
	}

	// Create the database directory if it doesn't exist
	dbDir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		log.Fatalf("Failed to create database directory %s: %v", dbDir, err)
	}

	debugLog("Using database at: %s with file-based temp storage", dbPath)

	// Add busy_timeout and other optimizations
	// Use FILE for temp_store to prevent OOM with large datasets
	connString := dbPath + "?_journal_mode=WAL" +
		"&_synchronous=NORMAL" +
		"&_cache_size=100000" +
		"&_temp_store=FILE" + // Use FILE instead of MEMORY for temp storage
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
		"PRAGMA temp_store=FILE", // Ensure temp_store is set to FILE
		"PRAGMA mmap_size=30000000000",
		"PRAGMA page_size=32768",
		"PRAGMA foreign_keys=ON",
	}

	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			log.Printf("Warning: Failed to set pragma %s: %v", pragma, err)
		}
	}

	// Using the createTables variable defined at package level
	_, err = db.Exec(createTables)
	if err != nil {
		log.Fatalf("Failed to create tables: %v", err)
	}

	// Create a connection-specific copy of rebuild_queue table if it doesn't exist
	// This ensures the table exists for all concurrent goroutines
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS rebuild_queue (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			drv_path TEXT NOT NULL,
			revision_id INTEGER NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			queue_timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
			started_at DATETIME,
			finished_at DATETIME,
			expected_hash TEXT,
			actual_hash TEXT,
			log TEXT,
			attempts INTEGER DEFAULT 0,
			error_message TEXT,
			FOREIGN KEY (drv_path) REFERENCES fods(drv_path) ON DELETE CASCADE,
			FOREIGN KEY (revision_id) REFERENCES revisions(id) ON DELETE CASCADE
		);
		CREATE INDEX IF NOT EXISTS idx_queue_status ON rebuild_queue(status);
		CREATE INDEX IF NOT EXISTS idx_queue_drv_path ON rebuild_queue(drv_path);
		CREATE INDEX IF NOT EXISTS idx_queue_revision_id ON rebuild_queue(revision_id);
		CREATE INDEX IF NOT EXISTS idx_queue_drv_rev ON rebuild_queue(drv_path, revision_id);
	`)
	if err != nil {
		log.Printf("Warning: Failed to create rebuild_queue table: %v", err)
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
	
	// Stream FOD to web interface in real-time (non-blocking)
	go func() {
		evaluationID := fmt.Sprintf("eval-%d", b.revisionID)
		streamFODRealtime(fod, evaluationID)
	}()

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

// AddRebuildInfo adds rebuild information for a FOD
// For DBBatcher, this is a no-op as the data is already in the SQLite database
func (b *DBBatcher) AddRebuildInfo(drvPath string, status, actualHash, errorMessage string) {
	// No-op for DBBatcher since the rebuild data is already in the database
	// The data will be retrieved directly from the rebuild_queue table as needed
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
	batcher        Writer // Interface for adding FODs
	visited        *sync.Map
	processedPaths *sync.Map // Cache for already processed derivation paths
	wg             *sync.WaitGroup
	semaphore      chan struct{} // Limit concurrency
}

func processDerivation(inputFile string, ctx *ProcessingContext) {
	ctx.batcher.IncrementDrvCount()
	
	if config.Debug {
		debugLog("PROCESS: Starting derivation %s", inputFile)
	}

	// Check if we've already processed this derivation
	if _, alreadyProcessed := ctx.processedPaths.Load(inputFile); alreadyProcessed {
		if config.Debug {
			debugLog("PROCESS: Skipping already processed derivation %s", inputFile)
		}
		return
	}

	// Mark this path as processed before we begin
	ctx.processedPaths.Store(inputFile, true)

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

func findFODsForRevision(rev string, revisionID int64, db *sql.DB, writer Writer) error {
	revStartTime := time.Now()
	log.Printf("[%s] Starting to find FODs...", rev)

	// Prepare worktree
	worktreeDir, err := prepareNixpkgsWorktree(rev)
	if err != nil {
		return fmt.Errorf("failed to prepare worktree: %w", err)
	}
	defer os.RemoveAll(worktreeDir)

	// Use the writer passed from main

	// Setup for concurrency and memory tracking
	visited := &sync.Map{}
	var wg sync.WaitGroup
	var peakMemoryMB int
	var memoryMutex sync.Mutex

	// Start memory monitoring
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)

			allocMB := int(m.Alloc / 1024 / 1024)
			memoryMutex.Lock()
			if allocMB > peakMemoryMB {
				peakMemoryMB = allocMB
			}
			memoryMutex.Unlock()
		}
	}()

	// Create processing context
	ctx := &ProcessingContext{
		batcher:        writer, // Use the writer interface
		visited:        visited,
		processedPaths: &sync.Map{}, // Cache for processed derivations
		wg:             &wg,
		semaphore:      make(chan struct{}, 20000), // Increased concurrency limit
	}

	// Channel for derivation paths
	drvPathChan := make(chan string, 200000) // Increased channel buffer
	done := make(chan struct{})

	// Start processor goroutine
	go func() {
		for drvPath := range drvPathChan {
			if _, loaded := visited.LoadOrStore(drvPath, true); !loaded {
				// Try to process in new goroutine
				select {
				case ctx.semaphore <- struct{}{}:
					wg.Add(1)
					go func(path string) {
						defer wg.Done()
						defer func() { <-ctx.semaphore }()
						processDerivation(path, ctx)
					}(drvPath)
				default:
					// Process in current goroutine if semaphore full
					processDerivation(drvPath, ctx)
				}
			}
		}
		wg.Wait()
		close(done)
	}()

	// Evaluation variables
	usedFallback := false
	fileStats := make(map[string]*exprFileStats)

	// Run nix-eval-jobs
	debugLog("[%s] Running nix-eval-jobs with %d workers", rev, workers)
	if err := streamNixEvalJobs(rev, worktreeDir, workers, drvPathChan, fileStats, &usedFallback); err != nil {
		close(drvPathChan)

		// Store metadata even on error
		memoryMutex.Lock()
		currentPeakMemory := peakMemoryMB
		memoryMutex.Unlock()
		storeEvaluationMetadata(db, revisionID, fileStats, worktreeDir,
			time.Since(revStartTime), usedFallback, currentPeakMemory)

		return fmt.Errorf("failed to run nix-eval-jobs: %w", err)
	}
	close(drvPathChan)

	// Wait for completion or timeout
	select {
	case <-done:
		log.Printf("[%s] All derivations processed", rev)
	case <-time.After(1 * time.Hour):
		log.Printf("[%s] Processing timed out after 1 hour", rev)
	}

	// Flush and get stats
	writer.Flush()
	revElapsed := time.Since(revStartTime)

	// Log completion
	debugLog("[%s] Processing completed in %v", rev, revElapsed)

	// Always store metadata in SQLite for reporting purposes
	memoryMutex.Lock()
	finalPeakMemory := peakMemoryMB
	memoryMutex.Unlock()

	storeEvaluationMetadata(db, revisionID, fileStats, worktreeDir,
		revElapsed, usedFallback, finalPeakMemory)

	return nil
}

// streamNixEvalJobs runs nix-eval-jobs and streams results to the provided channel
func streamNixEvalJobs(rev string, nixpkgsDir string, workers int, drvPathChan chan<- string,
	fileStats map[string]*exprFileStats, usedFallback *bool,
) error {
	debugLog("[%s] Running nix-eval-jobs with nixpkgs at %s (%d workers)", rev, nixpkgsDir, workers)

	// Define possible Nix expression files to evaluate
	possiblePaths := []string{
		filepath.Join(nixpkgsDir, "pkgs/top-level/release-outpaths.nix"),
		// filepath.Join(nixpkgsDir, "pkgs/top-level/release.nix"),
		// filepath.Join(nixpkgsDir, "pkgs/top-level/release-small.nix"),
		// filepath.Join(nixpkgsDir, "pkgs/top-level/all-packages.nix"),
		// filepath.Join(nixpkgsDir, "nixos/release.nix"),
		// filepath.Join(nixpkgsDir, "nixos/release-small.nix"),
		// filepath.Join(nixpkgsDir, "nixos/release-combined.nix"),
		// filepath.Join(nixpkgsDir, "all-packages.nix"),
		// filepath.Join(nixpkgsDir, "default.nix"),
	}

	// Check which expression files exist
	var existingPaths []string
	for _, path := range possiblePaths {
		relPath, _ := filepath.Rel(nixpkgsDir, path)
		fileStats[path] = &exprFileStats{}

		if _, err := os.Stat(path); err == nil {
			fileStats[path].exists = true
			existingPaths = append(existingPaths, path)
			log.Printf("[%s] Found: %s", rev, relPath)
		}
	}

	if len(existingPaths) == 0 {
		log.Printf("[%s] No suitable Nix files found, will try fallback", rev)
	}

	// Global deduplication map
	totalJobCount := 0
	globalVisited := make(map[string]bool)

	// Evaluate each expression file
	for _, expressionPath := range existingPaths {
		relPath, _ := filepath.Rel(nixpkgsDir, expressionPath)
		stats := fileStats[expressionPath]
		stats.attempted = true

		// Build the command with additional options
		args := []string{
			expressionPath,
			"--arg", "checkMeta", "false",
			"--workers", fmt.Sprintf("%d", workers),
			"--option", "allow-import-from-derivation", "false",
			"--option", "system-features", "nixos-test benchmark big-parallel kvm",
			"--option", "allow-unsupported-system", "true", // Allow evaluating all platforms
		}

		// Check for additional options in environment
		if extraOpts := os.Getenv("FOD_ORACLE_EVAL_OPTS"); extraOpts != "" {
			extraArgs := strings.Fields(extraOpts)
			args = append(args, extraArgs...)
		}

		cmd := exec.Command("nix-eval-jobs", args...)

		// Capture stdout and stderr
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			stats.errorMessage = fmt.Sprintf("Failed to create stdout pipe: %v", err)
			continue
		}

		stderr, err := cmd.StderrPipe()
		if err != nil {
			stats.errorMessage = fmt.Sprintf("Failed to create stderr pipe: %v", err)
			continue
		}

		// Start capturing stderr
		stderrChan := make(chan string, 1)
		go func() {
			stderrBytes, _ := io.ReadAll(stderr)
			stderrChan <- string(stderrBytes)
		}()

		if err = cmd.Start(); err != nil {
			stats.errorMessage = fmt.Sprintf("Failed to start nix-eval-jobs: %v", err)
			continue
		}

		// Process stdout
		scanner := bufio.NewScanner(stdout)
		const maxScannerSize = 100 * 1024 * 1024 // Increase to 100MB
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
				continue
			}

			// Add to channel if not already processed
			if result.DrvPath != "" {
				fileVisited[result.DrvPath] = true
				if !globalVisited[result.DrvPath] {
					globalVisited[result.DrvPath] = true
					totalJobCount++
					if config.Debug {
						debugLog("QUEUE: Sending derivation %s to processing", result.DrvPath)
					}
					drvPathChan <- result.DrvPath
				}

				jobCount++
				if jobCount%1000 == 0 {
					log.Printf("[%s] Processed %d jobs from %s (unique: %d, total: %d)",
						rev, jobCount, relPath, len(fileVisited), totalJobCount)
				}
			}
		}

		stats.derivationsFound = len(fileVisited)

		if err := scanner.Err(); err != nil {
			stats.errorMessage = fmt.Sprintf("Error reading stdout: %v", err)
		}

		// Wait for command to complete
		err = cmd.Wait()
		stderrOutput := <-stderrChan

		if err != nil {
			if stderrOutput != "" {
				stats.errorMessage = fmt.Sprintf("Error: %v\nStderr: %s", err, stderrOutput)
			} else {
				stats.errorMessage = fmt.Sprintf("Error: %v", err)
			}
		} else {
			stats.succeeded = true
		}
	}

	// Print summary of expression file status
	printExpressionSummary(rev, nixpkgsDir, possiblePaths, fileStats)

	// If we found some jobs, consider it a success
	if totalJobCount > 0 {
		log.Printf("[%s] Found %d unique derivations across all files", rev, totalJobCount)
		return nil
	}

	// Try fallback mechanism if no jobs found
	log.Printf("[%s] No jobs found, trying fallback...", rev)
	*usedFallback = true

	// Create fallback Nix file
	fallbackPath := filepath.Join(nixpkgsDir, "fallback-extract.nix")
	fallbackStats := &exprFileStats{
		exists:    false,
		attempted: true,
	}
	fileStats[fallbackPath] = fallbackStats

	// Create a simple Nix file for fallback
	fallbackNixContent := `
let
  pkgsAttempt = builtins.tryEval (import ./. {});
  pkgs = if pkgsAttempt.success then pkgsAttempt.value else {};
  
  basicPkgsList = [
    (pkgs.stdenv or {}).cc or {}
    pkgs.bash or {}
    pkgs.coreutils or {}
    pkgs.gnutar or {}
    pkgs.gzip or {}
    pkgs.gnused or {}
    pkgs.gnugrep or {}
    pkgs.gcc or {}
    pkgs.binutils or {}
    pkgs.perl or {}
    pkgs.python or {}
  ];
  
  basicPkgs = builtins.filter (p: p != {}) basicPkgsList;
  
  extractDrvPath = p: 
    if p ? drvPath then p.drvPath
    else if p ? outPath then p.outPath
    else "";
  
  drvPaths = map extractDrvPath (builtins.filter (p: p ? drvPath || p ? outPath) basicPkgs);
in drvPaths
`
	// Write fallback file and mark as existing
	if err := os.WriteFile(fallbackPath, []byte(fallbackNixContent), 0o644); err != nil {
		fallbackStats.errorMessage = fmt.Sprintf("Failed to write fallback file: %v", err)
		printExpressionSummary(rev, nixpkgsDir, possiblePaths, fileStats, fallbackPath)
		return fmt.Errorf("nix-eval-jobs failed and fallback failed: %w", err)
	}
	fallbackStats.exists = true

	// Try to evaluate with nix-instantiate
	log.Printf("[%s] Running fallback with nix-instantiate", rev)
	fallbackCmd := exec.Command("nix-instantiate", "--eval", "--json", fallbackPath)
	fallbackCmd.Dir = nixpkgsDir
	fallbackOutput, err := fallbackCmd.Output()
	// Handle fallback failure
	if err != nil {
		fallbackStats.errorMessage = fmt.Sprintf("Fallback failed: %v", err)
		log.Printf("[%s] All evaluation methods failed, recording revision anyway", rev)
		printExpressionSummary(rev, nixpkgsDir, possiblePaths, fileStats, fallbackPath)
		return nil
	}

	// Parse the output
	var drvPaths []string
	if err := json.Unmarshal(fallbackOutput, &drvPaths); err != nil {
		fallbackStats.errorMessage = fmt.Sprintf("Failed to parse fallback output: %v", err)
		printExpressionSummary(rev, nixpkgsDir, possiblePaths, fileStats, fallbackPath)
		return fmt.Errorf("fallback output parsing failed: %w", err)
	}

	// Add paths to channel
	fallbackVisited := make(map[string]bool)
	for _, path := range drvPaths {
		if path != "" {
			fallbackVisited[path] = true
			if !globalVisited[path] {
				globalVisited[path] = true
				totalJobCount++
				drvPathChan <- path
			}
		}
	}

	fallbackStats.derivationsFound = len(fallbackVisited)

	if len(fallbackVisited) > 0 {
		fallbackStats.succeeded = true
		log.Printf("[%s] Fallback found %d derivations", rev, len(fallbackVisited))
		return nil
	}

	log.Printf("[%s] Fallback found no derivations", rev)
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
	// Note: debugLog can't be used here since config isn't set yet

	// Parse command-line flags
	var (
		workersFlag    = flag.Int("workers", workers, "Number of worker threads")
		testMode       = flag.Bool("test", false, "Test mode - process a single derivation")
		testDrv        = flag.String("drv", "", "Derivation path for test mode")
		nixExpr        = flag.Bool("expr", false, "Process a Nix expression instead of a revision")
		reevaluateFlag = flag.Bool("reevaluate", false, "Reevaluate FODs by rebuilding them")
		buildDelayFlag = flag.Int("build-delay", config.BuildDelay, "Delay between builds in seconds")
		parallelFlag   = flag.Int("parallel", 1, "Number of parallel rebuild workers (default: 1, use higher values for testing)")
		debugFlag      = flag.Bool("debug", false, "Enable debug logging")
		helpFlag       = flag.Bool("help", false, "Show help")
		webFlag        = flag.Bool("web", false, "Start web interface with real-time FOD streaming")
		portFlag       = flag.Int("port", 8080, "Port for web interface")
		hostFlag       = flag.String("host", "127.0.0.1", "Host address for web interface (e.g., 0.0.0.0 for all interfaces)")
	)

	// Use custom flag parsing to separate flags from positional arguments
	// This ensures flags like -format and their values don't get treated as revisions
	flag.Parse()

	// Set debug flag early so we can use it for subsequent logging
	config.Debug = *debugFlag

	// Set reevaluate flag early too
	config.Reevaluate = *reevaluateFlag
	// Always check the raw args because flag parsing doesn't handle --flag style well
	for _, arg := range os.Args {
		if arg == "-reevaluate" || arg == "--reevaluate" {
			config.Reevaluate = true
			break
		}
	}

	// Now we can use debugLog since config is set
	debugLog("Starting FOD finder...")

	// Debug: Print all flag values only in debug mode
	debugLog("DEBUG: Flag values - reevaluate: %v", *reevaluateFlag)

	if *helpFlag {
		fmt.Printf("FOD Oracle - A tool for finding Fixed-Output Derivations in Nix packages\n\n")
		fmt.Printf("Usage: %s [options] <nixpkgs-revision> [<nixpkgs-revision2> ...]\n", os.Args[0])
		fmt.Printf("       %s --expr \"let pkgs = import <nixpkgs> {}; in pkgs.hello\" [--reevaluate]\n", os.Args[0])
		fmt.Printf("       %s --expr \"github:NixOS/nixpkgs/nixos-unstable#legacyPackages.x86_64-linux.hello\" [--reevaluate]\n\n", os.Args[0])
		fmt.Printf("Flake URI Examples:\n")
		fmt.Printf("  github:NixOS/nixpkgs/nixos-unstable#legacyPackages.x86_64-linux.obs-studio-plugins\n")
		fmt.Printf("  github:NixOS/nixpkgs#nixosConfigurations.myConfig\n")
		fmt.Printf("  gitlab:user/repo#myPackage\n\n")
		fmt.Printf("Options:\n")
		flag.PrintDefaults()
		fmt.Printf("\nJSON Lines Output:\n")
		fmt.Printf("  All output is streamed as JSON Lines to stdout for easy parsing.\n")
		fmt.Printf("  Without --reevaluate: Basic FOD information (DrvPath, OutputPath, HashAlgorithm, Hash)\n")
		fmt.Printf("  With --reevaluate: FOD information plus rebuild results (rebuild_status, actual_hash, hash_mismatch, error_message)\n")
		fmt.Printf("  Use --debug to show debug logs, otherwise only JSON output is shown.\n")
		fmt.Printf("\nEnvironment Variables:\n")
		fmt.Printf("  FOD_ORACLE_NUM_WORKERS   Number of worker threads (default: 1)\n")
		fmt.Printf("  FOD_ORACLE_TEST_DRV_PATH Path to derivation for test mode\n")
		fmt.Printf("  FOD_ORACLE_EVAL_OPTS     Additional options for nix-eval-jobs\n")
		fmt.Printf("  FOD_ORACLE_BUILD_DELAY   Delay between builds in seconds (default: 0)\n")
		fmt.Printf("\nWeb Interface:\n")
		fmt.Printf("  --web         Enable real-time web interface during evaluation\n")
		fmt.Printf("  --port        Port for web interface (default: 8080)\n")
		fmt.Printf("  --host        Host address for web interface (default: 127.0.0.1, use 0.0.0.0 for all interfaces)\n")
		return
	}

	// Handle web-only mode (no evaluation)
	if *webFlag && len(flag.Args()) == 0 && !*nixExpr {
		if err := startWebInterface(*portFlag, *hostFlag); err != nil {
			log.Fatalf("Failed to start web interface: %v", err)
		}
		return
	}

	// No format processing needed - always JSON Lines to stdout

	if *workersFlag > 0 {
		workers = *workersFlag
		config.WorkerCount = *workersFlag
	}

	if *nixExpr {
		config.IsNixExpr = true
	}

	// Reevaluate flag already set earlier

	// Apply the build delay from the flag, overriding environment and default values
	if *buildDelayFlag != config.BuildDelay {
		debugLog("Setting build delay to %d seconds (from command-line flag)", *buildDelayFlag)
		config.BuildDelay = *buildDelayFlag
	}
	// Set parallel workers
	if *parallelFlag > 0 {
		config.ParallelWorkers = *parallelFlag
		if config.ParallelWorkers > 1 {
			debugLog("Using %d parallel rebuild workers (faster testing mode)", config.ParallelWorkers)
		}
	} else {
		config.ParallelWorkers = 1
	}

	// Debug flag already set earlier

	// Debug info only shown when debug mode is enabled
	debugLog("Using %d worker threads on %s (%s), %s",
		workers, systemInfo.CPU, systemInfo.Cores, systemInfo.OS)
	debugLog("Output format: JSON Lines to stdout")

	// Clean up any leftover worktrees
	cleanupWorktrees()

	// Use persistent database if web flag is set (for real-time streaming), otherwise in-memory
	var db *sql.DB
	if *webFlag {
		db = initPersistentDB("./fod-oracle.db")
		log.Printf("Using persistent database for real-time web streaming")
		
		// Start web interface in background
		go func() {
			time.Sleep(1 * time.Second) // Give time for database to initialize
			if err := startWebInterfaceWithRealtime(*portFlag, *hostFlag, db); err != nil {
				log.Printf("Web interface error: %v", err)
			}
		}()
		log.Printf("Web interface will be available at http://%s:%d/realtime", *hostFlag, *portFlag)
	} else {
		db = initInMemoryDB()
	}
	defer db.Close()

	// Get revisions from command line
	revisions := flag.Args()

	// Validate that the provided arguments appear to be Git commit hashes
	// and not misinterpreted flags
	validRevisions := []string{}
	for _, rev := range revisions {
		// Skip any flags that got parsed as positional arguments
		if rev == "--reevaluate" {
			config.Reevaluate = true
			continue
		} else if rev == "--build-delay" || strings.HasPrefix(rev, "--build-delay=") {
			// Skip the --build-delay flag
			continue
		} else if strings.HasPrefix(rev, "-") {
			// Skip other flags
			continue
		} else if i, err := strconv.Atoi(rev); err == nil && i >= 0 {
			// This is a number, likely the value for build-delay, skip it
			continue
		}

		// If we are in Nix expression mode, do not validate the revision length
		if *nixExpr {
			validRevisions = append(validRevisions, rev)
			continue
		}

		// Skip arguments that are too short to be git hashes (only in Git revision mode)
		if len(rev) < 7 {
			// Only warn if it's not a number (which would be a flag value)
			_, err := strconv.Atoi(rev)
			if err != nil {
				log.Printf("Warning: Skipping invalid revision: %s (too short to be a git hash)", rev)
			}
			continue
		}

		validRevisions = append(validRevisions, rev)
	}

	if len(validRevisions) < 1 && !*testMode && !*nixExpr {
		log.Fatalf("Usage: %s [options] <nixpkgs-revision> [<nixpkgs-revision2> ...]\nUse --help for more information", os.Args[0])
	}

	debugLog("Processing %d nixpkgs revisions", len(validRevisions))

	// Replace revisions with validRevisions
	revisions = validRevisions

	startTime := time.Now()

	// Check for test mode
	testDrvPath := os.Getenv("FOD_ORACLE_TEST_DRV_PATH")
	if *testMode {
		if *testDrv != "" {
			testDrvPath = *testDrv
		}
	}
	isTestMode := testDrvPath != ""

	// Process revisions sequentially
	for _, rev := range revisions {

		revisionID, err := getOrCreateRevision(db, rev)
		if err != nil {
			log.Printf("Failed to get or create revision %s: %v", rev, err)
			continue
		}

		// Create the writer (always a DBBatcher now)
		writer, err := GetWriter(db, revisionID, rev)
		if err != nil {
			log.Printf("Error creating writer: %v", err)
			continue
		}

		// Handle different processing modes
		if isTestMode {
			log.Printf("Running in test mode with derivation path: %s", testDrvPath)
			if err := processTestDerivation(testDrvPath, revisionID, db, writer); err != nil {
				log.Printf("Error processing test derivation: %v", err)
			}
		} else if *nixExpr {
			// Process as Nix expression rather than as a Git revision
			// Set IsNixExpr before processing to ensure reevaluation works correctly
			config.IsNixExpr = true
			if err := processNixExpression(rev, revisionID, db, writer); err != nil {
				log.Printf("Error processing Nix expression: %v", err)
			}
		} else {
			// Normal mode - process as a Git revision
			if err := findFODsForRevision(rev, revisionID, db, writer); err != nil {
				log.Printf("Error finding FODs for revision %s: %v", rev, err)
			}
		}

		// Handle reevaluation if requested
		if config.Reevaluate {
			if err := reevaluateFODs(db, revisionID, rev, writer); err != nil {
				log.Printf("Error reevaluating FODs for revision %s: %v", rev, err)
			}
		}

		// Close the writer
		writer.Close()
	}

	// Optimize database
	for _, opt := range []string{"PRAGMA optimize", "VACUUM", "ANALYZE"} {
		db.Exec(opt)
	}

	// Final cleanup
	cleanupWorktrees()

	debugLog("All revisions processed in %v", time.Since(startTime))
}

// processTestDerivation processes a single derivation for testing purposes
func processTestDerivation(drvPath string, revisionID int64, db *sql.DB, writer Writer) error {
	log.Printf("Processing test derivation: %s", drvPath)

	// Mark this as a test/Nix expression
	config.IsNixExpr = true

	// Use the writer passed from main

	// Set up a context for processing
	visited := &sync.Map{}
	var wg sync.WaitGroup

	ctx := &ProcessingContext{
		batcher:        writer,
		visited:        visited,
		processedPaths: &sync.Map{},
		wg:             &wg,
		semaphore:      make(chan struct{}, 5),
	}

	// Manually read the derivation for debugging
	file, err := os.Open(drvPath)
	if err != nil {
		log.Printf("Error opening file %s: %v", drvPath, err)
		return fmt.Errorf("error opening file: %w", err)
	}
	defer file.Close()

	drv, err := derivation.ReadDerivation(file)
	if err != nil {
		log.Printf("Error reading derivation %s: %v", drvPath, err)
		return fmt.Errorf("error reading derivation: %w", err)
	}

	// Debug output - print the outputs and their properties
	log.Printf("Derivation has %d outputs", len(drv.Outputs))
	for name, out := range drv.Outputs {
		log.Printf("Output %s: Path=%s, HashAlgo=%s, Hash=%s",
			name, out.Path, out.HashAlgorithm, out.Hash)
	}

	// Process the derivation
	processDerivation(drvPath, ctx)

	// Flush the writer
	writer.Flush()

	// Log completion
	debugLog("Processed test derivation successfully")

	return nil
}

// reevaluateFODs handles the reevaluation of FODs for a given revision or expression
func reevaluateFODs(db *sql.DB, revisionID int64, rev string, writer Writer) error {
	identifier := rev
	if config.IsNixExpr {
		identifier = "expr"
	}

	debugLog("[%s] Starting FOD reevaluation...", identifier)

	// Create the rebuild queue with the configured parallel workers and build delay
	queue := NewRebuildQueue(db, config.ParallelWorkers, config.BuildDelay)

	// Queue FODs for reevaluation
	count, queueErr := queue.QueueFODsForRevision(revisionID)
	if queueErr != nil {
		return fmt.Errorf("failed to queue FODs: %w", queueErr)
	}

	// If no FODs were queued but we know there should be some, try force queuing
	if count == 0 {
		var fodCount int
		if config.IsNixExpr {
			queueErr = db.QueryRow("SELECT COUNT(*) FROM fods").Scan(&fodCount)
		} else {
			queueErr = db.QueryRow("SELECT COUNT(*) FROM drv_revisions WHERE revision_id = ?", revisionID).Scan(&fodCount)
		}

		if queueErr != nil {
			debugLog("Error counting FODs: %v", queueErr)
		} else if fodCount > 0 {
			// Force queue FODs if there are some in the database
			debugLog("[%s] Found %d FODs in database but none in queue. Force queuing them...", identifier, fodCount)
			count, queueErr = queue.ForceQueueAllFODs(revisionID)
			if queueErr != nil {
				return fmt.Errorf("failed to force queue FODs: %w", queueErr)
			}
		}
	}

	if count == 0 {
		debugLog("[%s] No FODs to reevaluate", identifier)
		return nil
	}

	// Always show the configured settings
	delayMessage := fmt.Sprintf("build delay: %d seconds", config.BuildDelay)
	if config.BuildDelay <= 0 {
		delayMessage = "build delay: DISABLED (0 seconds)"
	}

	if config.ParallelWorkers > 1 {
		debugLog("[%s] Queued %d FODs for reevaluation (parallel workers: %d, %s)",
			identifier, count, config.ParallelWorkers, delayMessage)
	} else {
		debugLog("[%s] Queued %d FODs for reevaluation (%s)",
			identifier, count, delayMessage)
	}

	// Start the queue runner with the configured concurrency and pass the writer
	queue.Start(config.ParallelWorkers, writer)

	// Setup for progress reporting
	var maxWaitTime time.Duration = 10 * time.Minute
	startTime := time.Now()

	// Use a status ticker for progress updates only
	statusTicker := time.NewTicker(5 * time.Second) // Status updates every 5 seconds
	defer statusTicker.Stop()

	// Setup a timeout to prevent hanging
	timeout := time.NewTimer(maxWaitTime)
	defer timeout.Stop()

	// Wait for completion or provide status updates
	for {
		select {
		case <-timeout.C:
			// Safety timeout to prevent hanging
			debugLog("[%s] Reevaluation timed out after %v", identifier, maxWaitTime)
			queue.Stop() // Force the queue to stop

			// Try to get final stats
			stats, err := queue.GetQueueStats(revisionID)
			if err == nil && stats["total"] > 0 {
				completed := stats["success"] + stats["hash_mismatch"] + stats["failure"] + stats["timeout"]
				debugLog("[%s] Partial results: %d/%d complete (%d%%)",
					identifier, completed, stats["total"],
					int(float64(completed)/float64(stats["total"])*100))
			}
			return fmt.Errorf("rebuild queue timed out after %v", maxWaitTime)

		case <-queue.Done():
			// Queue is completely finished - immediate exit!
			debugLog("[%s] Queue completion signal received - exiting immediately", identifier)
			stats, err := queue.GetQueueStats(revisionID)
			if err != nil {
				debugLog("[%s] Error getting final queue stats: %v", identifier, err)
			} else {
				debugLog("[%s] Reevaluation complete. Total: %d, Success: %d, Hash Mismatch: %d, Failure: %d, Timeout: %d",
					identifier, stats["total"], stats["success"], stats["hash_mismatch"], stats["failure"], stats["timeout"])
			}
			return nil

		case <-statusTicker.C:
			// Provide status update (every 5 seconds)
			stats, err := queue.GetQueueStats(revisionID)
			if err != nil {
				debugLog("[%s] Error getting queue stats: %v", identifier, err)
				continue
			}

			completed := stats["success"] + stats["hash_mismatch"] + stats["failure"] + stats["timeout"]
			total := stats["total"]

			if total > 0 {
				percentComplete := float64(completed) / float64(total) * 100
				elapsedTime := time.Since(startTime)
				debugLog("[%s] Reevaluation progress: %.1f%% (%d/%d complete, elapsed: %v). Success: %d, Hash Mismatch: %d, Failure: %d, Timeout: %d",
					identifier, percentComplete, completed, total, elapsedTime.Round(time.Second),
					stats["success"], stats["hash_mismatch"], stats["failure"], stats["timeout"])
			}
		}
	}
}
