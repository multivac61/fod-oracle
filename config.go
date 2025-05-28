package main

// Config holds the global configuration for the application
type Config struct {
	// IsNixExpr indicates if we're processing a Nix expression rather than a revision
	IsNixExpr bool

	// WorkerCount specifies how many worker threads to use
	WorkerCount int

	// Reevaluate indicates if we should reevaluate FODs by rebuilding them
	Reevaluate bool

	// BuildDelay specifies the delay between builds in seconds
	BuildDelay int

	// ParallelWorkers is the number of parallel rebuild workers
	ParallelWorkers int

	// Debug enables debug logging
	Debug bool
}

// Global configuration instance
var config Config
