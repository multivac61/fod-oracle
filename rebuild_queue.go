package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	
	"github.com/multivac61/fod-oracle/pkg/fod"
)

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
	db        *sql.DB
	buildChan chan RebuildJob
	delay     time.Duration
	lastEnd   time.Time
	wg        *sync.WaitGroup
	stopped   bool
	running   bool
	mutex     sync.Mutex
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
	// First check if the rebuild_queue table exists
	var tableName string
	err := q.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='rebuild_queue'`).Scan(&tableName)
	if err != nil {
		if err == sql.ErrNoRows {
			// Table doesn't exist, create it
			log.Printf("Creating rebuild_queue table...")
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
					FOREIGN KEY (revision_id) REFERENCES revisions(id) ON DELETE CASCADE
				);
				CREATE INDEX IF NOT EXISTS idx_queue_status ON rebuild_queue(status);
				CREATE INDEX IF NOT EXISTS idx_queue_drv_path ON rebuild_queue(drv_path);
				CREATE INDEX IF NOT EXISTS idx_queue_revision_id ON rebuild_queue(revision_id);
			`)
			if err != nil {
				return 0, fmt.Errorf("failed to create rebuild_queue table: %w", err)
			}
		} else {
			return 0, fmt.Errorf("failed to check if rebuild_queue table exists: %w", err)
		}
	}

	// First count how many FODs we have for this revision
	var totalCount int
	err = q.db.QueryRow(`
		SELECT COUNT(*) FROM drv_revisions 
		WHERE revision_id = ?
	`, revisionID).Scan(&totalCount)
	if err != nil {
		return 0, fmt.Errorf("failed to count total FODs: %w", err)
	}

	if totalCount == 0 {
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
			log.Printf("Reset %d failed FODs to pending for revision ID %d", resetCount, revisionID)
			return resetCount, nil
		}

		return 0, nil
	}

	// Insert all FODs for this revision into the queue that aren't already there
	result, err := q.db.Exec(`
		INSERT INTO rebuild_queue (drv_path, revision_id, expected_hash, status)
		SELECT dr.drv_path, dr.revision_id, f.hash, ?
		FROM drv_revisions dr
		JOIN fods f ON dr.drv_path = f.drv_path
		WHERE dr.revision_id = ?
		AND dr.drv_path NOT IN (SELECT drv_path FROM rebuild_queue WHERE revision_id = ?)
	`, StatusPending, revisionID, revisionID)
	if err != nil {
		return 0, fmt.Errorf("failed to queue FODs: %w", err)
	}

	// Get the number of rows inserted
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("failed to get rows affected: %w", err)
	}

	log.Printf("Queued %d FODs for rebuild for revision ID %d", rowsAffected, revisionID)
	return int(rowsAffected), nil
}

// Start starts the rebuild queue runner
func (q *RebuildQueue) Start(concurrency int) {
	q.mutex.Lock()
	if q.stopped {
		q.stopped = false
	}
	q.running = true
	q.mutex.Unlock()

	// Start worker goroutines
	for i := 0; i < concurrency; i++ {
		q.wg.Add(1)
		go func(workerID int) {
			defer q.wg.Done()
			log.Printf("Starting rebuild worker %d", workerID)
			for job := range q.buildChan {
				q.processJob(job)
			}
		}(i)
	}

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
				log.Printf("Job fetcher: stopping due to stop signal")
				break
			}

			// Get the next job
			job, err := q.fetchNextJob()
			if err != nil {
				if err != sql.ErrNoRows {
					log.Printf("Error fetching next job: %v", err)
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
						log.Printf("Error counting pending jobs: %v", err)
					} else if pending == 0 {
						log.Printf("Job fetcher: no more pending jobs, exiting")
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
						log.Printf("Error counting pending jobs: %v", err)
					} else if pending == 0 {
						log.Printf("Job fetcher: no more pending jobs, exiting")
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
	var count int
	err := q.db.QueryRow(`SELECT COUNT(*) FROM rebuild_queue WHERE status = ?`, StatusPending).Scan(&count)
	return count, err
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
		log.Printf("Warning: Timed out waiting for rebuild workers to complete")
	}
}

// Wait waits for all jobs to be processed
func (q *RebuildQueue) Wait() {
	q.wg.Wait()
}

