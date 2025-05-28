package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/hook"
)

// startWebInterface starts the PocketBase web interface
func startWebInterface(port int, host string) error {
	debugLog("Starting PocketBase web interface on %s:%d", host, port)

	app := pocketbase.New()

	// Set up data directory
	dataDir := "./pb_data"
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}

	// Setup collections and routes after bootstrap
	app.OnBootstrap().Bind(&hook.Handler[*core.BootstrapEvent]{
		Func: func(e *core.BootstrapEvent) error {
			if err := e.Next(); err != nil {
				return err
			}
			return setupFODCollections(e.App)
		},
	})

	// Setup routes exactly like the starter project
	app.OnServe().BindFunc(func(e *core.ServeEvent) error {
		// Debug the working directory
		cwd, _ := os.Getwd()
		debugLog("Current working directory: %s", cwd)
		
		// Try relative path from current directory
		buildPath := "web-frontend/build"
		debugLog("Using relative build path: %s", buildPath)
		
		// Check if build directory exists
		if _, err := os.Stat(buildPath); os.IsNotExist(err) {
			debugLog("Build directory does not exist: %s", buildPath)
			return e.Next()
		}
		
		debugLog("Build directory exists, setting up static file server with os.DirFS")

		// Custom static file handler with proper PocketBase wildcard syntax
		e.Router.GET("/{path...}", func(re *core.RequestEvent) error {
			path := re.Request.URL.Path
			debugLog("Route handler called for path: %s", path)
			
			// Skip API and admin routes
			if strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/_/") {
				return re.Next()
			}
			
			// Serve static assets
			if strings.HasPrefix(path, "/_app/") || path == "/favicon.png" {
				fullBuildPath := filepath.Join(cwd, buildPath)
				filePath := filepath.Join(fullBuildPath, path)
				if _, err := os.Stat(filePath); err == nil {
					// Set proper MIME types
					ext := filepath.Ext(path)
					switch ext {
					case ".js":
						re.Response.Header().Set("Content-Type", "application/javascript; charset=utf-8")
					case ".css":
						re.Response.Header().Set("Content-Type", "text/css; charset=utf-8")
					case ".png":
						re.Response.Header().Set("Content-Type", "image/png")
					}
					
					if strings.Contains(path, "/immutable/") {
						re.Response.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
					}
					
					http.ServeFile(re.Response, re.Request, filePath)
					return nil
				}
			}
			
			// SPA fallback - serve index.html for all other routes
			fullBuildPath := filepath.Join(cwd, buildPath)
			indexPath := filepath.Join(fullBuildPath, "index.html")
			http.ServeFile(re.Response, re.Request, indexPath)
			return nil
		})

		// Add API routes AFTER (like the starter project)
		e.Router.GET("/api/fod-status", func(re *core.RequestEvent) error {
			return re.JSON(200, map[string]interface{}{
				"status":    "ok",
				"message":   "FOD Oracle API is running",
				"timestamp": time.Now().UTC(),
				"version":   "web-interface",
			})
		})

		return e.Next()
	})

	// Open browser if not in debug mode and host is localhost
	if !config.Debug && (host == "127.0.0.1" || host == "localhost") {
		go func() {
			time.Sleep(2 * time.Second) // Wait for PocketBase to start
			openBrowser(fmt.Sprintf("http://%s:%d", host, port))
		}()
	}

	log.Printf("PocketBase web interface starting at http://%s:%d", host, port)
	log.Printf("Admin interface will be available at http://%s:%d/_/", host, port)

	// Override os.Args to avoid conflicts with PocketBase argument parsing
	originalArgs := os.Args
	os.Args = []string{os.Args[0], "serve", "--http", fmt.Sprintf("%s:%d", host, port)}
	defer func() { os.Args = originalArgs }()

	// Start PocketBase
	return app.Start()
}

