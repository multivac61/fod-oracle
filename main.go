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
	Cores        string `json:"CPU Cores"` // Note: This might be physical or logical cores depending on neofetch version/config
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

// Default number of workers for nix-eval-jobs, will be set based on CPU cores or env var in init()
var nixEvalJobsWorkers int

// systemInfo holds the neofetch JSON data
var systemInfoJSON string

// systemInfo holds the parsed neofetch data
var systemInfo NeofetchInfo

// getCPUCores extracts the number of logical cores from neofetch CPU cores field or runtime
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

	// Try to parse as a single number (likely logical cores)
	if cores, err := strconv.Atoi(strings.TrimSpace(coresStr)); err == nil {
		return cores
	}

	// Fallback to runtime.NumCPU() which returns logical cores
	log.Printf("Warning: Could not parse CPU cores string '%s'. Falling back to runtime.NumCPU()", coresStr)
	return runtime.NumCPU()
}

// getSystemInfo collects information about the system using neofetch
func getSystemInfo() (NeofetchInfo, string) {
	// Default fallback info using runtime package
	logicalCores := runtime.NumCPU()
	info := NeofetchInfo{
		Host:   "unknown",
		CPU:    "unknown",
		Cores:  fmt.Sprintf("%d", logicalCores), // Default to logical cores
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
		// Use the initial info with runtime data
		jsonData, _ := json.Marshal(info)
		return info, string(jsonData)
	}

	// Ensure Cores field reflects logical cores if possible
	info.Cores = fmt.Sprintf("%d", getCPUCores(info.Cores))

	return info, jsonStr
}

// init initializes configuration from environment variables
func init() {
	// Collect system information
	var jsonStr string
	systemInfo, jsonStr = getSystemInfo()
	systemInfoJSON = jsonStr

	// Set the default number of workers for nix-eval-jobs to the number of logical CPU cores
	nixEvalJobsWorkers = runtime.NumCPU()

	// Check for custom worker count for nix-eval-jobs in environment
	if workersEnv := os.Getenv("FOD_ORACLE_NIX_EVAL_WORKERS"); workersEnv != "" {
		if w, err := strconv.Atoi(workersEnv); err == nil && w > 0 {
			nixEvalJobsWorkers = w
			log.Printf("Using %d workers for nix-eval-jobs from FOD_ORACLE_NIX_EVAL_WORKERS environment variable", nixEvalJobsWorkers)
		} else if err != nil {
			log.Printf("Warning: Invalid FOD_ORACLE_NIX_EVAL_WORKERS value '%s': %v. Using default: %d (logical CPU cores)",
				workersEnv, err, nixEvalJobsWorkers)
		}
	} else {
		log.Printf("Using default worker count of %d for nix-eval-jobs (logical CPU cores)", nixEvalJobsWorkers)
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

	// Make sure the parent directory exists
	dbDir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		log.Fatalf("Failed to create database directory at %s: %v", dbDir, err)
	}
	log.Printf("Using database at %s", dbPath)

	// Add busy_timeout and other optimizations
	// Consider tuning these based on workload and system
	connString := dbPath + "?_journal_mode=WAL" + // Write-Ahead Logging for better concurrency
		"&_synchronous=NORMAL" + // Less strict than FULL, faster writes
		"&_cache_size=-200000" + // Cache size in KiB (negative means KiB), ~200MB
		"&_temp_store=MEMORY" + // Use memory for temporary tables
		"&_busy_timeout=10000" + // 10 second timeout for locks
		"&_locking_mode=NORMAL" // Default locking mode

	db, err := sql.Open("sqlite3", connString)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}

	// Set connection pool limits (adjust based on profiling)
	db.SetMaxOpenConns(runtime.NumCPU() * 2) // Allow more connections than cores for potential I/O waits
	db.SetMaxIdleConns(runtime.NumCPU())
	db.SetConnMaxLifetime(time.Minute * 10)

	// Set PRAGMAs (some might be redundant with connection string)
	pragmas := []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA synchronous=NORMAL;",
		"PRAGMA cache_size=-200000;", // ~200MB
		"PRAGMA temp_store=MEMORY;",
		"PRAGMA mmap_size=30000000000;", // Memory mapping (adjust based on RAM)
		"PRAGMA page_size=32768;",       // Larger page size can help with large DBs
		"PRAGMA foreign_keys=ON;",
	}

	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			// Log as warning, not fatal, as some might fail depending on SQLite version/config
			log.Printf("Warning: Failed to set pragma '%s': %v", strings.TrimRight(pragma, ";"), err)
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
    CREATE INDEX IF NOT EXISTS idx_revision_id ON drv_revisions(revision_id); -- Added index

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
        total_derivations_found INTEGER DEFAULT 0, -- Total unique drvs found by nix-eval-jobs/fallback
        total_derivations_processed INTEGER DEFAULT 0, -- Total unique drvs processed by workers
        fallback_used INTEGER DEFAULT 0,
        processing_time_seconds INTEGER DEFAULT 0,
        nix_eval_worker_count INTEGER DEFAULT 0, -- Workers used for nix-eval-jobs
        go_worker_count INTEGER DEFAULT 0,       -- Workers used for Go processing pool
        memory_mb_peak INTEGER DEFAULT 0,
        system_info TEXT,                        -- JSON from neofetch
        host_name TEXT,                          -- Extracted from system_info for easy querying
        cpu_model TEXT,                          -- Extracted from system_info for easy querying
        cpu_cores INTEGER DEFAULT 0,             -- Extracted from system_info for easy querying (logical cores)
        memory_total TEXT,                       -- Extracted from system_info for easy querying
        kernel_version TEXT,                     -- Extracted from system_info for easy querying
        os_name TEXT,                            -- Extracted from system_info for easy querying
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

// DBBatcher handles batched database operations using a dedicated writer goroutine
type DBBatcher struct {
	db            *sql.DB
	fodBatch      []FOD         // Batch for FOD inserts/updates
	relationBatch []DrvRevision // Batch for relation inserts
	batchSize     int           // Target size for triggering a write
	fodStmt       *sql.Stmt     // Prepared statement for FODs
	relStmt       *sql.Stmt     // Prepared statement for relations
	revisionID    int64
	stats         struct { // Internal stats tracking
		drvs int64 // Total derivations processed by workers
		fods int64 // Total FODs added to batches
	}
	lastStatsTime time.Time
	mu            sync.Mutex          // Protects internal batches and stats
	writeChan     chan writeOperation // Channel to send batches to the writer goroutine
	done          chan struct{}       // Signals writer goroutine completion
	wg            sync.WaitGroup      // Waits for writer goroutine to finish
}

// writeOperation represents a batch of data to be written to the DB
type writeOperation struct {
	fods      []FOD
	relations []DrvRevision
	// Optional channel to signal completion of this specific write
	// Used primarily by Flush to ensure data is persisted before returning
	resultChan chan error
}

// NewDBBatcher creates a new database batcher
func NewDBBatcher(db *sql.DB, batchSize int, revisionID int64) (*DBBatcher, error) {
	// Prepare statement for inserting/updating FODs
	// ON CONFLICT handles cases where a FOD from a previous revision is encountered again
	fodStmt, err := db.Prepare(`
        INSERT INTO fods (drv_path, output_path, hash_algorithm, hash)
        VALUES (?, ?, ?, ?)
        ON CONFLICT(drv_path) DO UPDATE SET
        output_path = excluded.output_path,
        hash_algorithm = excluded.hash_algorithm,
        hash = excluded.hash,
        timestamp = CURRENT_TIMESTAMP -- Update timestamp on conflict
    `)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare FOD statement: %w", err)
	}

	// Prepare statement for inserting derivation-revision relationships
	// ON CONFLICT prevents duplicates if the same drv-rev pair is added multiple times
	relStmt, err := db.Prepare(`
        INSERT INTO drv_revisions (drv_path, revision_id)
        VALUES (?, ?)
        ON CONFLICT(drv_path, revision_id) DO NOTHING
    `)
	if err != nil {
		fodStmt.Close() // Clean up already prepared statement
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
		writeChan:     make(chan writeOperation, 100), // Buffer to avoid blocking producers
		done:          make(chan struct{}),
	}

	// Start the dedicated writer goroutine
	batcher.wg.Add(1)
	go batcher.writerLoop()

	return batcher, nil
}

