package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/multivac61/fod-oracle/pkg/fod"
)

// debugLogQueue logs a message only if debug mode is enabled
func debugLogQueue(format string, v ...interface{}) {
	if config.Debug {
		log.Printf(format, v...)
	}
}

// RebuildJob represents a job to rebuild a FOD
type RebuildJob struct {
	ID           int64
	DrvPath      string
	RevisionID   int64
	Status       string
	QueuedAt     time.Time
	StartedAt    *time.Time
	FinishedAt   *time.Time
	ExpectedHash string
	ActualHash   string
	Log          string
	Attempts     int
	ErrorMessage string
}

// Queue status constants
const (
	StatusPending      = "pending"
	StatusRunning      = "running"
	StatusSuccess      = "success"
	StatusFailure      = "failure"
	StatusTimeout      = "timeout"
	StatusHashMismatch = "hash_mismatch"
)

// RebuildQueue manages the queue of FODs to rebuild
type RebuildQueue struct {
	db                     *sql.DB
	buildChan              chan RebuildJob
	delay                  time.Duration
	lastEnd                time.Time
	wg                     *sync.WaitGroup
	stopped                bool
	running                bool
	mutex                  sync.Mutex
	hasShownRebuildMessage bool // Track if we've shown the rebuild message
}

// NewRebuildQueue creates a new rebuild queue
func NewRebuildQueue(db *sql.DB, concurrency int, delaySeconds int) *RebuildQueue {
	return &RebuildQueue{
		db:        db,
		buildChan: make(chan RebuildJob, concurrency*2),
		delay:     time.Duration(delaySeconds) * time.Second,
		wg:        &sync.WaitGroup{},
		running:   false,
		stopped:   false,
	}
}

// QueueFODsForRevision adds all FODs for a revision to the rebuild queue
func (q *RebuildQueue) QueueFODsForRevision(revisionID int64) (int, error) {
	// First count how many FODs we have for this revision
	var totalCount int
	var err error
	if config.IsNixExpr {
		// For Nix expressions, just count all FODs
		err = q.db.QueryRow(`SELECT COUNT(*) FROM fods`).Scan(&totalCount)
		debugLogQueue("DEBUG: Using IsNixExpr path, found %d FODs", totalCount)
	} else {
		// For regular revisions, count FODs associated with this revision
		err = q.db.QueryRow(`
			SELECT COUNT(*) FROM drv_revisions 
			WHERE revision_id = ?
		`, revisionID).Scan(&totalCount)
	}
	if err != nil {
		return 0, fmt.Errorf("failed to count total FODs: %w", err)
	}

	if totalCount == 0 {
		if config.IsNixExpr {
			debugLogQueue("No FODs found for expression")
		} else {
			debugLogQueue("No FODs found for revision ID %d", revisionID)
		}
		return 0, nil
	}

	// Then count how many are already in the queue
	var existingCount int
	err = q.db.QueryRow(`
		SELECT COUNT(*) FROM rebuild_queue 
		WHERE revision_id = ?
	`, revisionID).Scan(&existingCount)
	if err != nil {
		return 0, fmt.Errorf("failed to count existing queue items: %w", err)
	}

	// If all FODs are already in the queue, return 0
	if existingCount >= totalCount {
		// Check if any are still pending
		var pendingCount int
		err = q.db.QueryRow(`
			SELECT COUNT(*) FROM rebuild_queue 
			WHERE revision_id = ? AND status = ?
		`, revisionID, StatusPending).Scan(&pendingCount)
		if err != nil {
			return 0, fmt.Errorf("failed to count pending queue items: %w", err)
		}

		// If some are still pending, return that count
		if pendingCount > 0 {
			return pendingCount, nil
		}

		// If none are pending, we need to reset any that failed
		_, err = q.db.Exec(`
			UPDATE rebuild_queue
			SET status = ?, attempts = 0, started_at = NULL, finished_at = NULL, 
			    actual_hash = NULL, log = NULL, error_message = NULL
			WHERE revision_id = ? AND status IN (?, ?)
		`, StatusPending, revisionID, StatusFailure, StatusTimeout)
		if err != nil {
			return 0, fmt.Errorf("failed to reset failed queue items: %w", err)
		}

		// Count how many we reset
		var resetCount int
		err = q.db.QueryRow(`
			SELECT COUNT(*) FROM rebuild_queue 
			WHERE revision_id = ? AND status = ?
		`, revisionID, StatusPending).Scan(&resetCount)
		if err != nil {
			return 0, fmt.Errorf("failed to count reset queue items: %w", err)
		}

		if resetCount > 0 {
			debugLogQueue("Reset %d failed FODs to pending for revision ID %d", resetCount, revisionID)
			return resetCount, nil
		}

		return 0, nil
	}

	// Insert all FODs for this revision into the queue
	var result sql.Result
	if config.IsNixExpr {
		// For Nix expressions, queue all FODs
		debugLogQueue("Queuing all FODs for expression (total: %d)", totalCount)
		result, err = q.db.Exec(`
			INSERT INTO rebuild_queue (drv_path, revision_id, expected_hash, status)
			SELECT f.drv_path, ?, f.hash, ?
			FROM fods f
			WHERE f.drv_path NOT IN (SELECT drv_path FROM rebuild_queue WHERE revision_id = ?)
		`, revisionID, StatusPending, revisionID)
	} else {
		// For regular revisions, only queue FODs associated with this revision
		result, err = q.db.Exec(`
			INSERT INTO rebuild_queue (drv_path, revision_id, expected_hash, status)
			SELECT dr.drv_path, dr.revision_id, f.hash, ?
			FROM drv_revisions dr
			JOIN fods f ON dr.drv_path = f.drv_path
			WHERE dr.revision_id = ?
			AND dr.drv_path NOT IN (SELECT drv_path FROM rebuild_queue WHERE revision_id = ?)
		`, StatusPending, revisionID, revisionID)
	}
	if err != nil {
		return 0, fmt.Errorf("failed to queue FODs: %w", err)
	}

	// Get the number of rows inserted
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("failed to get rows affected: %w", err)
	}

	debugLogQueue("Queued %d FODs for rebuild for revision ID %d", rowsAffected, revisionID)
	return int(rowsAffected), nil
}

