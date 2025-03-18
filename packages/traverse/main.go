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

	// Set connection pool settings
	db.SetMaxOpenConns(runtime.NumCPU() * 2)
	db.SetMaxIdleConns(runtime.NumCPU())
	db.SetConnMaxLifetime(time.Hour)

	// Create a single table for FODs only
	createTable := `
	CREATE TABLE IF NOT EXISTS fods (
		drv_path TEXT PRIMARY KEY,
		output_path TEXT NOT NULL,
		hash_algorithm TEXT NOT NULL,
		hash TEXT NOT NULL,
		timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_hash ON fods(hash);
	CREATE INDEX IF NOT EXISTS idx_hash_algo ON fods(hash_algorithm);
	`

	_, err = db.Exec(createTable)
	if err != nil {
		log.Fatalf("Failed to create table: %v", err)
	}

	// Set pragmas for better performance
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA cache_size=100000",
		"PRAGMA temp_store=MEMORY",
		"PRAGMA mmap_size=30000000000",
		"PRAGMA page_size=32768",
	}

	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			log.Printf("Warning: Failed to set pragma %s: %v", pragma, err)
		}
	}

	return db
}

// DBBatcher handles batched database operations
type DBBatcher struct {
	db           *sql.DB
	batch        []FOD
	batchSize    int
	mutex        sync.Mutex
	commitTicker *time.Ticker
	wg           sync.WaitGroup
	done         chan struct{}
	stmt         *sql.Stmt
	stats        struct {
		drvs int
		fods int
		sync.Mutex
	}
}

