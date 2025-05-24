package main

import (
	"database/sql"
	"fmt"
)

// FODWithRebuild extends FOD with rebuild information
type FODWithRebuild struct {
	FOD
	RevisionID    int64  `json:"revision_id,omitempty"`
	RebuildStatus string `json:"rebuild_status,omitempty"`
	ActualHash    string `json:"actual_hash,omitempty"`
	HashMismatch  bool   `json:"hash_mismatch,omitempty"`
	ErrorMessage  string `json:"error_message,omitempty"`
}

// Writer is an interface for writing FOD data
type Writer interface {
	AddFOD(fod FOD)
	IncrementDrvCount()
	Flush()
	Close() error
	// Include methods for reevaluation integration
	AddRebuildInfo(drvPath string, status, actualHash, errorMessage string)
}

// GetWriter returns a database writer
// In the new simplified architecture, we always use the DBBatcher for all output formats
func GetWriter(db *sql.DB, revisionID int64, rev string) (Writer, error) {
	// Always use DBBatcher - conversion to other formats happens at the end
	batcher, err := NewDBBatcher(db, 1000, revisionID)
	if err != nil {
		return nil, fmt.Errorf("failed to create database batcher: %w", err)
	}
	return batcher, nil
}
