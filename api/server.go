package api

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	_ "github.com/mattn/go-sqlite3"
)

// FOD represents a fixed-output derivation
type FOD struct {
	DrvPath       string `json:"drvPath"`
	OutputPath    string `json:"outputPath"`
	HashAlgorithm string `json:"hashAlgorithm"`
	Hash          string `json:"hash"`
}

// RevisionInfo represents information about a nixpkgs revision
type RevisionInfo struct {
	ID        int64     `json:"id"`
	Rev       string    `json:"rev"`
	Timestamp time.Time `json:"timestamp"`
	FODCount  int       `json:"fodCount"`
}

// Server represents the API server
type Server struct {
	router chi.Router
	db     *sql.DB
}

// NewServer creates a new API server
func NewServer(db *sql.DB) *Server {
	s := &Server{
		router: chi.NewRouter(),
		db:     db,
	}

	// Setup middleware
	s.router.Use(middleware.Logger)
	s.router.Use(middleware.Recoverer)
	s.router.Use(middleware.RequestID)
	s.router.Use(middleware.RealIP)
	s.router.Use(middleware.Timeout(60 * time.Second))

	// Setup CORS
	s.router.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	// Register routes
	s.routes()

	return s
}

// routes sets up the API routes
func (s *Server) routes() {
	s.router.Get("/api/health", s.handleHealth)
	s.router.Get("/api/revisions", s.handleGetRevisions)
	s.router.Get("/api/revisions/{id}", s.handleGetRevision)
	s.router.Get("/api/revision/{rev}", s.handleGetRevisionByHash)
	s.router.Get("/api/fods", s.handleGetFODs)
	s.router.Get("/api/fods/{hash}", s.handleGetFODByHash)
	s.router.Get("/api/stats", s.handleGetStats)
	s.router.Get("/api/compare", s.handleCompareRevisions)
}

// ServeHTTP implements the http.Handler interface
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