// fetchNextJob gets the next job from the queue
func (q *RebuildQueue) fetchNextJob() (*RebuildJob, error) {
	// Add retries for database transactions to handle contention
	var job *RebuildJob
	var err error

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

		// Only retry on database locks or busy errors
		if !strings.Contains(err.Error(), "database is locked") &&
			!strings.Contains(err.Error(), "database is busy") {
			return nil, err
		}

		log.Printf("Database locked, retrying fetch (attempt %d/5)", attempt+1)
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
func (q *RebuildQueue) processJob(job RebuildJob) {
	log.Printf("Processing job #%d: %s", job.ID, job.DrvPath)

	// Run the rebuild-fod command
	result, err := q.rebuildFOD(job.DrvPath)
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
		if strings.Contains(err.Error(), "signal: killed") || strings.Contains(err.Error(), "context deadline exceeded") {
			status = StatusTimeout
			errorMessage = "Build timed out"
		} else {
			status = StatusFailure
			errorMessage = err.Error()

			// If command not found error for rebuild-fod, add more details
			if strings.Contains(err.Error(), "executable file not found") {
				errorMessage = "rebuild-fod command not found. Try installing it with 'nix build .#rebuild-fod'"
			}
		}
	} else {
		// Check if the output contains a failure or hash mismatch message
		if strings.Contains(result, "Hash mismatch detected") {
			status = StatusHashMismatch
			errorMessage = "Hash mismatch detected during build"
		}

		// Extract the actual hash from the result
		actualHash = extractHashFromOutput(result, job.ExpectedHash)

		// Check if the hash matches the expected hash
		if actualHash != "" && actualHash != job.ExpectedHash {
			status = StatusHashMismatch
			errorMessage = fmt.Sprintf("Hash mismatch: expected %s, got %s", job.ExpectedHash, actualHash)
		}

		// If we couldn't extract a hash but the command succeeded, that might still be valid
		// Check if the output contains any hash info first
		if actualHash == "" {
			if strings.Contains(result, "hex hash") ||
				strings.Contains(result, "SHA") ||
				strings.Contains(result, "SUMMARY OF HEX HASHES") {
				status = StatusFailure
				errorMessage = "Could not parse hash from rebuild output, but hash information was present"
			} else if strings.Contains(result, "No hex hash could be determined") {
				status = StatusFailure
				errorMessage = "No hash could be determined through any method"
			}
		}
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

		tx, err := q.db.Begin()
		if err != nil {
			log.Printf("Error beginning transaction for job #%d: %v", job.ID, err)
			continue
		}

		// Set a busy timeout
		_, err = tx.Exec("PRAGMA busy_timeout = 5000")
		if err != nil {
			tx.Rollback()
			log.Printf("Error setting busy timeout for job #%d: %v", job.ID, err)
			continue
		}

		_, err = tx.Exec(`
			UPDATE rebuild_queue
			SET status = ?, finished_at = ?, actual_hash = ?, log = ?, error_message = ?
			WHERE id = ?
		`, status, now, actualHash, result, errorMessage, job.ID)
		if err != nil {
			tx.Rollback()
			if strings.Contains(err.Error(), "database is locked") ||
				strings.Contains(err.Error(), "database is busy") {
				log.Printf("Database locked, retrying update for job #%d (attempt %d/5)", job.ID, attempt+1)
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
		log.Printf("Error updating job #%d after retries: %v", job.ID, dbErr)
	}

	log.Printf("Job #%d completed with status: %s", job.ID, status)
}

// rebuildFOD runs the rebuild-fod implementation for a derivation
func (q *RebuildQueue) rebuildFOD(drvPath string) (string, error) {
	startTime := time.Now()
	log.Printf("Rebuilding FOD: %s", drvPath)

	// Set a timeout to prevent hanging - shorter timeout for testing
	timeout := 5 * time.Minute
	if q.delay <= 0 {
		timeout = 30 * time.Second // Shorter timeout for testing
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Use our Go implementation directly
	rebuildResult, err := fod.RebuildFOD(ctx, drvPath)
	if err != nil {
		return "", fmt.Errorf("rebuild failed: %w", err)
	}

	totalDuration := time.Since(startTime)
	if totalDuration > 1*time.Second {
		log.Printf("Rebuild took %v to complete", totalDuration)
	}

	if rebuildResult.Status != "success" {
		if rebuildResult.HashMismatch {
			return rebuildResult.Log, fmt.Errorf("hash mismatch: expected %s, got %s", 
				rebuildResult.ExpectedHash, rebuildResult.ActualHash)
		}
		return rebuildResult.Log, fmt.Errorf("rebuild failed with status: %s", rebuildResult.Status)
	}

	return rebuildResult.Log, nil
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