// startWebInterfaceWithRealtime starts web interface optimized for real-time FOD streaming during evaluation
func startWebInterfaceWithRealtime(port int, host string, sqliteDB *sql.DB) error {
	debugLog("Starting real-time FOD streaming web interface on %s:%d", host, port)

	app := pocketbase.New()

	// Set up data directory
	dataDir := "./pb_data"
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}

	// Setup collections and enable real-time streaming after bootstrap
	app.OnBootstrap().Bind(&hook.Handler[*core.BootstrapEvent]{
		Func: func(e *core.BootstrapEvent) error {
			if err := e.Next(); err != nil {
				return err
			}

			// Setup collections first
			if err := setupFODCollections(e.App); err != nil {
				debugLog("Error setting up collections: %v", err)
			}

			// Enable real-time streaming for evaluation
			enableRealtimeStreaming(e.App)

			return nil
		},
	})

	// Setup routes optimized for real-time viewing
	app.OnServe().Bind(&hook.Handler[*core.ServeEvent]{
		Func: func(e *core.ServeEvent) error {
			// Main route goes directly to real-time dashboard
			e.Router.GET("/", func(re *core.RequestEvent) error {
				return re.HTML(200, getRealtimeHTML())
			})

			// Real-time dashboard (same as main)
			e.Router.GET("/realtime", func(re *core.RequestEvent) error {
				return re.HTML(200, getRealtimeHTML())
			})

			// API status endpoint
			e.Router.GET("/api/fod-status", func(re *core.RequestEvent) error {
				return re.JSON(200, map[string]interface{}{
					"status":            "ok",
					"message":           "Real-time FOD streaming active",
					"timestamp":         time.Now().UTC(),
					"streaming_enabled": streamActive,
				})
			})

			return e.Next()
		},
	})

	// Don't open browser automatically during evaluation
	// User will access manually when ready

	log.Printf("🚀 Real-time FOD streaming interface starting at http://%s:%d", host, port)
	log.Printf("📊 Dashboard: http://%s:%d/realtime", host, port)
	log.Printf("🛠️ Admin: http://%s:%d/_/", host, port)

	// Override os.Args to avoid conflicts with PocketBase argument parsing
	originalArgs := os.Args
	os.Args = []string{os.Args[0], "serve", "--http", fmt.Sprintf("%s:%d", host, port)}
	defer func() { os.Args = originalArgs }()

	// Start PocketBase
	return app.Start()
}

// getRealtimeHTML returns the same Svelte build HTML as the main interface
func getRealtimeHTML() string {
	// Use the same Svelte build for consistency
	return getIndexHTML()
}

// Real-time streaming globals
var (
	realtimeApp  core.App
	realtimeMux  sync.RWMutex
	streamActive bool
)

// enableRealtimeStreaming sets up the app instance for real-time streaming
func enableRealtimeStreaming(app core.App) {
	realtimeMux.Lock()
	defer realtimeMux.Unlock()
	realtimeApp = app
	streamActive = true
	debugLog("Real-time streaming enabled")
}

// disableRealtimeStreaming disables real-time streaming
func disableRealtimeStreaming() {
	realtimeMux.Lock()
	defer realtimeMux.Unlock()
	streamActive = false
	realtimeApp = nil
	debugLog("Real-time streaming disabled")
}

// streamFODRealtime streams a single FOD to PocketBase in real-time
func streamFODRealtime(fod FOD, evaluationID string) error {
	realtimeMux.RLock()
	defer realtimeMux.RUnlock()

	if !streamActive || realtimeApp == nil {
		return nil // Streaming not active
	}

	// Find FODs collection
	collection, err := realtimeApp.FindCollectionByNameOrId("fods")
	if err != nil {
		debugLog("FODs collection not found for streaming: %v", err)
		return nil // Don't fail evaluation if streaming fails
	}

	// Create record
	record := core.NewRecord(collection)
	record.Set("drv_path", fod.DrvPath)
	record.Set("output_path", fod.OutputPath)
	record.Set("hash_algorithm", fod.HashAlgorithm)
	record.Set("expected_hash", fod.Hash)
	record.Set("rebuild_status", "discovered")
	record.Set("evaluation_id", evaluationID)
	record.Set("discovered_at", time.Now().UTC().Format(time.RFC3339))

	// Save to PocketBase (this triggers real-time subscriptions)
	if err := realtimeApp.Save(record); err != nil {
		debugLog("Failed to stream FOD %s: %v", fod.DrvPath, err)
		return nil // Don't fail evaluation
	}

	debugLog("Streamed FOD: %s", fod.DrvPath)
	return nil
}