// handleHealth returns a health check response
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleGetRevisions returns all nixpkgs revisions
func (s *Server) handleGetRevisions(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(`
		SELECT r.id, r.rev, r.timestamp, COUNT(dr.drv_path) as fod_count
		FROM revisions r
		LEFT JOIN drv_revisions dr ON r.id = dr.revision_id
		GROUP BY r.id
		ORDER BY r.timestamp DESC
	`)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	var revisions []RevisionInfo
	for rows.Next() {
		var rev RevisionInfo
		if err := rows.Scan(&rev.ID, &rev.Rev, &rev.Timestamp, &rev.FODCount); err != nil {
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
		revisions = append(revisions, rev)
	}

	if err := rows.Err(); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	respondJSON(w, http.StatusOK, revisions)
}

// handleGetRevision returns details about a specific revision
func (s *Server) handleGetRevision(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid revision ID")
		return
	}

	var rev RevisionInfo
	err = s.db.QueryRow(`
		SELECT r.id, r.rev, r.timestamp, COUNT(dr.drv_path) as fod_count
		FROM revisions r
		LEFT JOIN drv_revisions dr ON r.id = dr.revision_id
		WHERE r.id = ?
		GROUP BY r.id
	`, id).Scan(&rev.ID, &rev.Rev, &rev.Timestamp, &rev.FODCount)
	if err != nil {
		if err == sql.ErrNoRows {
			respondError(w, http.StatusNotFound, "Revision not found")
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	respondJSON(w, http.StatusOK, rev)
}

// handleGetFODs returns FODs with pagination
func (s *Server) handleGetFODs(w http.ResponseWriter, r *http.Request) {
	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")
	revIDStr := r.URL.Query().Get("revision_id")

	limit := 100
	if limitStr != "" {
		parsedLimit, err := strconv.Atoi(limitStr)
		if err == nil && parsedLimit > 0 && parsedLimit <= 1000 {
			limit = parsedLimit
		}
	}

	offset := 0
	if offsetStr != "" {
		parsedOffset, err := strconv.Atoi(offsetStr)
		if err == nil && parsedOffset >= 0 {
			offset = parsedOffset
		}
	}

	var args []interface{}
	query := `
		SELECT f.drv_path, f.output_path, f.hash_algorithm, f.hash
		FROM fods f
	`

	if revIDStr != "" {
		revID, err := strconv.ParseInt(revIDStr, 10, 64)
		if err == nil {
			query += `
				JOIN drv_revisions dr ON f.drv_path = dr.drv_path
				WHERE dr.revision_id = ?
			`
			args = append(args, revID)
		}
	}

	query += `
		ORDER BY f.drv_path
		LIMIT ? OFFSET ?
	`
	args = append(args, limit, offset)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	var fods []FOD
	for rows.Next() {
		var fod FOD
		if err := rows.Scan(&fod.DrvPath, &fod.OutputPath, &fod.HashAlgorithm, &fod.Hash); err != nil {
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
		fods = append(fods, fod)
	}

	if err := rows.Err(); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	respondJSON(w, http.StatusOK, fods)
}

// handleGetFODByHash returns FODs with a specific hash
func (s *Server) handleGetFODByHash(w http.ResponseWriter, r *http.Request) {
	hash := chi.URLParam(r, "hash")

	rows, err := s.db.Query(`
		SELECT drv_path, output_path, hash_algorithm, hash
		FROM fods
		WHERE hash = ?
	`, hash)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	var fods []FOD
	for rows.Next() {
		var fod FOD
		if err := rows.Scan(&fod.DrvPath, &fod.OutputPath, &fod.HashAlgorithm, &fod.Hash); err != nil {
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
		fods = append(fods, fod)
	}

	if err := rows.Err(); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if len(fods) == 0 {
		respondError(w, http.StatusNotFound, "No FODs found with the specified hash")
		return
	}

	respondJSON(w, http.StatusOK, fods)
}

// Stats represents database statistics
type Stats struct {
	TotalFODs         int       `json:"totalFODs"`
	TotalRevisions    int       `json:"totalRevisions"`
	LastUpdated       time.Time `json:"lastUpdated"`
	UniqueHashes      int       `json:"uniqueHashes"`
	DatabaseSizeBytes int64     `json:"databaseSizeBytes"`
}

// handleGetStats returns database statistics
func (s *Server) handleGetStats(w http.ResponseWriter, r *http.Request) {
	var stats Stats

	// Get total FODs
	err := s.db.QueryRow("SELECT COUNT(*) FROM fods").Scan(&stats.TotalFODs)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Get total revisions
	err = s.db.QueryRow("SELECT COUNT(*) FROM revisions").Scan(&stats.TotalRevisions)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Get last updated timestamp
	err = s.db.QueryRow("SELECT MAX(timestamp) FROM revisions").Scan(&stats.LastUpdated)
	if err != nil && err != sql.ErrNoRows {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Get unique hashes
	err = s.db.QueryRow("SELECT COUNT(DISTINCT hash) FROM fods").Scan(&stats.UniqueHashes)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Get database size (approximate)
	err = s.db.QueryRow("SELECT page_count * page_size FROM pragma_page_count(), pragma_page_size()").Scan(&stats.DatabaseSizeBytes)
	if err != nil {
		log.Printf("Failed to get database size: %v", err)
		// Continue without database size
	}

	respondJSON(w, http.StatusOK, stats)
}

// ComparisonResult represents the result of comparing two revisions
type ComparisonResult struct {
	Rev1          string `json:"rev1"`
	Rev2          string `json:"rev2"`
	CommonFODs    int    `json:"commonFODs"`
	OnlyInRev1    int    `json:"onlyInRev1"`
	OnlyInRev2    int    `json:"onlyInRev2"`
	HashChanges   int    `json:"hashChanges"`
	TotalInRev1   int    `json:"totalInRev1"`
	TotalInRev2   int    `json:"totalInRev2"`
	FODsWithDiffs []FODDiff `json:"fodsWithDiffs,omitempty"`
}

// FODDiff represents a FOD that differs between revisions
type FODDiff struct {
	DrvPath string `json:"drvPath"`
	Hash1   string `json:"hash1"`
	Hash2   string `json:"hash2"`
}

// handleCompareRevisions compares two revisions
func (s *Server) handleCompareRevisions(w http.ResponseWriter, r *http.Request) {
	rev1 := r.URL.Query().Get("rev1")
	rev2 := r.URL.Query().Get("rev2")
	includeDetails := r.URL.Query().Get("details") == "true"

	if rev1 == "" || rev2 == "" {
		respondError(w, http.StatusBadRequest, "Both rev1 and rev2 query parameters are required")
		return
	}

	// Get revision IDs
	var rev1ID, rev2ID int64
	err := s.db.QueryRow("SELECT id FROM revisions WHERE rev = ?", rev1).Scan(&rev1ID)
	if err != nil {
		if err == sql.ErrNoRows {
			respondError(w, http.StatusNotFound, "Revision 1 not found")
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	err = s.db.QueryRow("SELECT id FROM revisions WHERE rev = ?", rev2).Scan(&rev2ID)
	if err != nil {
		if err == sql.ErrNoRows {
			respondError(w, http.StatusNotFound, "Revision 2 not found")
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	// Get total FODs in each revision
	var totalInRev1, totalInRev2 int
	err = s.db.QueryRow("SELECT COUNT(*) FROM drv_revisions WHERE revision_id = ?", rev1ID).Scan(&totalInRev1)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	err = s.db.QueryRow("SELECT COUNT(*) FROM drv_revisions WHERE revision_id = ?", rev2ID).Scan(&totalInRev2)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Count common FODs (same drv_path in both revisions)
	var commonFODs int
	err = s.db.QueryRow(`
		SELECT COUNT(*)
		FROM drv_revisions dr1
		JOIN drv_revisions dr2 ON dr1.drv_path = dr2.drv_path
		WHERE dr1.revision_id = ? AND dr2.revision_id = ?
	`, rev1ID, rev2ID).Scan(&commonFODs)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Count FODs only in rev1
	var onlyInRev1 int
	err = s.db.QueryRow(`
		SELECT COUNT(*)
		FROM drv_revisions dr1
		LEFT JOIN drv_revisions dr2 ON dr1.drv_path = dr2.drv_path AND dr2.revision_id = ?
		WHERE dr1.revision_id = ? AND dr2.drv_path IS NULL
	`, rev2ID, rev1ID).Scan(&onlyInRev1)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Count FODs only in rev2
	var onlyInRev2 int
	err = s.db.QueryRow(`
		SELECT COUNT(*)
		FROM drv_revisions dr2
		LEFT JOIN drv_revisions dr1 ON dr2.drv_path = dr1.drv_path AND dr1.revision_id = ?
		WHERE dr2.revision_id = ? AND dr1.drv_path IS NULL
	`, rev1ID, rev2ID).Scan(&onlyInRev2)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Count FODs with hash changes
	var hashChanges int
	query := `
		SELECT COUNT(*)
		FROM drv_revisions dr1
		JOIN drv_revisions dr2 ON dr1.drv_path = dr2.drv_path
		JOIN fods f1 ON dr1.drv_path = f1.drv_path
		JOIN fods f2 ON dr2.drv_path = f2.drv_path
		WHERE dr1.revision_id = ? AND dr2.revision_id = ? AND f1.hash != f2.hash
	`
	err = s.db.QueryRow(query, rev1ID, rev2ID).Scan(&hashChanges)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	result := ComparisonResult{
		Rev1:        rev1,
		Rev2:        rev2,
		CommonFODs:  commonFODs,
		OnlyInRev1:  onlyInRev1,
		OnlyInRev2:  onlyInRev2,
		HashChanges: hashChanges,
		TotalInRev1: totalInRev1,
		TotalInRev2: totalInRev2,
	}

	// Get details of FODs with hash changes if requested
	if includeDetails && hashChanges > 0 {
		detailsQuery := `
			SELECT dr1.drv_path, f1.hash, f2.hash
			FROM drv_revisions dr1
			JOIN drv_revisions dr2 ON dr1.drv_path = dr2.drv_path
			JOIN fods f1 ON dr1.drv_path = f1.drv_path
			JOIN fods f2 ON dr2.drv_path = f2.drv_path
			WHERE dr1.revision_id = ? AND dr2.revision_id = ? AND f1.hash != f2.hash
			LIMIT 1000
		`
		detailsRows, err := s.db.Query(detailsQuery, rev1ID, rev2ID)
		if err != nil {
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer detailsRows.Close()

		for detailsRows.Next() {
			var diff FODDiff
			if err := detailsRows.Scan(&diff.DrvPath, &diff.Hash1, &diff.Hash2); err != nil {
				respondError(w, http.StatusInternalServerError, err.Error())
				return
			}
			result.FODsWithDiffs = append(result.FODsWithDiffs, diff)
		}

		if err := detailsRows.Err(); err != nil {
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	respondJSON(w, http.StatusOK, result)
}

// respondJSON sends a JSON response
func respondJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("Error encoding JSON response: %v", err)
	}
}

// respondError sends an error response
func respondError(w http.ResponseWriter, status int, message string) {
	respondJSON(w, status, map[string]string{"error": message})
}

// handleGetRevisionByHash returns details about a specific revision by its git hash
func (s *Server) handleGetRevisionByHash(w http.ResponseWriter, r *http.Request) {
	revHash := chi.URLParam(r, "rev")
	if revHash == "" {
		respondError(w, http.StatusBadRequest, "Revision hash is required")
		return
	}

	var rev RevisionInfo
	err := s.db.QueryRow(`
		SELECT r.id, r.rev, r.timestamp, COUNT(dr.drv_path) as fod_count
		FROM revisions r
		LEFT JOIN drv_revisions dr ON r.id = dr.revision_id
		WHERE r.rev = ?
		GROUP BY r.id
	`, revHash).Scan(&rev.ID, &rev.Rev, &rev.Timestamp, &rev.FODCount)
	if err != nil {
		if err == sql.ErrNoRows {
			respondError(w, http.StatusNotFound, "Revision not found")
		} else {
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	respondJSON(w, http.StatusOK, rev)
}