// writerLoop is the dedicated goroutine that handles database writes
func (b *DBBatcher) writerLoop() {
	defer b.wg.Done()
	defer close(b.done) // Signal completion when loop exits

	ticker := time.NewTicker(5 * time.Second) // Periodically flush incomplete batches
	defer ticker.Stop()

	// Local buffers to accumulate operations between transactions
	localFods := make([]FOD, 0, b.batchSize*2)
	localRelations := make([]DrvRevision, 0, b.batchSize*2)
	pendingResults := []chan error{} // Track channels waiting for results

	for {
		select {
		case op, ok := <-b.writeChan:
			if !ok {
				// writeChan was closed, flush remaining data and exit
				if len(localFods) > 0 || len(localRelations) > 0 {
					err := b.executeWriteWithRetry(localFods, localRelations)
					// Notify any remaining result channels
					for _, ch := range pendingResults {
						ch <- err
						close(ch)
					}
				}
				return // Exit the writer loop
			}

			// Append incoming data to local buffers
			localFods = append(localFods, op.fods...)
			localRelations = append(localRelations, op.relations...)
			if op.resultChan != nil {
				pendingResults = append(pendingResults, op.resultChan)
			}

			// If accumulated data exceeds batch size, execute a write
			if len(localFods) >= b.batchSize || len(localRelations) >= b.batchSize {
				err := b.executeWriteWithRetry(localFods, localRelations)
				// Notify result channels for this batch
				for _, ch := range pendingResults {
					ch <- err
					close(ch)
				}
				// Clear local buffers and pending results
				localFods = localFods[:0]
				localRelations = localRelations[:0]
				pendingResults = pendingResults[:0]
			}

		case <-ticker.C:
			// Periodically flush whatever is in the local buffers
			if len(localFods) > 0 || len(localRelations) > 0 {
				err := b.executeWriteWithRetry(localFods, localRelations)
				// Notify result channels for this batch
				for _, ch := range pendingResults {
					ch <- err
					close(ch)
				}
				// Clear local buffers and pending results
				localFods = localFods[:0]
				localRelations = localRelations[:0]
				pendingResults = pendingResults[:0]
			}
		}
	}
}

// logStats prints processing statistics periodically
func (b *DBBatcher) logStats() {
	// Assumes lock is held by caller or called internally
	if time.Since(b.lastStatsTime) >= 3*time.Second {
		log.Printf("DB Stats: processed %d derivations, queued %d FODs for writing", b.stats.drvs, b.stats.fods)
		b.lastStatsTime = time.Now()
	}
}

// executeWriteWithRetry attempts to write a batch within a transaction, retrying on failure
func (b *DBBatcher) executeWriteWithRetry(fods []FOD, relations []DrvRevision) error {
	var lastErr error
	maxRetries := 5
	baseBackoff := 100 * time.Millisecond

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			backoff := baseBackoff * time.Duration(1<<attempt) // Exponential backoff
			time.Sleep(backoff)
			log.Printf("Retrying DB write (attempt %d/%d) after error: %v", attempt+1, maxRetries, lastErr)
		}

		err := b.executeWrite(fods, relations)
		if err == nil {
			return nil // Success
		}
		lastErr = err

		// Check for specific errors that shouldn't be retried (e.g., constraint violations)
		// if errors.Is(err, someNonRetryableError) { break }
	}

	log.Printf("ERROR: Failed to write batch to DB after %d attempts: %v", maxRetries, lastErr)
	// Consider more robust error handling here (e.g., writing failed batches to a file)
	return lastErr
}

// executeWrite performs the actual database transaction for a batch
func (b *DBBatcher) executeWrite(fods []FOD, relations []DrvRevision) error {
	tx, err := b.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback() // Rollback if commit fails or function panics

	fodStmtTx := tx.Stmt(b.fodStmt)
	relStmtTx := tx.Stmt(b.relStmt)
	defer fodStmtTx.Close() // Ensure prepared statements within tx are closed
	defer relStmtTx.Close()

	// Insert/Update FODs
	for _, fod := range fods {
		_, err = fodStmtTx.Exec(
			fod.DrvPath, fod.OutputPath, fod.HashAlgorithm, fod.Hash, // Values for INSERT
			// fod.OutputPath, fod.HashAlgorithm, fod.Hash, // Values for ON CONFLICT UPDATE (using excluded)
		)
		if err != nil {
			return fmt.Errorf("failed to execute FOD statement for %s: %w", fod.DrvPath, err)
		}
	}

	// Insert relations
	for _, rel := range relations {
		_, err = relStmtTx.Exec(rel.DrvPath, rel.RevisionID)
		if err != nil {
			// Don't necessarily fail the whole batch for relation errors if FODs are important
			// Log the error and continue, or return error depending on requirements
			log.Printf("Warning: Failed to execute relation statement for %s (rev %d): %v", rel.DrvPath, rel.RevisionID, err)
			// return fmt.Errorf("failed to execute relation statement for %s: %w", rel.DrvPath, err)
		}
	}

	// Commit transaction
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

// AddFOD adds a FOD and its corresponding revision relation to the batch.
// It sends the batch to the writer goroutine if the batch size is reached.
func (b *DBBatcher) AddFOD(fod FOD) {
	b.mu.Lock() // Protect access to shared batches and stats

	b.fodBatch = append(b.fodBatch, fod)
	relation := DrvRevision{
		DrvPath:    fod.DrvPath,
		RevisionID: b.revisionID,
	}
	b.relationBatch = append(b.relationBatch, relation)
	b.stats.fods++ // Increment FOD counter

	// Check if batch size is reached
	batchReady := len(b.fodBatch) >= b.batchSize

	if batchReady {
		// Copy batches to local variables to send
		fodsToSend := make([]FOD, len(b.fodBatch))
		copy(fodsToSend, b.fodBatch)
		relationsToSend := make([]DrvRevision, len(b.relationBatch))
		copy(relationsToSend, b.relationBatch)

		// Clear the shared batches
		b.fodBatch = b.fodBatch[:0]
		b.relationBatch = b.relationBatch[:0]

		// Release the lock before sending to the channel
		b.mu.Unlock()

		// Send the copied batch to the writer goroutine
		b.writeChan <- writeOperation{
			fods:      fodsToSend,
			relations: relationsToSend,
			// No resultChan needed for regular batch sends
		}
	} else {
		// Release the lock if batch wasn't sent
		b.mu.Unlock()
	}
}

// IncrementDrvCount increments the count of processed derivations and logs stats periodically.
func (b *DBBatcher) IncrementDrvCount() {
	b.mu.Lock()
	b.stats.drvs++
	shouldLog := time.Since(b.lastStatsTime) >= 3*time.Second
	if shouldLog {
		b.logStats() // Log stats while lock is held
	}
	b.mu.Unlock()
}

// GetProcessedDrvCount returns the current count of processed derivations.
func (b *DBBatcher) GetProcessedDrvCount() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stats.drvs
}

// Flush sends any remaining items in the current batches to the writer goroutine
// and waits for the write to complete.
func (b *DBBatcher) Flush() error {
	b.mu.Lock() // Protect access to shared batches

	if len(b.fodBatch) == 0 && len(b.relationBatch) == 0 {
		b.mu.Unlock()
		return nil // Nothing to flush
	}

	// Copy remaining batches
	fodsToSend := make([]FOD, len(b.fodBatch))
	copy(fodsToSend, b.fodBatch)
	relationsToSend := make([]DrvRevision, len(b.relationBatch))
	copy(relationsToSend, b.relationBatch)

	// Clear shared batches
	b.fodBatch = b.fodBatch[:0]
	b.relationBatch = b.relationBatch[:0]

	// Create a channel to wait for the result of this specific flush
	resultChan := make(chan error, 1)

	// Release lock before sending to channel
	b.mu.Unlock()

	// Send the final batch to the writer
	b.writeChan <- writeOperation{
		fods:       fodsToSend,
		relations:  relationsToSend,
		resultChan: resultChan, // Provide the channel to get the result
	}

	// Wait for the writer to process this specific batch
	err := <-resultChan
	if err != nil {
		log.Printf("ERROR during DB flush: %v", err)
		return err
	}
	log.Println("DB Batcher flushed successfully.")
	return nil
}