// streamEvaluationProgress streams evaluation progress in real-time
func streamEvaluationProgress(evaluationID string, stats EvaluationStats) error {
	realtimeMux.RLock()
	defer realtimeMux.RUnlock()

	if !streamActive || realtimeApp == nil {
		return nil
	}

	// Find evaluations collection
	collection, err := realtimeApp.FindCollectionByNameOrId("evaluations")
	if err != nil {
		debugLog("Evaluations collection not found for progress streaming: %v", err)
		return nil
	}

	// Try to find existing evaluation record
	existing, err := realtimeApp.FindFirstRecordByFilter("evaluations", "evaluation_id = {:evalId}",
		map[string]any{"evalId": evaluationID})

	var record *core.Record
	if err == nil && existing != nil {
		// Update existing record
		record = existing
	} else {
		// Create new record
		record = core.NewRecord(collection)
		record.Set("evaluation_id", evaluationID)
		record.Set("start_time", time.Now().UTC().Format(time.RFC3339))
	}

	// Update progress fields
	record.Set("status", "running")
	record.Set("total_fods", stats.TotalFODs)
	record.Set("processed_fods", stats.ProcessedFODs)
	record.Set("failed_fods", stats.FailedFODs)
	record.Set("current_package", stats.CurrentPackage)
	record.Set("last_update", time.Now().UTC().Format(time.RFC3339))

	// Calculate progress percentage
	if stats.TotalFODs > 0 {
		progress := float64(stats.ProcessedFODs) / float64(stats.TotalFODs) * 100
		record.Set("progress_percent", progress)
	}

	// Save to trigger real-time updates
	if err := realtimeApp.Save(record); err != nil {
		debugLog("Failed to stream progress: %v", err)
	}

	return nil
}

// EvaluationStats represents real-time evaluation statistics
type EvaluationStats struct {
	TotalFODs      int
	ProcessedFODs  int
	FailedFODs     int
	CurrentPackage string
	StartTime      time.Time
}

// setupFODCollections sets up the PocketBase collections for FOD data
func setupFODCollections(app core.App) error {
	debugLog("Setting up FOD collections...")

	// Check if collections already exist
	fodCollection, _ := app.FindCollectionByNameOrId("fods")
	evaluationCollection, _ := app.FindCollectionByNameOrId("evaluations")

	if fodCollection == nil {
		debugLog("FODs collection not found - please create via admin interface")
		debugLog("Recommended FODs collection schema:")
		debugLog("  - drv_path (text, required): Derivation path")
		debugLog("  - output_path (text, required): Output path")
		debugLog("  - expected_hash (text): Expected hash")
		debugLog("  - actual_hash (text): Actual hash from rebuild")
		debugLog("  - rebuild_status (select): pending|success|failed")
		debugLog("  - hash_mismatch (bool): Whether hashes match")
		debugLog("  - evaluation_id (relation to evaluations): Link to evaluation run")
	}

	if evaluationCollection == nil {
		debugLog("Evaluations collection not found - please create via admin interface")
		debugLog("Recommended Evaluations collection schema:")
		debugLog("  - input (text, required): Nix expression input")
		debugLog("  - status (select): running|completed|failed")
		debugLog("  - start_time (datetime): When evaluation started")
		debugLog("  - end_time (datetime): When evaluation finished")
		debugLog("  - total_fods (number): Total FODs found")
		debugLog("  - failed_fods (number): Number of failed FODs")
	}

	debugLog("Collections setup complete - use admin interface at /_/ to create missing collections")
	return nil
}