// Start starts the rebuild queue runner
func (q *RebuildQueue) Start(concurrency int, writer Writer) {
	q.mutex.Lock()
	if q.stopped {
		q.stopped = false
	}
	q.running = true
	q.mutex.Unlock()

	// Make sure the rebuild_queue table exists before we start
	// This prevents errors when workers start fetching jobs
	err := q.ensureRebuildQueueTableExists()
	if err != nil {
		debugLogQueue("Error ensuring rebuild_queue table exists: %v", err)
	}

	// Start worker goroutines
	for i := 0; i < concurrency; i++ {
		q.wg.Add(1)
		go func(workerID int) {
			defer q.wg.Done()
			debugLogQueue("Starting rebuild worker %d", workerID)
			for job := range q.buildChan {
				status, actualHash, errorMsg := q.processJob(job)

				// Pass rebuild data to the writer
				debugLogQueue("DEBUG: Adding rebuild info to writer: %s, status: %s, hash: %s", job.DrvPath, status, actualHash)
				writer.AddRebuildInfo(job.DrvPath, status, actualHash, errorMsg)
			}
		}(i)
	}
	
	// Start the job fetcher goroutine
	q.startJobFetcher()
}

// ensureRebuildQueueTableExists creates the rebuild_queue table if it doesn't exist
func (q *RebuildQueue) ensureRebuildQueueTableExists() error {
	// Check if the rebuild_queue table exists
	var tableName string
	err := q.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='rebuild_queue'`).Scan(&tableName)
	if err != nil {
		if err == sql.ErrNoRows {
			// Table doesn't exist, create it
			debugLogQueue("Creating rebuild_queue table...")
			_, err = q.db.Exec(`
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
				CREATE INDEX IF NOT EXISTS idx_rebuild_queue_drv_path_revision_id ON rebuild_queue(drv_path, revision_id);
			`)
			if err != nil {
				return fmt.Errorf("failed to create rebuild_queue table: %w", err)
			}
			return nil
		}
		return fmt.Errorf("failed to check if rebuild_queue table exists: %w", err)
	}
	return nil
}

// Start starts the job fetcher goroutine
func (q *RebuildQueue) startJobFetcher() {
	// Start job fetcher
	go func() {
		defer func() {
			// Ensure we mark as not running when done
			q.mutex.Lock()
			q.running = false
			q.mutex.Unlock()

			// Close the channel to signal workers to exit
			close(q.buildChan)
		}()

		// Track consecutive empty job attempts
		emptyAttempts := 0

		for {
			q.mutex.Lock()
			stopped := q.stopped
			q.mutex.Unlock()
			if stopped {
				debugLogQueue("Job fetcher: stopping due to stop signal")
				break
			}

			// Get the next job
			job, err := q.fetchNextJob()
			if err != nil {
				if err != sql.ErrNoRows {
					debugLogQueue("Error fetching next job: %v", err)
				}

				// Use a shorter wait time if delay is set to 0 (testing mode)
				if q.delay <= 0 {
					time.Sleep(100 * time.Millisecond) // Much faster for testing
				} else {
					time.Sleep(1 * time.Second)
				}

				// Increment empty attempts
				emptyAttempts++

				// After 5 consecutive empty attempts, check if we're done
				if emptyAttempts >= 5 {
					pending, err := q.countPendingJobs()
					if err != nil {
						debugLogQueue("Error counting pending jobs: %v", err)
					} else if pending == 0 {
						debugLogQueue("Job fetcher: no more pending jobs, exiting")
						break
					}
				}

				continue
			}

			if job == nil {
				// No jobs available, wait before checking again
				// Use a shorter wait time if delay is set to 0 (testing mode)
				if q.delay <= 0 {
					time.Sleep(100 * time.Millisecond) // Much faster for testing
				} else {
					time.Sleep(1 * time.Second)
				}

				// Increment empty attempts
				emptyAttempts++

				// After 5 consecutive empty attempts, check if we're done
				if emptyAttempts >= 5 {
					pending, err := q.countPendingJobs()
					if err != nil {
						debugLogQueue("Error counting pending jobs: %v", err)
					} else if pending == 0 {
						debugLogQueue("Job fetcher: no more pending jobs, exiting")
						break
					}
				}

				continue
			}

			// Reset empty attempts counter when we find a job
			emptyAttempts = 0

			// Apply build delay if needed
			q.applyBuildDelay()

			// Send the job to a worker
			q.buildChan <- *job
		}
	}()
}

// countPendingJobs returns the number of pending jobs
func (q *RebuildQueue) countPendingJobs() (int, error) {
	// First check if the table exists
	var tableCount int
	err := q.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='rebuild_queue'").Scan(&tableCount)
	if err != nil || tableCount == 0 {
		// Table doesn't exist or there was an error checking
		return 0, nil // Return 0 as if no pending jobs
	}

	// Count pending jobs
	var count int
	err = q.db.QueryRow(`SELECT COUNT(*) FROM rebuild_queue WHERE status = ?`, StatusPending).Scan(&count)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			// Table doesn't exist anymore
			return 0, nil
		}
		return 0, fmt.Errorf("failed to count pending jobs: %w", err)
	}
	return count, nil
}

// IsRunning returns whether the queue is still running
func (q *RebuildQueue) IsRunning() bool {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	return q.running
}

// Stop stops the rebuild queue runner
func (q *RebuildQueue) Stop() {
	q.mutex.Lock()
	q.stopped = true
	q.mutex.Unlock()

	// Wait for workers to complete with a timeout
	done := make(chan struct{})
	go func() {
		q.wg.Wait()
		close(done)
	}()

	// Wait with timeout
	select {
	case <-done:
		// Workers completed normally
	case <-time.After(10 * time.Second):
		// Timed out waiting for workers
		debugLogQueue("Warning: Timed out waiting for rebuild workers to complete")
	}
}

// Wait waits for all jobs to be processed
func (q *RebuildQueue) Wait() {
	q.wg.Wait()
}

// fetchNextJob gets the next job from the queue
func (q *RebuildQueue) fetchNextJob() (*RebuildJob, error) {
	// First check if the table exists
	var tableCount int
	err := q.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='rebuild_queue'").Scan(&tableCount)
	if err != nil || tableCount == 0 {
		// Table doesn't exist or there was an error checking
		return nil, sql.ErrNoRows // Return as if no rows found
	}

	// Add retries for database transactions to handle contention
	var job *RebuildJob

	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			// Add backoff between attempts
			if q.delay <= 0 {
				time.Sleep(10 * time.Millisecond) // Minimal delay for testing
			} else {
				time.Sleep(time.Duration(100*attempt) * time.Millisecond)
			}
		}

		job, err = q.attemptFetchNextJob()
		
		if err == nil || err == sql.ErrNoRows {
			return job, nil
		}

		// Handle case where table doesn't exist anymore
		if strings.Contains(err.Error(), "no such table") {
			return nil, sql.ErrNoRows
		}

		// Only retry on database locks or busy errors
		if !strings.Contains(err.Error(), "database is locked") &&
			!strings.Contains(err.Error(), "database is busy") {
			return nil, err
		}

		debugLogQueue("Database locked, retrying fetch (attempt %d/5)", attempt+1)
	}

	return nil, fmt.Errorf("failed to fetch job after retries: %w", err)
}

// attemptFetchNextJob makes a single attempt to fetch the next job
func (q *RebuildQueue) attemptFetchNextJob() (*RebuildJob, error) {
	tx, err := q.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Set a busy timeout on this connection
	_, err = tx.Exec("PRAGMA busy_timeout = 5000")
	if err != nil {
		return nil, fmt.Errorf("failed to set busy timeout: %w", err)
	}

	// Get the next pending job
	var job RebuildJob
	err = tx.QueryRow(`
		SELECT id, drv_path, revision_id, expected_hash, attempts
		FROM rebuild_queue
		WHERE status = ?
		ORDER BY id ASC
		LIMIT 1
	`, StatusPending).Scan(&job.ID, &job.DrvPath, &job.RevisionID, &job.ExpectedHash, &job.Attempts)
	if err == sql.ErrNoRows {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get next job: %w", err)
	}

	// Mark the job as running
	now := time.Now()
	_, err = tx.Exec(`
		UPDATE rebuild_queue
		SET status = ?, started_at = ?, attempts = attempts + 1
		WHERE id = ?
	`, StatusRunning, now, job.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to update job status: %w", err)
	}

	// Commit the transaction
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	job.Status = StatusRunning
	job.StartedAt = &now
	job.Attempts++

	return &job, nil
}

// applyBuildDelay enforces a delay between builds
func (q *RebuildQueue) applyBuildDelay() {
	// If delay is 0, don't apply any delay
	if q.delay <= 0 {
		return
	}

	q.mutex.Lock()
	defer q.mutex.Unlock()

	if q.lastEnd.IsZero() {
		return
	}

	elapsed := time.Since(q.lastEnd)
	if elapsed < q.delay {
		time.Sleep(q.delay - elapsed)
	}
}

// markBuildComplete records the end time of a build
func (q *RebuildQueue) markBuildComplete() {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	q.lastEnd = time.Now()
}

// processJob handles a single rebuild job
// Returns status, actualHash, and errorMessage for use by writers
func (q *RebuildQueue) processJob(job RebuildJob) (string, string, string) {
	debugLogQueue("Processing job #%d: %s", job.ID, job.DrvPath)

	// Run the rebuild-fod command
	outputLog, err := q.rebuildFOD(job.DrvPath)
	q.markBuildComplete()

	// Get the current time for finished_at
	now := time.Now()
	job.FinishedAt = &now

	// Parse the result
	var (
		status       = StatusSuccess
		actualHash   = ""
		errorMessage = ""
	)

	if err != nil {
		// Handle timeout
		if strings.Contains(err.Error(), "signal: killed") ||
			strings.Contains(err.Error(), "context deadline exceeded") ||
			strings.Contains(err.Error(), "timeout") {
			status = StatusTimeout
			errorMessage = "Build timed out"
		} else {
			status = StatusFailure
			errorMessage = err.Error()
		}
	}

	// Extract information from the output log
	for _, line := range strings.Split(outputLog, "\n") {
		// Extract status if present
		if strings.HasPrefix(line, "STATUS=") {
			outStatus := strings.TrimPrefix(line, "STATUS=")
			if outStatus == "timeout" {
				status = StatusTimeout
			} else if outStatus == "failure" {
				status = StatusFailure
			} else if outStatus == "hash_mismatch" {
				status = StatusHashMismatch
			}
		}

		// Extract actual hash if present
		if strings.HasPrefix(line, "ACTUAL_HASH=") {
			actualHash = strings.TrimPrefix(line, "ACTUAL_HASH=")
		}

		// Extract error message if present
		if strings.HasPrefix(line, "ERROR_MESSAGE=") {
			newErrorMsg := strings.TrimPrefix(line, "ERROR_MESSAGE=")
			if newErrorMsg != "" {
				errorMessage = newErrorMsg
			}
		}
	}

	// Check if the hash matches the expected hash
	if status == StatusSuccess && actualHash != "" && actualHash != job.ExpectedHash {
		status = StatusHashMismatch
		errorMessage = fmt.Sprintf("Hash mismatch: expected %s, got %s", job.ExpectedHash, actualHash)
	}

	// Update the job in the database with retries for database locks
	var dbErr error
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			// Add backoff between attempts
			if q.delay <= 0 {
				time.Sleep(10 * time.Millisecond) // Minimal delay for testing
			} else {
				time.Sleep(time.Duration(100*attempt) * time.Millisecond)
			}
		}

		// First check if the table exists
		var tableCount int
		err := q.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='rebuild_queue'").Scan(&tableCount)
		if err != nil || tableCount == 0 {
			// Table doesn't exist or there was an error checking - don't try to update
			// This can happen during in-memory database conversion to file formats
			// Just log it once and continue with the rest of the process
			if attempt == 0 {
				// Only log on the first attempt to reduce noise
				debugLogQueue("Cannot update job #%d: rebuild_queue table no longer exists, continuing with job results", job.ID)
			}
			dbErr = nil
			break
		}

		// Table exists, proceed with update
		tx, err := q.db.Begin()
		if err != nil {
			debugLogQueue("Error beginning transaction for job #%d: %v", job.ID, err)
			continue
		}

		// Set a busy timeout
		_, err = tx.Exec("PRAGMA busy_timeout = 5000")
		if err != nil {
			tx.Rollback()
			debugLogQueue("Error setting busy timeout for job #%d: %v", job.ID, err)
			continue
		}

		_, err = tx.Exec(`
			UPDATE rebuild_queue
			SET status = ?, finished_at = ?, actual_hash = ?, log = ?, error_message = ?
			WHERE id = ?
		`, status, now, actualHash, outputLog, errorMessage, job.ID)
		if err != nil {
			tx.Rollback()
			if strings.Contains(err.Error(), "database is locked") ||
				strings.Contains(err.Error(), "database is busy") ||
				strings.Contains(err.Error(), "no such table") {
				if strings.Contains(err.Error(), "no such table") {
					// Table has been dropped (may happen during database conversion)
					if attempt == 0 {
						debugLogQueue("rebuild_queue table no longer exists for job #%d, continuing with job results", job.ID)
					}
					dbErr = nil
					break
				}
				debugLogQueue("Database locked, retrying update for job #%d (attempt %d/5)", job.ID, attempt+1)
				dbErr = err
				continue
			}
			dbErr = err
			break
		}

		if err = tx.Commit(); err != nil {
			dbErr = err
			continue
		}

		// Success
		dbErr = nil
		break
	}

	if dbErr != nil {
		debugLogQueue("Error updating job #%d after retries: %v", job.ID, dbErr)
	}

	debugLogQueue("Job #%d completed with status: %s", job.ID, status)

	return status, actualHash, errorMessage
}

// rebuildFOD runs the rebuild-fod implementation for a derivation
func (q *RebuildQueue) rebuildFOD(drvPath string) (string, error) {
	startTime := time.Now()
	debugLogQueue("Rebuilding FOD: %s", drvPath)

	// Set a timeout to prevent hanging - shorter timeout for testing
	timeoutSeconds := 300 // 5 minutes
	if q.delay <= 0 {
		timeoutSeconds = 30 // 30 seconds in testing mode
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	// Show rebuild message on first FOD
	if !q.hasShownRebuildMessage {
		q.hasShownRebuildMessage = true
		debugLogQueue("INFO: Rebuilding FODs for JSON Lines output")
	}

	// Use the fod package's RebuildFOD function directly
	// This ensures we're using the same code as the rebuild-fod command
	result, err := fod.RebuildFOD(ctx, drvPath)

	// Create an output buffer for compatibility with the old approach
	var outputBuf bytes.Buffer

	// Add the result to the output buffer
	if result != nil {
		fmt.Fprintf(&outputBuf, "%s\n", result.Log)

		if result.Status == "success" {
			fmt.Fprintf(&outputBuf, "STATUS=success\n")
			fmt.Fprintf(&outputBuf, "ACTUAL_HASH=%s\n", result.ActualHash)
			fmt.Fprintf(&outputBuf, "ERROR_MESSAGE=\n")
		} else if result.Status == "timeout" {
			fmt.Fprintf(&outputBuf, "STATUS=timeout\n")
			fmt.Fprintf(&outputBuf, "ACTUAL_HASH=\n")
			fmt.Fprintf(&outputBuf, "ERROR_MESSAGE=%s\n", result.ErrorMessage)
		} else {
			fmt.Fprintf(&outputBuf, "STATUS=%s\n", result.Status)
			fmt.Fprintf(&outputBuf, "ACTUAL_HASH=%s\n", result.ActualHash)
			fmt.Fprintf(&outputBuf, "ERROR_MESSAGE=%s\n", result.ErrorMessage)
		}
	} else if err != nil {
		// Handle error case when result is nil
		fmt.Fprintf(&outputBuf, "Rebuild failed: %v\n", err)
		fmt.Fprintf(&outputBuf, "STATUS=failure\n")
		fmt.Fprintf(&outputBuf, "ACTUAL_HASH=\n")
		fmt.Fprintf(&outputBuf, "ERROR_MESSAGE=%v\n", err)
	}

	totalDuration := time.Since(startTime)
	if totalDuration > 1*time.Second {
		debugLogQueue("Rebuild took %v to complete", totalDuration)
	}

	outputStr := outputBuf.String()

	// Get status for the return values
	status := "failure"

	if result != nil {
		status = result.Status
	}

	// If status is not success, return an error
	if status != "success" {
		errorMsg := "rebuild failed"
		if result != nil && result.ErrorMessage != "" {
			errorMsg = result.ErrorMessage
		}
		return outputStr, fmt.Errorf("rebuild failed with status %s: %s", status, errorMsg)
	}

	return outputStr, nil
}

// extractHashFromOutput tries to extract the actual hash from the rebuild output
func extractHashFromOutput(output string, expectedHashFormat string) string {
	// Look for the hex hash summary in the output
	lines := strings.Split(output, "\n")

	// First look for the BEST HASH section from the updated script format
	bestHashFound := false
	for i, line := range lines {
		// Look for the BEST HASH indicator line
		if strings.Contains(line, "🔑 BEST HASH") {
			// The hash should be on the next line
			if i+1 < len(lines) {
				bestHash := strings.TrimSpace(lines[i+1])
				if len(bestHash) >= 32 && isHexString(bestHash) {
					return bestHash
				}
			}
			bestHashFound = true
			break
		}
	}

	// If we didn't find a BEST HASH section, try the individual method lines
	if !bestHashFound {
		// First look for the Method 3 (Computed) hash, which is the most reliable
		for _, line := range lines {
			if strings.Contains(line, "Method 3 (Computed)") {
				parts := strings.Split(line, ":")
				if len(parts) > 1 {
					return strings.TrimSpace(parts[1])
				}
			}
		}

		// Look for Method 2 (Query) hash as fallback
		for _, line := range lines {
			if strings.Contains(line, "Method 2 (Query)") {
				parts := strings.Split(line, ":")
				if len(parts) > 1 {
					return strings.TrimSpace(parts[1])
				}
			}
		}

		// Look for Method 1 (JSON) hash as another fallback
		for _, line := range lines {
			if strings.Contains(line, "Method 1 (JSON)") {
				parts := strings.Split(line, ":")
				if len(parts) > 1 {
					return strings.TrimSpace(parts[1])
				}
			}
		}

		// Try Method 4 options
		for _, line := range lines {
			if strings.Contains(line, "Method 4 (Build Computed)") {
				parts := strings.Split(line, ":")
				if len(parts) > 1 {
					return strings.TrimSpace(parts[1])
				}
			}
		}
	}

	// Try any hash line in the output as a last resort
	for _, line := range lines {
		// Check for any line with a colon that might contain a hash
		if strings.Contains(line, ":") {
			parts := strings.Split(line, ":")
			if len(parts) > 1 {
				hashCandidate := strings.TrimSpace(parts[1])
				// Only return if it looks like a hex hash (at least 32 hex chars)
				if len(hashCandidate) >= 32 && isHexString(hashCandidate) {
					return hashCandidate
				}
			}
		}
	}

	return ""
}

// isHexString checks if a string contains only hexadecimal characters
func isHexString(s string) bool {
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

// ForceQueueAllFODs adds all FODs for a revision to the rebuild queue, even if they were already queued before
// This is particularly useful for non-SQLite output formats with in-memory databases
func (q *RebuildQueue) ForceQueueAllFODs(revisionID int64) (int, error) {
	// Clear any existing queue entries for this revision
	var err error
	_, err = q.db.Exec(`DELETE FROM rebuild_queue WHERE revision_id = ?`, revisionID)
	if err != nil {
		return 0, fmt.Errorf("failed to clear existing queue entries: %w", err)
	}

	// Add all FODs to the queue
	var result sql.Result
	if config.IsNixExpr {
		// For Nix expressions, force queue all FODs
		debugLogQueue("Force queuing all FODs for expression")
		result, err = q.db.Exec(`
			INSERT INTO rebuild_queue (drv_path, revision_id, expected_hash, status)
			SELECT f.drv_path, ?, f.hash, ?
			FROM fods f
		`, revisionID, StatusPending)
	} else {
		// For regular revisions, force queue FODs associated with this revision
		result, err = q.db.Exec(`
			INSERT INTO rebuild_queue (drv_path, revision_id, expected_hash, status)
			SELECT dr.drv_path, dr.revision_id, f.hash, ?
			FROM drv_revisions dr
			JOIN fods f ON dr.drv_path = f.drv_path
			WHERE dr.revision_id = ?
		`, StatusPending, revisionID)
	}
	if err != nil {
		return 0, fmt.Errorf("failed to force queue FODs: %w", err)
	}

	// Get the number of rows inserted
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("failed to get rows affected: %w", err)
	}

	debugLogQueue("Force queued %d FODs for rebuild for revision ID %d", rowsAffected, revisionID)
	return int(rowsAffected), nil
}

// GetQueueStats returns statistics about the rebuild queue
func (q *RebuildQueue) GetQueueStats(revisionID int64) (map[string]int, error) {
	stats := map[string]int{
		"total":         0,
		"pending":       0,
		"running":       0,
		"success":       0,
		"failure":       0,
		"timeout":       0,
		"hash_mismatch": 0,
	}

	rows, err := q.db.Query(`
		SELECT status, COUNT(*) 
		FROM rebuild_queue 
		WHERE revision_id = ?
		GROUP BY status
	`, revisionID)
	if err != nil {
		return nil, fmt.Errorf("failed to get queue stats: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}
		stats[status] = count
		stats["total"] += count
	}

	return stats, nil
}