// Close flushes any remaining batches, signals the writer goroutine to stop,
// waits for it to finish, and closes the prepared statements.
func (b *DBBatcher) Close() error {
	log.Println("Closing DB Batcher...")
	// Flush any remaining data and wait for it
	flushErr := b.Flush()

	// Signal the writer goroutine to exit by closing the channel
	close(b.writeChan)

	// Wait for the writer goroutine to finish processing and exit
	b.wg.Wait() // Wait for writerLoop to finish
	<-b.done    // Ensure done channel is closed

	log.Println("DB writer goroutine finished.")

	// Close prepared statements
	var closeErrors []error
	if err := b.fodStmt.Close(); err != nil {
		closeErrors = append(closeErrors, fmt.Errorf("failed to close FOD statement: %w", err))
	}
	if err := b.relStmt.Close(); err != nil {
		closeErrors = append(closeErrors, fmt.Errorf("failed to close relation statement: %w", err))
	}

	// Combine flush error and close errors
	if flushErr != nil {
		closeErrors = append(closeErrors, flushErr)
	}

	if len(closeErrors) > 0 {
		// Log all errors, return the first one
		for _, err := range closeErrors {
			log.Printf("Error during DBBatcher close: %v", err)
		}
		return closeErrors[0]
	}

	log.Println("DB Batcher closed.")
	return nil
}

// ProcessingContext holds the shared state for processing derivations
type ProcessingContext struct {
	batcher       *DBBatcher    // For writing results to the database
	processedDrvs *sync.Map     // Thread-safe map to track processed derivations (key: drvPath, value: bool)
	workQueue     chan<- string // Channel to send newly found derivation paths for processing
	// wg is removed, managed within findFODsForRevision now
}

// processDerivation processes a single derivation file.
// It checks if the derivation has already been processed using processedDrvs.
// If it's a new derivation, it parses it, adds any FOD outputs to the DB batcher,
// and queues its input derivations for further processing.
func processDerivation(inputFile string, ctx *ProcessingContext) {
	// The LoadOrStore is crucial:
	// - It atomically checks if the key exists.
	// - If it exists (loaded == true), we return immediately, avoiding redundant work.
	// - If it doesn't exist, it stores `true` and returns `loaded == false`,
	//   ensuring only one goroutine fully processes this specific inputFile.
	if _, loaded := ctx.processedDrvs.LoadOrStore(inputFile, true); loaded {
		return // Already processed or being processed by another goroutine
	}

	// Increment the counter only *after* successfully marking it as processed
	ctx.batcher.IncrementDrvCount()

	// --- Open and Parse Derivation ---
	file, err := os.Open(inputFile)
	if err != nil {
		// Log error but don't stop processing other derivations
		log.Printf("Error opening file %s: %v", inputFile, err)
		return // Cannot proceed with this derivation
	}
	defer file.Close()

	drv, err := derivation.ReadDerivation(file)
	if err != nil {
		log.Printf("Error reading derivation %s: %v", inputFile, err)
		// Mark as processed but couldn't parse
		return
	}

	// --- Process Outputs (Find FODs) ---
	// isFOD := false
	for name, out := range drv.Outputs {
		// Check if it's a Fixed Output Derivation (has hash info)
		if out.HashAlgorithm != "" && out.Hash != "" {
			fod := FOD{
				DrvPath:       inputFile,
				OutputPath:    out.Path, // Use the specific output path
				HashAlgorithm: out.HashAlgorithm,
				Hash:          out.Hash,
			}
			ctx.batcher.AddFOD(fod) // Add to the database batch
			// isFOD = true            // Mark that this drv is a FOD

			if os.Getenv("VERBOSE") == "1" {
				log.Printf("Found FOD: %s (output: %s, hash: %s:%s)",
					filepath.Base(inputFile), name, out.HashAlgorithm, out.Hash)
			}
			// Typically, a FOD has only one output defining the fixed hash,
			// but we process all outputs just in case. If multiple outputs have hashes,
			// the current DB schema (drv_path primary key) stores the last one encountered.
			// If multiple FOD outputs per drv need tracking, schema needs adjustment.
		}
	}

	// --- Queue Input Derivations ---
	// If the current derivation itself was a FOD, we generally don't need to
	// explore its inputs further for *this specific task* (finding FODs).
	// However, exploring inputs might be necessary for other analyses or full graph traversal.
	// For strict FOD finding, we can potentially skip queuing inputs if isFOD is true.
	// Let's keep exploring inputs for now for completeness, but this is an optimization point.
	// if isFOD {
	//     return // Optimization: Stop traversal at FODs if only finding FODs matters
	// }

	for inputDrvPath := range drv.InputDerivations {
		// Check if the input derivation has *already* been processed or queued.
		// Use Load here for a quick check. If it exists, we don't need to queue it again.
		if _, loaded := ctx.processedDrvs.Load(inputDrvPath); !loaded {
			// Only attempt to queue if not already loaded.
			// Another goroutine might LoadOrStore it concurrently, which is fine.
			select {
			case ctx.workQueue <- inputDrvPath:
				// Successfully queued the input derivation path
			default:
				// This case should be rare with a buffered channel, but could happen
				// if the queue is genuinely full or closed unexpectedly.
				log.Printf("Warning: Work queue full or closed when trying to add input drv %s (from %s)", inputDrvPath, inputFile)
				// We don't necessarily need to mark it processed here,
				// as the worker picking it up will do the LoadOrStore check.
			}
		}
	}
}

// getOrCreateRevision gets the ID for a revision string, creating it if it doesn't exist.
func getOrCreateRevision(db *sql.DB, rev string) (int64, error) {
	var id int64
	// Check if revision already exists
	err := db.QueryRow("SELECT id FROM revisions WHERE rev = ?", rev).Scan(&id)
	if err == nil {
		log.Printf("Revision %s already exists with ID %d", rev, id)
		return id, nil // Found existing revision
	} else if err != sql.ErrNoRows {
		return 0, fmt.Errorf("error checking for existing revision %s: %w", rev, err)
	}

	// Revision doesn't exist, insert it
	log.Printf("Creating new entry for revision %s", rev)
	result, err := db.Exec("INSERT INTO revisions (rev) VALUES (?)", rev)
	if err != nil {
		// Handle potential race condition if another instance inserted it concurrently
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			log.Printf("Revision %s was likely inserted concurrently, retrying fetch...", rev)
			// Retry the query row
			err = db.QueryRow("SELECT id FROM revisions WHERE rev = ?", rev).Scan(&id)
			if err == nil {
				return id, nil
			}
			return 0, fmt.Errorf("error fetching concurrently inserted revision %s: %w", rev, err)
		}
		return 0, fmt.Errorf("failed to insert revision %s: %w", rev, err)
	}

	id, err = result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get last insert ID for revision %s: %w", rev, err)
	}
	log.Printf("Created revision %s with ID %d", rev, id)
	return id, nil
}