// getIndexHTML returns the actual Svelte build HTML, reading from build directory or fallback
func getIndexHTML() string {
	// Try to read the actual built HTML from the filesystem
	cwd, err := os.Getwd()
	if err == nil {
		buildPath := filepath.Join(cwd, "web-frontend", "build", "index.html")
		if content, err := os.ReadFile(buildPath); err == nil {
			return string(content)
		}
	}

	// Fallback to embedded HTML if build not found
	return `<!doctype html>
<html lang="en">
	<head>
		<meta charset="utf-8" />
		<link rel="icon" href="/favicon.png" />
		<meta name="viewport" content="width=device-width, initial-scale=1" />
		<title>🔮 FOD Oracle - Real-time Dashboard</title>
		
		<link rel="modulepreload" href="/_app/immutable/entry/start.BEyWVUI3.js">
		<link rel="modulepreload" href="/_app/immutable/chunks/DA8hFdIb.js">
		<link rel="modulepreload" href="/_app/immutable/chunks/DDF6VVfN.js">
		<link rel="modulepreload" href="/_app/immutable/chunks/CB4osflO.js">
		<link rel="modulepreload" href="/_app/immutable/entry/app.BKHvZl57.js">
		<link rel="modulepreload" href="/_app/immutable/chunks/Lh3U4GXs.js">
		<link rel="modulepreload" href="/_app/immutable/chunks/DRuFr7Gh.js">
		<link rel="modulepreload" href="/_app/immutable/chunks/D-uDJOuw.js">
	</head>
	<body data-sveltekit-preload-data="hover" class="bg-fod-dark text-white">
		<div style="display: contents">
			<script>
				{
					__sveltekit_10yq3w8 = {
						base: ""
					};

					const element = document.currentScript.parentElement;

					Promise.all([
						import("/_app/immutable/entry/start.BEyWVUI3.js"),
						import("/_app/immutable/entry/app.BKHvZl57.js")
					]).then(([kit, app]) => {
						kit.start(app, element);
					});
				}
			</script>
		</div>
	</body>
</html>`
}

// handleSvelteRequest middleware to handle static files and SPA routing
func handleSvelteRequest(re *core.RequestEvent) error {
	path := re.Request.URL.Path

	// Skip API routes and PocketBase admin routes - let them be handled by PocketBase
	if strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/_/") {
		return re.Next()
	}

	// Get the current working directory to locate the frontend build
	cwd, err := os.Getwd()
	if err != nil {
		debugLog("Error getting current directory: %v", err)
		return re.Next()
	}

	// Path to the built Svelte app
	buildPath := filepath.Join(cwd, "web-frontend", "build")

	// Check if the build directory exists
	if _, err := os.Stat(buildPath); os.IsNotExist(err) {
		debugLog("Svelte build not found at %s", buildPath)
		return re.Next()
	}

	// Handle static assets
	if strings.HasPrefix(path, "/_app/") || path == "/favicon.png" {
		filePath := filepath.Join(buildPath, path)
		if _, err := os.Stat(filePath); err == nil {
			// Set proper MIME type based on file extension
			ext := filepath.Ext(filePath)
			switch ext {
			case ".js":
				re.Response.Header().Set("Content-Type", "application/javascript; charset=utf-8")
			case ".css":
				re.Response.Header().Set("Content-Type", "text/css; charset=utf-8")
			case ".json":
				re.Response.Header().Set("Content-Type", "application/json; charset=utf-8")
			case ".png":
				re.Response.Header().Set("Content-Type", "image/png")
			case ".svg":
				re.Response.Header().Set("Content-Type", "image/svg+xml")
			case ".woff", ".woff2":
				re.Response.Header().Set("Content-Type", "font/woff2")
			}

			// Add cache headers for immutable assets
			if strings.Contains(path, "/immutable/") {
				re.Response.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}

			http.ServeFile(re.Response, re.Request, filePath)
			return nil
		}
	}

	// For all other routes (/, /dashboard, etc.) serve the SPA index.html
	indexPath := filepath.Join(buildPath, "index.html")
	if _, err := os.Stat(indexPath); err == nil {
		http.ServeFile(re.Response, re.Request, indexPath)
		return nil
	}

	// Fallback to embedded HTML
	return re.HTML(200, getIndexHTML())
}