// NewDBBatcher creates a new database batcher
func NewDBBatcher(db *sql.DB, batchSize int, commitInterval time.Duration) (*DBBatcher, error) {
	// Prepare statement once
	stmt, err := db.Prepare(`
		INSERT INTO fods (drv_path, output_path, hash_algorithm, hash)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(drv_path) DO UPDATE SET 
		output_path = ?,
		hash_algorithm = ?,
		hash = ?
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare statement: %w", err)
	}

	batcher := &DBBatcher{
		db:           db,
		batch:        make([]FOD, 0, batchSize),
		batchSize:    batchSize,
		commitTicker: time.NewTicker(commitInterval),
		done:         make(chan struct{}),
		stmt:         stmt,
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

	b.batch = append(b.batch, fod)

	b.stats.Lock()
	b.stats.fods++
	b.stats.Unlock()

	if len(b.batch) >= b.batchSize {
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
	if len(b.batch) == 0 {
		return
	}

	tx, err := b.db.Begin()
	if err != nil {
		log.Printf("Failed to begin transaction: %v", err)
		return
	}

	stmt := tx.Stmt(b.stmt)

	for _, fod := range b.batch {
		_, err := stmt.Exec(
			fod.DrvPath, fod.OutputPath, fod.HashAlgorithm, fod.Hash,
			fod.OutputPath, fod.HashAlgorithm, fod.Hash,
		)
		if err != nil {
			log.Printf("Failed to insert FOD %s: %v", fod.DrvPath, err)
		}
	}

	if err := tx.Commit(); err != nil {
		log.Printf("Failed to commit transaction: %v", err)
		tx.Rollback()
		return
	}

	b.batch = b.batch[:0]
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

	if err := b.stmt.Close(); err != nil {
		return err
	}

	return nil
}

// Worker represents a worker that processes derivations
type Worker struct {
	id          int
	batcher     *DBBatcher
	jobChan     chan string
	wg          *sync.WaitGroup
	visited     *sync.Map
	pendingJobs *sync.Map
}

// NewWorker creates a new worker
func NewWorker(id int, batcher *DBBatcher, jobChan chan string, wg *sync.WaitGroup, visited *sync.Map, pendingJobs *sync.Map) *Worker {
	return &Worker{
		id:          id,
		batcher:     batcher,
		jobChan:     jobChan,
		wg:          wg,
		visited:     visited,
		pendingJobs: pendingJobs,
	}
}

// Start starts the worker
func (w *Worker) Start() {
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		for drvPath := range w.jobChan {
			w.processDerivation(drvPath)
			// Mark this job as done
			w.pendingJobs.Delete(drvPath)
		}
	}()
}

// processDerivation processes a derivation
func (w *Worker) processDerivation(inputFile string) {
	// Increment derivation count for statistics
	w.batcher.IncrementDrvCount()

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
			w.batcher.AddFOD(fod)

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
		if _, alreadyVisited := w.visited.LoadOrStore(path, true); !alreadyVisited {
			// Mark as pending before sending to avoid race conditions
			if _, alreadyPending := w.pendingJobs.LoadOrStore(path, true); !alreadyPending {
				// Use a separate goroutine to avoid deadlocks if the channel is full
				go func(p string) {
					w.jobChan <- p
				}(path)
			}
		}
	}
}

// WorkerPool manages a pool of workers
type WorkerPool struct {
	workers     []*Worker
	jobChan     chan string
	wg          sync.WaitGroup
	visited     *sync.Map
	pendingJobs *sync.Map
}

// NewWorkerPool creates a new worker pool
func NewWorkerPool(numWorkers int, batcher *DBBatcher) *WorkerPool {
	pool := &WorkerPool{
		workers:     make([]*Worker, numWorkers),
		jobChan:     make(chan string, 100000), // Large buffer to avoid blocking
		visited:     &sync.Map{},
		pendingJobs: &sync.Map{},
	}

	// Create and start workers
	for i := range numWorkers {
		pool.workers[i] = NewWorker(i, batcher, pool.jobChan, &pool.wg, pool.visited, pool.pendingJobs)
		pool.workers[i].Start()
	}

	return pool
}

// AddJob adds a job to the pool
func (p *WorkerPool) AddJob(drvPath string) {
	if _, alreadyVisited := p.visited.LoadOrStore(drvPath, true); !alreadyVisited {
		if _, alreadyPending := p.pendingJobs.LoadOrStore(drvPath, true); !alreadyPending {
			p.jobChan <- drvPath
		}
	}
}

// Wait waits for all jobs to complete
func (p *WorkerPool) Wait() {
	// Close the job channel when all jobs are done
	// We need to periodically check if there are pending jobs
	for {
		time.Sleep(100 * time.Millisecond)

		// Count pending jobs
		pendingCount := 0
		p.pendingJobs.Range(func(_, _ any) bool {
			pendingCount++
			return true
		})

		if pendingCount == 0 {
			// No more pending jobs, we can close the channel
			close(p.jobChan)
			break
		}
	}

	// Wait for all workers to finish
	p.wg.Wait()
}

// callNixEvalJobs calls nix-eval-jobs and sends derivation paths to the worker pool
func callNixEvalJobs(pool *WorkerPool) error {
	cmd := exec.Command("nix-eval-jobs", "--workers", "8", "full-nixpkgs.nix") // Customize arguments as needed
	cmd.Stderr = os.Stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	err = cmd.Start()
	if err != nil {
		return fmt.Errorf("failed to start nix-eval-jobs: %w", err)
	}

	scanner := bufio.NewScanner(stdout)
	// Increase scanner buffer size for large outputs
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
			pool.AddJob(result.DrvPath)
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

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Println("Starting FOD finder...")

	// Initialize database
	db := initDB()
	defer db.Close()

	// Create batcher for efficient database operations
	batcher, err := NewDBBatcher(db, 5000, 3*time.Second)
	if err != nil {
		log.Fatalf("Failed to create database batcher: %v", err)
	}
	defer batcher.Close()

	// Start the process
	startTime := time.Now()
	log.Println("Starting to find all FODs...")

	// Create worker pool
	numWorkers := runtime.NumCPU() * 2
	log.Printf("Starting %d worker goroutines", numWorkers)
	pool := NewWorkerPool(numWorkers, batcher)

	// Call nix-eval-jobs
	if err := callNixEvalJobs(pool); err != nil {
		log.Fatalf("Error calling nix-eval-jobs: %v", err)
	}

	// Wait for all jobs to complete
	pool.Wait()

	// Ensure all data is written
	batcher.Flush()

	elapsed := time.Since(startTime)
	log.Printf("Process completed in %v", elapsed)

	// Print final statistics
	var fodCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM fods").Scan(&fodCount); err != nil {
		log.Printf("Error counting FODs: %v", err)
	}

	log.Printf("Final database stats: %d FODs", fodCount)
	log.Printf("Average processing rate: %.2f derivations/second",
		float64(batcher.stats.drvs)/elapsed.Seconds())

	// Print some useful queries for analysis
	log.Println("Useful queries for analysis:")
	log.Println("- Count FODs by hash algorithm: SELECT hash_algorithm, COUNT(*) FROM fods GROUP BY hash_algorithm ORDER BY COUNT(*) DESC;")
	log.Println("- Find most common hashes: SELECT hash, COUNT(*) FROM fods GROUP BY hash HAVING COUNT(*) > 1 ORDER BY COUNT(*) DESC LIMIT 20;")
}