// findFODsForRevision orchestrates the process of finding FODs for a specific nixpkgs revision.
func findFODsForRevision(rev string, revisionID int64, db *sql.DB) error {
	revStartTime := time.Now()
	log.Printf("[%s] Starting FOD finding process...", rev)

	// --- Prepare Nixpkgs Worktree ---
	worktreeDir, err := prepareNixpkgsWorktree(rev)
	if err != nil {
		return fmt.Errorf("[%s] failed to prepare worktree: %w", rev, err)
	}
	defer func() {
		log.Printf("[%s] Cleaning up worktree at %s", rev, worktreeDir)
		// Use os.RemoveAll for cleanup
		if removeErr := os.RemoveAll(worktreeDir); removeErr != nil {
			log.Printf("[%s] Warning: Failed to remove worktree directory %s: %v", rev, worktreeDir, removeErr)
		}
	}()

	// --- Initialize Database Batcher ---
	// Use a reasonable batch size, e.g., 1000 or 5000
	batcher, err := NewDBBatcher(db, 2000, revisionID)
	if err != nil {
		return fmt.Errorf("[%s] failed to create database batcher: %w", rev, err)
	}
	defer batcher.Close() // Ensure batcher is closed and flushed

	// --- Setup Processing Context and Worker Pool ---
	processedDrvs := &sync.Map{} // Map to track processed derivations [string]bool
	// Use a large buffer to prevent nix-eval-jobs from blocking if workers are busy
	// Tune buffer size based on memory and expected number of derivations
	workQueue := make(chan string, 100000)

	ctx := &ProcessingContext{
		batcher:       batcher,
		processedDrvs: processedDrvs,
		workQueue:     workQueue, // Pass the send-only channel interface
	}

	// Determine number of Go worker goroutines (use logical cores)
	numGoWorkers := runtime.NumCPU()
	log.Printf("[%s] Setting up Go processing worker pool with %d workers (logical cores)", rev, numGoWorkers)

	var workerWg sync.WaitGroup // WaitGroup for the Go processing workers

	// Start Go worker goroutines
	for i := 0; i < numGoWorkers; i++ {
		workerWg.Add(1)
		go func(workerID int) {
			defer workerWg.Done()
			// Process items from the work queue until it's closed
			for drvPath := range workQueue {
				processDerivation(drvPath, ctx)
			}
			// Log worker completion (optional)
			// log.Printf("[%s] Go Worker %d finished.", rev, workerID)
		}(i)
	}

	// --- Memory Monitoring ---
	var peakMemoryMB int64
	var memoryMutex sync.Mutex
	memoryDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		var m runtime.MemStats
		for {
			select {
			case <-ticker.C:
				runtime.ReadMemStats(&m)
				allocMB := int64(m.Alloc / 1024 / 1024)
				memoryMutex.Lock()
				if allocMB > peakMemoryMB {
					peakMemoryMB = allocMB
				}
				currentPeak := peakMemoryMB // Capture current peak for logging
				memoryMutex.Unlock()
				log.Printf("[%s] Memory Usage: Alloc=%dMB Sys=%dMB (Peak Alloc Reached=%dMB)",
					rev, allocMB, m.Sys/1024/1024, currentPeak)
			case <-memoryDone:
				log.Printf("[%s] Memory monitoring stopped.", rev)
				return
			}
		}
	}()

	// --- Run nix-eval-jobs and Feed Work Queue ---
	// Channel to receive initial derivation paths from nix-eval-jobs/fallback
	drvPathChan := make(chan string, 50000) // Buffer between nix-eval-jobs and queue feeder

	// Goroutine to feed the workQueue from drvPathChan, checking processedDrvs first
	feederDone := make(chan struct{})
	go func() {
		defer close(feederDone)
		initialJobCount := 0
		for drvPath := range drvPathChan {
			// Check if already processed *before* adding to work queue
			if _, loaded := ctx.processedDrvs.LoadOrStore(drvPath, true); !loaded {
				initialJobCount++
				select {
				case workQueue <- drvPath:
					// Successfully queued initial job
				default:
					log.Printf("[%s] Warning: Work queue full or closed when adding initial drv %s", rev, drvPath)
					// Mark as processed anyway, as we couldn't queue it
					// ctx.processedDrvs.Store(drvPath, true) // Or rely on worker check? Let's rely on worker check.
				}
				if initialJobCount%5000 == 0 {
					log.Printf("[%s] Queued %d initial derivations for processing...", rev, initialJobCount)
				}
			}
		}
		log.Printf("[%s] Finished queueing %d initial derivations.", rev, initialJobCount)
		// CRITICAL: Close the workQueue only after all initial paths are sent
		// and the feeder goroutine is done processing drvPathChan.
		close(workQueue)
		log.Printf("[%s] Work queue closed.", rev)
	}()

	// --- Execute Nix Evaluation ---
	usedFallback := false
	fileStats := make(map[string]*exprFileStats) // Map to store stats per expression file

	log.Printf("[%s] Starting nix-eval-jobs/fallback evaluation...", rev)
	// Pass nixEvalJobsWorkers (potentially from env var) to the nix command
	evalErr := streamNixEvalJobs(rev, worktreeDir, nixEvalJobsWorkers, drvPathChan, fileStats, &usedFallback)

	// CRITICAL: Close drvPathChan *after* streamNixEvalJobs finishes (or errors out)
	// This signals the feeder goroutine that no more initial paths are coming.
	close(drvPathChan)
	log.Printf("[%s] Nix evaluation finished. Waiting for feeder goroutine...", rev)

	// Wait for the feeder goroutine to finish processing all paths from drvPathChan and close workQueue
	<-feederDone
	log.Printf("[%s] Feeder goroutine finished. Waiting for worker pool...", rev)

	// --- Wait for Workers and Cleanup ---
	// Wait for all Go worker goroutines to finish processing everything in the workQueue
	workerWg.Wait()
	log.Printf("[%s] Go processing worker pool finished.", rev)

	// Stop memory monitoring
	close(memoryDone)

	// Final flush of any remaining data in the DB batcher
	log.Printf("[%s] Flushing final DB batches...", rev)
	if flushErr := batcher.Flush(); flushErr != nil {
		log.Printf("[%s] Error during final DB flush: %v", rev, flushErr)
		// Decide if this error should cause the overall function to fail
		// return flushErr // Optionally propagate flush error
	}

	// --- Log Summary and Store Metadata ---
	revElapsed := time.Since(revStartTime)
	log.Printf("[%s] Processing completed in %v", rev, revElapsed)

	// Get final counts
	processedCount := batcher.GetProcessedDrvCount() // Get count from batcher's internal stats
	var fodCount int64
	countErr := db.QueryRow("SELECT COUNT(*) FROM drv_revisions WHERE revision_id = ?", revisionID).Scan(&fodCount)
	if countErr != nil {
		log.Printf("[%s] Error counting final FODs in DB: %v", rev, countErr)
	} else {
		log.Printf("[%s] Final stats: Found %d FODs, processed %d total unique derivations.", rev, fodCount, processedCount)
	}

	// Get final peak memory
	memoryMutex.Lock()
	finalPeakMemory := peakMemoryMB
	memoryMutex.Unlock()

	// Store evaluation metadata and overall stats
	log.Printf("[%s] Storing evaluation metadata...", rev)
	if storeErr := storeEvaluationMetadata(db, revisionID, fileStats, worktreeDir,
		revElapsed, usedFallback, finalPeakMemory, processedCount, numGoWorkers); storeErr != nil {
		log.Printf("[%s] Error storing evaluation metadata: %v", rev, storeErr)
		// Decide if this error should cause the overall function to fail
		// return storeErr // Optionally propagate metadata storage error
	} else {
		log.Printf("[%s] Successfully stored evaluation metadata.", rev)
	}

	// Return the original evaluation error, if any
	if evalErr != nil {
		return fmt.Errorf("[%s] evaluation failed: %w", rev, evalErr)
	}

	return nil // Success
}