// setupSvelteRoutes configures routes to serve the Svelte frontend with proper SPA routing
func setupSvelteRoutes(e *core.ServeEvent) {
	// Get the current working directory to locate the frontend build
	cwd, err := os.Getwd()
	if err != nil {
		debugLog("Error getting current directory: %v", err)
		// Fallback to basic HTML for all routes
		e.Router.GET("/*", func(re *core.RequestEvent) error {
			return re.HTML(200, getIndexHTML())
		})
		return
	}

	// Path to the built Svelte app
	buildPath := filepath.Join(cwd, "web-frontend", "build")

	// Check if the build directory exists
	if _, err := os.Stat(buildPath); os.IsNotExist(err) {
		debugLog("Svelte build not found at %s, serving basic HTML", buildPath)
		// Fallback to basic HTML for all routes
		e.Router.GET("/*", func(re *core.RequestEvent) error {
			return re.HTML(200, getIndexHTML())
		})
		return
	}

	// Serve static assets with proper MIME types
	e.Router.GET("/_app/*", func(re *core.RequestEvent) error {
		filePath := filepath.Join(buildPath, re.Request.URL.Path)
		if _, err := os.Stat(filePath); err == nil {
			// Set proper MIME type based on file extension
			ext := filepath.Ext(filePath)
			switch ext {
			case ".js":
				re.Response.Header().Set("Content-Type", "application/javascript; charset=utf-8")
			case ".css":
				re.Response.Header().Set("Content-Type", "text/css; charset=utf-8")
			case ".json":
				re.Response.Header().Set("Content-Type", "application/json; charset=utf-8")
			case ".png":
				re.Response.Header().Set("Content-Type", "image/png")
			case ".svg":
				re.Response.Header().Set("Content-Type", "image/svg+xml")
			case ".woff", ".woff2":
				re.Response.Header().Set("Content-Type", "font/woff2")
			}

			// Add cache headers for immutable assets
			if strings.Contains(re.Request.URL.Path, "/immutable/") {
				re.Response.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}

			http.ServeFile(re.Response, re.Request, filePath)
			return nil
		}
		return re.NoContent(404)
	})

	// Serve favicon
	e.Router.GET("/favicon.png", func(re *core.RequestEvent) error {
		filePath := filepath.Join(buildPath, "favicon.png")
		if _, err := os.Stat(filePath); err == nil {
			http.ServeFile(re.Response, re.Request, filePath)
			return nil
		}
		return re.NoContent(404)
	})

	debugLog("Serving Svelte frontend from: %s", buildPath)
}

// startSvelteServer starts a standalone HTTP server for SvelteKit
func startSvelteServer(port int, host string) {
	cwd, err := os.Getwd()
	if err != nil {
		debugLog("Error getting current directory: %v", err)
		return
	}

	buildPath := filepath.Join(cwd, "web-frontend", "build")

	// Check if build exists
	if _, err := os.Stat(buildPath); os.IsNotExist(err) {
		debugLog("SvelteKit build not found at %s", buildPath)
		return
	}

	// Create file server
	fs := http.FileServer(http.Dir(buildPath))

	// Create mux
	mux := http.NewServeMux()

	// Handle all requests
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// For static assets, serve directly
		if strings.HasPrefix(r.URL.Path, "/_app/") || r.URL.Path == "/favicon.png" {
			// Set proper MIME types
			ext := filepath.Ext(r.URL.Path)
			switch ext {
			case ".js":
				w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
			case ".css":
				w.Header().Set("Content-Type", "text/css; charset=utf-8")
			case ".png":
				w.Header().Set("Content-Type", "image/png")
			}

			// Add cache headers for immutable assets
			if strings.Contains(r.URL.Path, "/immutable/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}

			fs.ServeHTTP(w, r)
			return
		}

		// For all other routes, serve index.html (SPA fallback)
		indexPath := filepath.Join(buildPath, "index.html")
		http.ServeFile(w, r, indexPath)
	})

	addr := fmt.Sprintf("%s:%d", host, port)
	log.Printf("🎨 SvelteKit frontend starting at http://%s", addr)

	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	if err := server.ListenAndServe(); err != nil {
		debugLog("SvelteKit server error: %v", err)
	}
}

// setupSPAFallback configures the SPA fallback route - must be called last
func setupSPAFallback(e *core.ServeEvent) {
	// Get the current working directory to locate the frontend build
	cwd, err := os.Getwd()
	if err != nil {
		debugLog("Error getting current directory for SPA fallback: %v", err)
		return
	}

	// Path to the built Svelte app
	buildPath := filepath.Join(cwd, "web-frontend", "build")

	// SPA fallback - serve index.html for all other routes (must be last)
	// This ensures that client-side routing works properly
	e.Router.GET("/*", func(re *core.RequestEvent) error {
		// Skip API routes and PocketBase admin routes - let them be handled by PocketBase
		if strings.HasPrefix(re.Request.URL.Path, "/api/") ||
			strings.HasPrefix(re.Request.URL.Path, "/_/") ||
			strings.HasPrefix(re.Request.URL.Path, "/_app/") {
			return re.Next()
		}

		// Serve index.html for all other routes (SPA fallback)
		indexPath := filepath.Join(buildPath, "index.html")
		if _, err := os.Stat(indexPath); err == nil {
			http.ServeFile(re.Response, re.Request, indexPath)
			return nil
		}
		return re.HTML(200, getIndexHTML())
	})

	debugLog("SPA fallback route configured")
}

// openBrowser opens the default browser to the given URL
func openBrowser(url string) {
	var err error
	switch runtime.GOOS {
	case "linux":
		err = exec.Command("xdg-open", url).Start()
	case "windows":
		err = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		err = exec.Command("open", url).Start()
	default:
		err = fmt.Errorf("unsupported platform")
	}

	if err != nil {
		log.Printf("Failed to open browser: %v", err)
		log.Printf("Please open your browser and go to: %s", url)
	}
}

// syncSQLiteDataToPocketBase syncs data from in-memory SQLite to PocketBase collections
func syncSQLiteDataToPocketBase(sqliteDB *sql.DB, pbApp core.App) error {
	debugLog("Starting data sync from SQLite to PocketBase...")

	// Check if collections exist
	fodsCollection, err := pbApp.FindCollectionByNameOrId("fods")
	if err != nil || fodsCollection == nil {
		debugLog("FODs collection not found - skipping FOD sync")
	} else {
		if err := syncFODsData(sqliteDB, pbApp); err != nil {
			debugLog("Error syncing FODs: %v", err)
		}
	}

	evaluationsCollection, err := pbApp.FindCollectionByNameOrId("evaluations")
	if err != nil || evaluationsCollection == nil {
		debugLog("Evaluations collection not found - skipping evaluation sync")
	} else {
		if err := syncEvaluationsData(sqliteDB, pbApp); err != nil {
			debugLog("Error syncing evaluations: %v", err)
		}
	}

	debugLog("Data sync completed")
	return nil
}