// streamNixEvalJobs runs nix-eval-jobs (or fallback) and streams derivation paths to drvPathChan.
// It populates fileStats with metadata about each expression file evaluation attempt.
func streamNixEvalJobs(rev string, nixpkgsDir string, workers int, drvPathChan chan<- string,
	fileStats map[string]*exprFileStats, usedFallback *bool,
) error {
	log.Printf("[%s] Identifying Nix expression files in %s...", rev, nixpkgsDir)

	// Define potential Nix expression files relative to nixpkgsDir
	// Order matters: try preferred/common ones first
	relativePaths := []string{
		"pkgs/top-level/release-outpaths.nix",
		"pkgs/top-level/release.nix",
		"pkgs/top-level/release-small.nix",
		"pkgs/top-level/all-packages.nix",
		"nixos/release.nix",
		"nixos/release-small.nix",
		"nixos/release-combined.nix",
		"all-packages.nix", // Older nixpkgs might have this at top-level
		"default.nix",      // Less common for full evaluation, but possible
	}

	var existingPaths []string                            // Store full paths of files that actually exist
	allCheckedPaths := make([]string, len(relativePaths)) // Store all full paths checked

	// Check existence and initialize stats
	for i, relPath := range relativePaths {
		fullPath := filepath.Join(nixpkgsDir, relPath)
		allCheckedPaths[i] = fullPath // Store the full path regardless of existence
		stats := &exprFileStats{}     // Initialize stats for this path
		fileStats[fullPath] = stats   // Add to the main stats map

		if _, err := os.Stat(fullPath); err == nil {
			stats.exists = true
			existingPaths = append(existingPaths, fullPath)
			log.Printf("[%s] Found potential expression file: %s", rev, relPath)
		} else {
			// Log if file doesn't exist (optional, can be verbose)
			// log.Printf("[%s] Expression file does not exist: %s", rev, relPath)
		}
	}

	if len(existingPaths) == 0 {
		log.Printf("[%s] Warning: No standard Nix expression files found. Will proceed directly to fallback.", rev)
		// Skip the nix-eval-jobs loop and go straight to fallback
	}

	totalJobCount := 0 // Total unique derivations found across all files
	// Global deduplication map (optional, as feeder goroutine handles final deduplication)
	// Keeping it here helps provide more accurate per-file unique counts if needed for logging.
	// globalVisited := make(map[string]bool)

	// --- Evaluate Each Existing Expression File ---
	var firstError error // Record the first error encountered

	for _, expressionPath := range existingPaths {
		relPath, _ := filepath.Rel(nixpkgsDir, expressionPath)
		stats := fileStats[expressionPath] // Get stats object for this file
		stats.attempted = true             // Mark as attempted

		log.Printf("[%s] Evaluating expression file: %s with %d workers...", rev, relPath, workers)

		// Build the nix-eval-jobs command
		// Adjust memory size as needed
		// Consider adding --impure if needed for specific nixpkgs versions/setups
		cmd := exec.Command("nix-eval-jobs",
			expressionPath, // Use the full path
			"--workers", fmt.Sprintf("%d", workers),
			"--max-memory-size", "4096", // Per-worker memory limit in MB
			"--option", "allow-import-from-derivation", "false", // Security best practice
			// "--option", "show-trace", "true", // Enable for debugging eval errors
			// Consider adding flags like --check-cache, --force-check based on needs
			// "--arg", "checkMeta", "false", // This arg might not exist in all nixpkgs versions
		)
		cmd.Dir = nixpkgsDir // Run command from within the nixpkgs directory

		// Capture stdout and stderr
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			errMsg := fmt.Sprintf("failed to create stdout pipe: %v", err)
			stats.errorMessage = errMsg
			log.Printf("[%s] ERROR: %s for %s", rev, errMsg, relPath)
			if firstError == nil {
				firstError = fmt.Errorf(errMsg)
			}
			continue // Try next file
		}
		stderrPipe, err := cmd.StderrPipe()
		if err != nil {
			errMsg := fmt.Sprintf("failed to create stderr pipe: %v", err)
			stats.errorMessage = errMsg
			log.Printf("[%s] ERROR: %s for %s", rev, errMsg, relPath)
			if firstError == nil {
				firstError = fmt.Errorf(errMsg)
			}
			stdout.Close() // Close stdout pipe if stderr fails
			continue       // Try next file
		}

		// Start capturing stderr in a goroutine
		var stderrBuf strings.Builder
		stderrWg := sync.WaitGroup{}
		stderrWg.Add(1)
		go func() {
			defer stderrWg.Done()
			_, _ = io.Copy(&stderrBuf, stderrPipe)
		}()

		// Start the command
		err = cmd.Start()
		if err != nil {
			errMsg := fmt.Sprintf("failed to start nix-eval-jobs: %v", err)
			stats.errorMessage = errMsg
			log.Printf("[%s] ERROR: %s for %s", rev, errMsg, relPath)
			if firstError == nil {
				firstError = fmt.Errorf(errMsg)
			}
			stdout.Close()
			stderrPipe.Close() // Ensure stderr pipe is closed
			stderrWg.Wait()    // Wait for stderr goroutine
			continue           // Try next file
		}

		// Process stdout (JSON stream of derivations)
		scanner := bufio.NewScanner(stdout)
		// Increase buffer size for potentially long lines (drv paths can be long)
		const maxScannerCapacity = 10 * 1024 * 1024 // 10 MB buffer
		buf := make([]byte, maxScannerCapacity)
		scanner.Buffer(buf, maxScannerCapacity)

		jobCountFile := 0 // Derivations found in *this* file

		for scanner.Scan() {
			line := scanner.Text()
			var result struct {
				DrvPath string `json:"drvPath"`
				// Add other fields if needed from nix-eval-jobs output
			}
			// Trim whitespace just in case
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}

			if err := json.Unmarshal([]byte(line), &result); err != nil {
				// Log parsing errors but continue processing other lines
				log.Printf("[%s] Warning: Failed to parse JSON line from %s: %v (Line: %s)", rev, relPath, err, line)
				continue
			}

			if result.DrvPath != "" {
				jobCountFile++
				// Send the found derivation path to the channel for the feeder goroutine
				// The feeder goroutine will handle deduplication via processedDrvs map
				select {
				case drvPathChan <- result.DrvPath:
					// Successfully sent
				default:
					// This indicates the drvPathChan buffer is full, which might mean
					// the feeder or workers are falling behind. Log it.
					log.Printf("[%s] Warning: drvPathChan buffer full when sending path from %s. Potential bottleneck.", rev, relPath)
					// We might lose this path if the channel remains full.
					// Consider increasing drvPathChan buffer or optimizing downstream processing.
				}

				// Optional: Log progress periodically per file
				if jobCountFile%5000 == 0 {
					log.Printf("[%s] Found %d derivations from %s...", rev, jobCountFile, relPath)
				}
			}
		}

		stats.derivationsFound = jobCountFile // Store count for this file

		// Check for scanner errors
		if err := scanner.Err(); err != nil {
			errMsg := fmt.Sprintf("error reading stdout from %s: %v", relPath, err)
			stats.errorMessage = errMsg
			log.Printf("[%s] ERROR: %s", rev, errMsg)
			if firstError == nil {
				firstError = fmt.Errorf(errMsg)
			}
		}

		// Wait for the command to complete and capture stderr
		err = cmd.Wait()
		stderrPipe.Close() // Close the pipe explicitly after Wait()
		stderrWg.Wait()    // Wait for stderr goroutine to finish reading
		stderrOutput := stderrBuf.String()

		if err != nil {
			// Command exited with an error
			errMsg := fmt.Sprintf("nix-eval-jobs for %s exited with error: %v", relPath, err)
			if stderrOutput != "" {
				errMsg += fmt.Sprintf("\nStderr:\n%s", stderrOutput)
			}
			stats.errorMessage = errMsg // Store detailed error
			log.Printf("[%s] ERROR: %s", rev, errMsg)
			if firstError == nil {
				firstError = err // Store the actual exit error
			}
			// Continue to the next file even if one fails
		} else {
			// Command completed successfully
			stats.succeeded = true
			log.Printf("[%s] Evaluation of %s completed successfully (%d derivations found).", rev, relPath, jobCountFile)
			if stderrOutput != "" {
				// Log stderr even on success, as it might contain warnings
				log.Printf("[%s] nix-eval-jobs stderr for %s (success):\n%s", rev, relPath, stderrOutput)
			}
		}
		totalJobCount += jobCountFile // Add to total (note: not unique across files here)
	} // End loop over existingPaths

	// --- Fallback Mechanism ---
	// If no derivations were found via nix-eval-jobs OR if no expression files existed initially
	if totalJobCount == 0 {
		log.Printf("[%s] No derivations found via nix-eval-jobs or no standard files existed. Attempting fallback mechanism...", rev)
		*usedFallback = true // Mark that fallback is being used

		fallbackPath := filepath.Join(nixpkgsDir, "fod-finder-fallback.nix")
		relFallbackPath := "fod-finder-fallback.nix"
		fallbackStats := &exprFileStats{attempted: true} // Initialize stats for fallback
		fileStats[fallbackPath] = fallbackStats          // Add to main stats map

		// Simple Nix expression trying to import nixpkgs and get basic derivations
		// This is highly dependent on the nixpkgs structure at the given revision
		fallbackNixContent := `
let
  # Attempt to import the top-level nixpkgs set
  pkgsAttempt = builtins.tryEval (import ./. {});
  # If import fails, use an empty set
  pkgs = if pkgsAttempt.success then pkgsAttempt.value else {};

  # List of potential package names/attributes to check
  # Include common core packages and potential variations
  potentialAttrs = [
    "stdenv" "bash" "coreutils" "gcc" "glibc" "binutils" "findutils"
    "gzip" "gnused" "gnutar" "gnugrep" "gawk" "patch" "make" "perl" "python"
    # Add more essential build tools if needed
  ];

  # Function to safely access an attribute and check if it looks like a derivation
  getDrv = attrName:
    let val = pkgs.${attrName} or null;
    in if val != null && builtins.isAttrs val && val ? type && val.type == "derivation" then val else null;

  # Get the derivations for the potential attributes
  basicDrvs = builtins.filter (x: x != null) (map getDrv potentialAttrs);

  # Extract the drvPath from each valid derivation
  extractDrvPath = drv: drv.drvPath or null;

in
  # Return a list of unique, non-null drvPaths
  builtins.filter (x: x != null) (builtins.attrValues (builtins.listToAttrs (map (p: { name = p; value = p; }) (map extractDrvPath basicDrvs))))
`
		// Write the fallback Nix file
		err := os.WriteFile(fallbackPath, []byte(fallbackNixContent), 0o644)
		if err != nil {
			errMsg := fmt.Sprintf("failed to write fallback Nix file %s: %v", relFallbackPath, err)
			fallbackStats.errorMessage = errMsg
			log.Printf("[%s] ERROR: %s", rev, errMsg)
			if firstError == nil {
				firstError = fmt.Errorf(errMsg)
			}
			// Fallback failed, print summary and return the error
			printExpressionSummary(rev, nixpkgsDir, allCheckedPaths, fileStats, fallbackPath)
			return firstError // Return the first error encountered overall
		}
		fallbackStats.exists = true // Mark fallback file as existing

		// Try evaluating the fallback file with nix-instantiate
		log.Printf("[%s] Running fallback evaluation: nix-instantiate --eval --json %s", rev, relFallbackPath)
		fallbackCmd := exec.Command("nix-instantiate", "--eval", "--json", "--strict", "--read-write-mode", fallbackPath)
		fallbackCmd.Dir = nixpkgsDir // Run from nixpkgs directory

		fallbackOutput, err := fallbackCmd.Output() // Capture combined stdout/stderr
		if err != nil {
			// nix-instantiate failed
			errMsg := fmt.Sprintf("fallback evaluation failed for %s: %v\nOutput:\n%s", relFallbackPath, err, string(fallbackOutput))
			fallbackStats.errorMessage = errMsg
			log.Printf("[%s] ERROR: %s", rev, errMsg)
			if firstError == nil {
				firstError = fmt.Errorf("fallback evaluation failed: %w", err)
			}
			// Fallback failed, print summary and return
			printExpressionSummary(rev, nixpkgsDir, allCheckedPaths, fileStats, fallbackPath)
			return firstError // Return the first error encountered overall
		}

		// Parse the JSON output from nix-instantiate (should be a list of strings)
		var drvPaths []string
		if err := json.Unmarshal(fallbackOutput, &drvPaths); err != nil {
			errMsg := fmt.Sprintf("failed to parse fallback JSON output from %s: %v\nOutput:\n%s", relFallbackPath, err, string(fallbackOutput))
			fallbackStats.errorMessage = errMsg
			log.Printf("[%s] ERROR: %s", rev, errMsg)
			if firstError == nil {
				firstError = fmt.Errorf("fallback JSON parsing failed: %w", err)
			}
			// Fallback parsing failed, print summary and return
			printExpressionSummary(rev, nixpkgsDir, allCheckedPaths, fileStats, fallbackPath)
			return firstError // Return the first error encountered overall
		}

		// Send the paths found via fallback to the drvPathChan
		fallbackJobCount := 0
		for _, path := range drvPaths {
			if path != "" {
				fallbackJobCount++
				select {
				case drvPathChan <- path:
					// Successfully sent fallback path
				default:
					log.Printf("[%s] Warning: drvPathChan buffer full when sending path from fallback. Potential bottleneck.", rev)
				}
			}
		}

		fallbackStats.derivationsFound = fallbackJobCount
		if fallbackJobCount > 0 {
			fallbackStats.succeeded = true
			log.Printf("[%s] Fallback mechanism found %d basic derivations.", rev, fallbackJobCount)
		} else {
			log.Printf("[%s] Fallback mechanism did not find any derivations.", rev)
			if firstError == nil {
				// If nix-eval-jobs didn't error but found nothing, and fallback found nothing,
				// create a generic error message.
				firstError = fmt.Errorf("no derivations found by nix-eval-jobs or fallback")
			}
		}
		// Print summary including fallback attempt before returning
		printExpressionSummary(rev, nixpkgsDir, allCheckedPaths, fileStats, fallbackPath)
		return firstError // Return the first error encountered (or nil if fallback succeeded after initial failures)

	} else {
		// nix-eval-jobs found derivations, print summary without fallback path
		printExpressionSummary(rev, nixpkgsDir, allCheckedPaths, fileStats)
		return firstError // Return the first error encountered during nix-eval-jobs runs (if any)
	}
}