// syncFODsData syncs FODs from SQLite to PocketBase
func syncFODsData(sqliteDB *sql.DB, pbApp core.App) error {
	debugLog("Syncing FODs data...")

	// Query FODs with rebuild status from SQLite
	query := `
		SELECT 
			f.drv_path,
			f.output_path,
			f.hash_algorithm,
			f.hash as expected_hash,
			COALESCE(rq.status, 'pending') as rebuild_status,
			COALESCE(rq.actual_hash, '') as actual_hash,
			CASE 
				WHEN rq.actual_hash IS NOT NULL AND rq.actual_hash != f.hash THEN 1 
				ELSE 0 
			END as hash_mismatch,
			COALESCE(rq.error_message, '') as error_message,
			f.timestamp
		FROM fods f
		LEFT JOIN rebuild_queue rq ON f.drv_path = rq.drv_path
	`

	rows, err := sqliteDB.Query(query)
	if err != nil {
		return fmt.Errorf("failed to query FODs: %w", err)
	}
	defer rows.Close()

	collection, err := pbApp.FindCollectionByNameOrId("fods")
	if err != nil {
		return fmt.Errorf("failed to find fods collection: %w", err)
	}

	count := 0
	for rows.Next() {
		var drvPath, outputPath, hashAlgo, expectedHash, rebuildStatus, actualHash, errorMsg, timestamp string
		var hashMismatch int

		err := rows.Scan(&drvPath, &outputPath, &hashAlgo, &expectedHash,
			&rebuildStatus, &actualHash, &hashMismatch, &errorMsg, &timestamp)
		if err != nil {
			debugLog("Error scanning FOD row: %v", err)
			continue
		}

		// Create or update record in PocketBase
		record := core.NewRecord(collection)
		record.Set("drv_path", drvPath)
		record.Set("output_path", outputPath)
		record.Set("hash_algorithm", hashAlgo)
		record.Set("expected_hash", expectedHash)
		record.Set("rebuild_status", rebuildStatus)
		record.Set("actual_hash", actualHash)
		record.Set("hash_mismatch", hashMismatch == 1)
		record.Set("error_message", errorMsg)

		// Try to find existing record first
		existing, err := pbApp.FindFirstRecordByFilter("fods", "drv_path = {:drvPath}",
			map[string]any{"drvPath": drvPath})

		if err == nil && existing != nil {
			// Update existing record
			record.Id = existing.Id
			record.Set("created", existing.Get("created"))
			if err := pbApp.Save(record); err != nil {
				debugLog("Error updating FOD record %s: %v", drvPath, err)
			}
		} else {
			// Create new record
			if err := pbApp.Save(record); err != nil {
				debugLog("Error creating FOD record %s: %v", drvPath, err)
			}
		}
		count++
	}

	debugLog("Synced %d FOD records", count)
	return nil
}

// syncEvaluationsData syncs evaluation data from SQLite to PocketBase
func syncEvaluationsData(sqliteDB *sql.DB, pbApp core.App) error {
	debugLog("Syncing evaluations data...")

	// Query evaluations data from SQLite
	query := `
		SELECT 
			r.id,
			r.rev as input,
			CASE 
				WHEN rs.revision_id IS NOT NULL THEN 'completed'
				ELSE 'pending'
			END as status,
			rs.evaluation_timestamp as start_time,
			rs.evaluation_timestamp as end_time,
			COALESCE(rs.total_derivations_found, 0) as total_fods,
			COALESCE(
				(SELECT COUNT(*) FROM rebuild_queue rq WHERE rq.revision_id = r.id AND rq.status = 'failure'), 
				0
			) as failed_fods
		FROM revisions r
		LEFT JOIN revision_stats rs ON r.id = rs.revision_id
	`

	rows, err := sqliteDB.Query(query)
	if err != nil {
		return fmt.Errorf("failed to query evaluations: %w", err)
	}
	defer rows.Close()

	collection, err := pbApp.FindCollectionByNameOrId("evaluations")
	if err != nil {
		return fmt.Errorf("failed to find evaluations collection: %w", err)
	}

	count := 0
	for rows.Next() {
		var revisionId int64
		var input, status, startTime, endTime string
		var totalFods, failedFods int

		err := rows.Scan(&revisionId, &input, &status, &startTime, &endTime, &totalFods, &failedFods)
		if err != nil {
			debugLog("Error scanning evaluation row: %v", err)
			continue
		}

		// Create or update record in PocketBase
		record := core.NewRecord(collection)
		record.Set("input", input)
		record.Set("status", status)
		record.Set("start_time", startTime)
		record.Set("end_time", endTime)
		record.Set("total_fods", totalFods)
		record.Set("failed_fods", failedFods)
		record.Set("revision_id", revisionId)

		// Try to find existing record first
		existing, err := pbApp.FindFirstRecordByFilter("evaluations", "revision_id = {:revId}",
			map[string]any{"revId": revisionId})

		if err == nil && existing != nil {
			// Update existing record
			record.Id = existing.Id
			record.Set("created", existing.Get("created"))
			if err := pbApp.Save(record); err != nil {
				debugLog("Error updating evaluation record %d: %v", revisionId, err)
			}
		} else {
			// Create new record
			if err := pbApp.Save(record); err != nil {
				debugLog("Error creating evaluation record %d: %v", revisionId, err)
			}
		}
		count++
	}

	debugLog("Synced %d evaluation records", count)
	return nil
}