// storeEvaluationMetadata persists evaluation metadata and overall revision stats to the database.
func storeEvaluationMetadata(db *sql.DB, revisionID int64, stats map[string]*exprFileStats,
	nixpkgsDir string, processingTime time.Duration, usedFallback bool, peakMemoryMB int64,
	totalDerivationsProcessed int64, goWorkerCount int,
) error {
	log.Printf("Storing metadata for revision ID %d...", revisionID)

	// Begin transaction
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin metadata transaction: %w", err)
	}
	// Use defer with a named return to handle rollback/commit cleanly
	var txErr error
	defer func() {
		if txErr != nil {
			log.Printf("Rolling back metadata transaction due to error: %v", txErr)
			tx.Rollback()
		} else {
			log.Printf("Committing metadata transaction for revision ID %d", revisionID)
			commitErr := tx.Commit()
			if commitErr != nil {
				txErr = fmt.Errorf("failed to commit metadata transaction: %w", commitErr)
				log.Printf("ERROR: %v", txErr)
			}
		}
	}()

	// --- Store Per-File Metadata ---
	metadataStmt, err := tx.Prepare(`
		INSERT INTO evaluation_metadata (
			revision_id, file_path, file_exists, attempted,
			succeeded, error_message, derivations_found
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(revision_id, file_path) DO UPDATE SET
		file_exists = excluded.file_exists,
		attempted = excluded.attempted,
		succeeded = excluded.succeeded,
		error_message = excluded.error_message,
		derivations_found = excluded.derivations_found,
		evaluation_time = CURRENT_TIMESTAMP
	`)
	if err != nil {
		txErr = fmt.Errorf("failed to prepare metadata statement: %w", err)
		return txErr
	}
	defer metadataStmt.Close()

	var totalExprFound, totalExprAttempted, totalExprSucceeded, totalExprDerivations int
	processedFilePaths := make(map[string]bool) // Track paths already processed

	for path, stat := range stats {
		// Ensure we only process each unique path once, even if it was checked multiple times
		if processedFilePaths[path] {
			continue
		}
		processedFilePaths[path] = true

		relPath, _ := filepath.Rel(nixpkgsDir, path)
		if relPath == "." || relPath == "" {
			relPath = filepath.Base(path) // Use base name if relative path fails (e.g., for fallback file)
		}

		// Convert boolean to int for SQLite
		fileExists := 0
		if stat.exists {
			fileExists = 1
			totalExprFound++
		}
		attempted := 0
		if stat.attempted {
			attempted = 1
			totalExprAttempted++
		}
		succeeded := 0
		if stat.succeeded {
			succeeded = 1
			totalExprSucceeded++
		}
		totalExprDerivations += stat.derivationsFound // Sum derivations found by nix-eval-jobs/fallback

		_, err := metadataStmt.Exec(
			revisionID, relPath, fileExists, attempted,
			succeeded, stat.errorMessage, stat.derivationsFound)
		if err != nil {
			txErr = fmt.Errorf("failed to insert/update metadata for %s: %w", relPath, err)
			return txErr
		}
	}

	// --- Store Revision-Level Stats ---
	fallbackUsedInt := 0
	if usedFallback {
		fallbackUsedInt = 1
	}

	// Get logical core count from system info
	logicalCores := getCPUCores(systemInfo.Cores)

	// Use Exec directly for single insert/update
	_, err = tx.Exec(`
		INSERT INTO revision_stats (
			revision_id, total_expressions_found, total_expressions_attempted,
			total_expressions_succeeded, total_derivations_found, total_derivations_processed,
			fallback_used, processing_time_seconds, nix_eval_worker_count, go_worker_count, memory_mb_peak,
			system_info, host_name, cpu_model, cpu_cores, memory_total, kernel_version, os_name
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(revision_id) DO UPDATE SET
		total_expressions_found = excluded.total_expressions_found,
		total_expressions_attempted = excluded.total_expressions_attempted,
		total_expressions_succeeded = excluded.total_expressions_succeeded,
		total_derivations_found = excluded.total_derivations_found,
		total_derivations_processed = excluded.total_derivations_processed,
		fallback_used = excluded.fallback_used,
		processing_time_seconds = excluded.processing_time_seconds,
		nix_eval_worker_count = excluded.nix_eval_worker_count,
		go_worker_count = excluded.go_worker_count,
		memory_mb_peak = excluded.memory_mb_peak,
		system_info = excluded.system_info,
		host_name = excluded.host_name,
		cpu_model = excluded.cpu_model,
		cpu_cores = excluded.cpu_cores,
		memory_total = excluded.memory_total,
		kernel_version = excluded.kernel_version,
		os_name = excluded.os_name,
		evaluation_timestamp = CURRENT_TIMESTAMP
	`,
		revisionID, totalExprFound, totalExprAttempted, totalExprSucceeded, totalExprDerivations, totalDerivationsProcessed,
		fallbackUsedInt, int(processingTime.Seconds()), nixEvalJobsWorkers, goWorkerCount, peakMemoryMB,
		systemInfoJSON, systemInfo.Host, systemInfo.CPU, logicalCores, systemInfo.Memory, systemInfo.Kernel, systemInfo.OS)
	if err != nil {
		txErr = fmt.Errorf("failed to insert/update revision stats: %w", err)
		return txErr
	}

	// If we reach here without error, the defer func will commit the transaction
	log.Printf("Metadata transaction prepared for commit for revision ID %d", revisionID)
	return nil
}

// printExpressionSummary logs a summary table of expression file evaluation attempts.
func printExpressionSummary(rev, nixpkgsDir string, checkedPaths []string, stats map[string]*exprFileStats, extraPaths ...string) {
	log.Printf("[%s] --- Expression File Evaluation Summary ---", rev)
	header := fmt.Sprintf("[%s] %-45s | %-6s | %-9s | %-9s | %-11s | %s",
		rev, "FILE (relative to nixpkgs)", "EXISTS", "ATTEMPTED", "SUCCEEDED", "DERIVATIONS", "ERROR (truncated)")
	log.Println(header)
	log.Printf("[%s] %s", rev, strings.Repeat("-", len(header)-len(rev)-3)) // Divider line

	allPaths := append([]string{}, checkedPaths...)
	allPaths = append(allPaths, extraPaths...)
	processedRelPaths := make(map[string]bool) // Avoid printing duplicates if paths resolve similarly

	for _, path := range allPaths {
		relPath, _ := filepath.Rel(nixpkgsDir, path)
		if relPath == "." || relPath == "" {
			relPath = filepath.Base(path) // Use base name if relative path fails
		}

		// Skip if we already printed this relative path
		if processedRelPaths[relPath] {
			continue
		}

		if s, ok := stats[path]; ok {
			var errorSummary string
			if s.errorMessage != "" {
				// Sanitize and truncate error message
				sanitized := strings.ReplaceAll(s.errorMessage, "\n", " ")
				sanitized = strings.ReplaceAll(sanitized, "\r", "")
				if len(sanitized) > 70 {
					errorSummary = sanitized[:67] + "..."
				} else {
					errorSummary = sanitized
				}
			}
			log.Printf("[%s] %-45s | %-6t | %-9t | %-9t | %-11d | %s",
				rev, relPath, s.exists, s.attempted, s.succeeded,
				s.derivationsFound, errorSummary)
			processedRelPaths[relPath] = true // Mark as printed
		} else {
			// Should not happen if all paths are added to stats map initially
			log.Printf("[%s] %-45s | (stats not found)", rev, relPath)
		}
	}
	log.Printf("[%s] --- End Summary ---", rev)
}

// prepareNixpkgsWorktree creates or updates a git worktree for a specific nixpkgs revision.
func prepareNixpkgsWorktree(rev string) (string, error) {
	if len(rev) != 40 || !isHex(rev) {
		return "", fmt.Errorf("invalid git commit hash format: %s", rev)
	}

	scriptDir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get current directory: %w", err)
	}

	// Define paths
	mainRepoDir := filepath.Join(scriptDir, "nixpkgs-repo.git") // Use .git convention for bare repo
	worktreeDir := filepath.Join(scriptDir, fmt.Sprintf("nixpkgs-worktree-%s", rev))
	repoURL := "https://github.com/NixOS/nixpkgs.git"

	// --- Ensure Main Bare Repository Exists ---
	if _, err := os.Stat(mainRepoDir); os.IsNotExist(err) {
		log.Printf("Main bare repository not found. Initializing shallow clone at %s", mainRepoDir)
		// Create parent directory if needed
		if err := os.MkdirAll(filepath.Dir(mainRepoDir), 0o755); err != nil {
			return "", fmt.Errorf("failed to create parent directory for bare repo: %w", err)
		}

		// Initialize bare repository
		initCmd := exec.Command("git", "init", "--bare", mainRepoDir)
		if output, err := initCmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("failed to initialize bare repository: %w\nOutput:\n%s", err, string(output))
		}

		// Add remote origin
		remoteCmd := exec.Command("git", "-C", mainRepoDir, "remote", "add", "origin", repoURL)
		if output, err := remoteCmd.CombinedOutput(); err != nil {
			// Clean up initialized repo if adding remote fails
			os.RemoveAll(mainRepoDir)
			return "", fmt.Errorf("failed to add remote 'origin': %w\nOutput:\n%s", err, string(output))
		}
		log.Printf("Bare repository initialized and remote 'origin' added.")
	} else {
		log.Printf("Using existing bare repository at %s", mainRepoDir)
	}

	// --- Clean Up Existing Worktree (if any) ---
	// First, try removing via git worktree remove (safer)
	removeCmd := exec.Command("git", "-C", mainRepoDir, "worktree", "remove", worktreeDir)
	removeOutput, removeErr := removeCmd.CombinedOutput()
	if removeErr == nil {
		log.Printf("Successfully removed existing worktree entry for %s using 'git worktree remove'.", worktreeDir)
	} else {
		// If 'git worktree remove' fails (e.g., directory doesn't exist or not registered), log and proceed
		// It might have been partially deleted before.
		log.Printf("Info: 'git worktree remove %s' failed (maybe already removed or inconsistent state): %v\nOutput:\n%s", worktreeDir, removeErr, string(removeOutput))
		// Forcefully remove the directory if it still exists
		if _, statErr := os.Stat(worktreeDir); statErr == nil {
			log.Printf("Forcefully removing existing worktree directory: %s", worktreeDir)
			if err := os.RemoveAll(worktreeDir); err != nil {
				return "", fmt.Errorf("failed to forcefully remove existing worktree directory %s: %w", worktreeDir, err)
			}
		}
	}

	// --- Prune Stale Worktrees ---
	pruneCmd := exec.Command("git", "-C", mainRepoDir, "worktree", "prune")
	if output, err := pruneCmd.CombinedOutput(); err != nil {
		// Log prune failure as a warning, as it might not be critical
		log.Printf("Warning: 'git worktree prune' failed: %v\nOutput:\n%s", err, string(output))
	}

	// --- Fetch the Specific Commit ---
	log.Printf("Fetching commit %s from origin...", rev)
	// Fetch only the required commit, unshallowing if necessary
	// Using --update-head-ok allows fetching into the bare repo's refs/heads space if needed
	// Using --depth=1 might fail if the commit is not recent; fetching without depth is safer but slower.
	// Let's try fetching without depth first, it's more reliable for arbitrary commits.
	// fetchCmd := exec.Command("git", "-C", mainRepoDir, "fetch", "--update-head-ok", "origin", rev)
	// Optimization: Try fetching with depth 1 first, if it fails, fetch full history for that commit.
	fetchCmd := exec.Command("git", "-C", mainRepoDir, "fetch", "--depth=1", "--update-head-ok", "origin", rev)
	fetchOutput, fetchErr := fetchCmd.CombinedOutput()
	if fetchErr != nil {
		log.Printf("Warning: Shallow fetch failed for %s (commit might be old): %v. Trying full fetch...", rev, fetchErr)
		// Fallback to fetching the specific commit without depth limit
		fetchCmd = exec.Command("git", "-C", mainRepoDir, "fetch", "--update-head-ok", "origin", rev)
		fetchOutput, fetchErr = fetchCmd.CombinedOutput()
		if fetchErr != nil {
			return "", fmt.Errorf("failed to fetch revision %s: %w\nOutput:\n%s", rev, fetchErr, string(fetchOutput))
		}
	}
	log.Printf("Successfully fetched commit %s.", rev)

	// --- Create the Worktree ---
	log.Printf("Creating worktree for revision %s at %s", rev, worktreeDir)
	// Use --detach to avoid creating a branch
	addCmd := exec.Command("git", "-C", mainRepoDir, "worktree", "add", "--detach", worktreeDir, rev)
	if output, err := addCmd.CombinedOutput(); err != nil {
		// If creation fails, try to clean up the potentially partially created directory
		os.RemoveAll(worktreeDir)
		return "", fmt.Errorf("failed to create worktree at %s for %s: %w\nOutput:\n%s", worktreeDir, rev, err, string(output))
	}

	// --- Verify Worktree (Optional) ---
	// Check for a common file to sanity check the checkout
	verifyPath := filepath.Join(worktreeDir, "default.nix") // Or another stable top-level file
	if _, err := os.Stat(verifyPath); os.IsNotExist(err) {
		log.Printf("Warning: Verification file %s not found in worktree. Nixpkgs structure might be unexpected for revision %s.", verifyPath, rev)
	}

	log.Printf("Successfully prepared worktree for revision %s at %s", rev, worktreeDir)
	return worktreeDir, nil
}