// startWebInterfaceWithSync starts web interface with existing SQLite data
func startWebInterfaceWithSync(port int, host string, sqliteDB *sql.DB) error {
	debugLog("Starting PocketBase web interface with data sync on %s:%d", host, port)

	app := pocketbase.New()

	// Set up data directory
	dataDir := "./pb_data"
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}

	// Setup collections and sync data after bootstrap
	app.OnBootstrap().Bind(&hook.Handler[*core.BootstrapEvent]{
		Func: func(e *core.BootstrapEvent) error {
			if err := e.Next(); err != nil {
				return err
			}

			// Setup collections first
			if err := setupFODCollections(e.App); err != nil {
				debugLog("Error setting up collections: %v", err)
			}

			// Sync data if SQLite DB is provided
			if sqliteDB != nil {
				if err := syncSQLiteDataToPocketBase(sqliteDB, e.App); err != nil {
					debugLog("Error syncing data: %v", err)
				}
			}

			return nil
		},
	})

	// Setup custom routes and middleware
	app.OnServe().Bind(&hook.Handler[*core.ServeEvent]{
		Func: func(e *core.ServeEvent) error {
			// Enable real-time streaming
			enableRealtimeStreaming(e.App)

			// Serve Svelte frontend
			setupSvelteRoutes(e)

			// Add real-time dashboard route
			e.Router.GET("/realtime", func(re *core.RequestEvent) error {
				return re.HTML(200, getRealtimeHTML())
			})

			// Add FOD Oracle API status endpoint
			e.Router.GET("/api/fod-status", func(re *core.RequestEvent) error {
				return re.JSON(200, map[string]interface{}{
					"status":             "ok",
					"message":            "FOD Oracle API is running",
					"timestamp":          time.Now().UTC(),
					"version":            "web-interface",
					"data_synced":        sqliteDB != nil,
					"realtime_streaming": streamActive,
				})
			})

			// Add data sync trigger endpoint
			if sqliteDB != nil {
				e.Router.POST("/api/sync-data", func(re *core.RequestEvent) error {
					if err := syncSQLiteDataToPocketBase(sqliteDB, re.App); err != nil {
						return re.JSON(500, map[string]interface{}{
							"error":   "Data sync failed",
							"details": err.Error(),
						})
					}
					return re.JSON(200, map[string]interface{}{
						"status":    "success",
						"message":   "Data synced successfully",
						"timestamp": time.Now().UTC(),
					})
				})
			}

			return e.Next()
		},
	})

	// Open browser if not in debug mode and host is localhost
	if !config.Debug && (host == "127.0.0.1" || host == "localhost") {
		go func() {
			time.Sleep(2 * time.Second) // Wait for PocketBase to start
			openBrowser(fmt.Sprintf("http://%s:%d", host, port))
		}()
	}

	log.Printf("PocketBase web interface starting at http://%s:%d", host, port)
	log.Printf("Admin interface will be available at http://%s:%d/_/", host, port)
	if sqliteDB != nil {
		log.Printf("Data sync enabled - SQLite data will be synced to PocketBase collections")
	}

	// Override os.Args to avoid conflicts with PocketBase argument parsing
	originalArgs := os.Args
	os.Args = []string{os.Args[0], "serve", "--http", fmt.Sprintf("%s:%d", host, port)}
	defer func() { os.Args = originalArgs }()

	// Start PocketBase
	return app.Start()
}