// isHex checks if a string contains only hexadecimal characters.
func isHex(s string) bool {
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

// isWorktreeDir checks if a directory name matches the worktree pattern.
func isWorktreeDir(name string) bool {
	// Match nixpkgs-worktree-<40-hex-chars>
	if !strings.HasPrefix(name, "nixpkgs-worktree-") {
		return false
	}
	hashPart := strings.TrimPrefix(name, "nixpkgs-worktree-")
	return len(hashPart) == 40 && isHex(hashPart)
}

// cleanupWorktrees removes leftover worktree directories and prunes git worktree state.
func cleanupWorktrees() error {
	scriptDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get current directory: %w", err)
	}
	log.Println("Starting cleanup of old worktrees...")

	mainRepoDir := filepath.Join(scriptDir, "nixpkgs-repo.git")
	if _, err := os.Stat(mainRepoDir); os.IsNotExist(err) {
		log.Println("Main repository directory not found, skipping git prune.")
	} else {
		// Prune worktrees known to git
		pruneCmd := exec.Command("git", "-C", mainRepoDir, "worktree", "prune")
		pruneOutput, pruneErr := pruneCmd.CombinedOutput()
		if pruneErr != nil {
			log.Printf("Warning: 'git worktree prune' failed: %v\nOutput:\n%s", pruneErr, string(pruneOutput))
		} else {
			log.Println("Git worktree prune completed.")
		}
	}

	// Find and remove any leftover worktree directories by pattern matching
	entries, err := os.ReadDir(scriptDir)
	if err != nil {
		return fmt.Errorf("failed to read script directory %s: %w", scriptDir, err)
	}

	removedCount := 0
	for _, entry := range entries {
		if entry.IsDir() && isWorktreeDir(entry.Name()) {
			worktreePath := filepath.Join(scriptDir, entry.Name())
			log.Printf("Removing leftover worktree directory: %s", worktreePath)
			if err := os.RemoveAll(worktreePath); err != nil {
				// Log warning but continue trying to remove others
				log.Printf("Warning: Failed to remove directory %s: %v", worktreePath, err)
			} else {
				removedCount++
			}
		}
	}
	log.Printf("Removed %d leftover worktree directories.", removedCount)
	log.Println("Worktree cleanup finished.")
	return nil
}

func main() {
	// Setup logging
	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.Lshortfile) // Include file/line number
	log.Println("=================================================")
	log.Println("Starting Nixpkgs FOD Finder")
	log.Println("=================================================")

	// Log initial configuration and system info
	log.Printf("Configuration: nix-eval-jobs workers = %d", nixEvalJobsWorkers)
	log.Printf("System Info: Host=%s, OS=%s, Kernel=%s", systemInfo.Host, systemInfo.OS, systemInfo.Kernel)
	log.Printf("System Info: CPU=%s (Logical Cores=%s), Memory=%s", systemInfo.CPU, systemInfo.Cores, systemInfo.Memory)

	// --- Initial Cleanup ---
	if err := cleanupWorktrees(); err != nil {
		// Log as warning, cleanup failure shouldn't necessarily stop the main process
		log.Printf("Warning: Initial worktree cleanup failed: %v", err)
	}

	// --- Database Initialization ---
	db := initDB()
	defer func() {
		log.Println("Closing database connection...")
		if err := db.Close(); err != nil {
			log.Printf("Error closing database: %v", err)
		} else {
			log.Println("Database connection closed.")
		}
	}()

	// --- Process Revisions ---
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <nixpkgs-revision-hash> [<nixpkgs-revision-hash2> ...]\n", os.Args[0])
		os.Exit(1)
	}
	revisions := os.Args[1:]
	log.Printf("Processing %d nixpkgs revisions: %v", len(revisions), revisions)

	startTime := time.Now()
	errorsEncountered := 0

	for i, rev := range revisions {
		log.Printf("--- Starting processing for revision %d/%d: %s ---", i+1, len(revisions), rev)
		// Validate revision format basic check
		if len(rev) != 40 || !isHex(rev) {
			log.Printf("ERROR: Invalid revision format '%s'. Skipping.", rev)
			errorsEncountered++
			continue
		}

		revisionID, err := getOrCreateRevision(db, rev)
		if err != nil {
			log.Printf("ERROR: Failed to get or create revision_id for %s: %v. Skipping.", rev, err)
			errorsEncountered++
			continue
		}

		if err := findFODsForRevision(rev, revisionID, db); err != nil {
			log.Printf("ERROR: Failed processing revision %s: %v", rev, err)
			errorsEncountered++
			// Continue to the next revision even if one fails
		}
		log.Printf("--- Finished processing for revision %d/%d: %s ---", i+1, len(revisions), rev)
	}

	totalElapsed := time.Since(startTime)
	log.Println("=================================================")
	log.Printf("Finished processing all %d revisions in %v.", len(revisions), totalElapsed)
	if errorsEncountered > 0 {
		log.Printf("WARNING: Encountered errors during processing for %d revisions.", errorsEncountered)
	}
	log.Println("=================================================")

	// --- Final Database Optimization ---
	log.Println("Optimizing database (PRAGMA optimize, ANALYZE)...")
	optimizeStart := time.Now()
	// Run optimize first
	if _, err := db.Exec("PRAGMA optimize;"); err != nil {
		log.Printf("Warning: Failed to execute PRAGMA optimize: %v", err)
	}
	// Then run analyze
	if _, err := db.Exec("ANALYZE;"); err != nil {
		log.Printf("Warning: Failed to execute ANALYZE: %v", err)
	}
	// VACUUM is less critical with WAL mode and can be slow/blocking, run optionally or less frequently
	// if _, err := db.Exec("VACUUM;"); err != nil {
	// 	log.Printf("Warning: Failed to execute VACUUM: %v", err)
	// }
	log.Printf("Database optimization finished in %v.", time.Since(optimizeStart))

	// --- Final Cleanup ---
	if err := cleanupWorktrees(); err != nil {
		log.Printf("Warning: Final worktree cleanup failed: %v", err)
	}

	log.Println("=================================================")
	if errorsEncountered == 0 {
		log.Println("FOD Finder completed successfully.")
	} else {
		log.Println("FOD Finder completed with errors.")
	}
	log.Println("=================================================")

	if errorsEncountered > 0 {
		os.Exit(1) // Exit with error code if any revision failed
	}
}
