//go:build integration

package main

import (
	"os"

	"github.com/multivac61/fod-oracle/tests"
)

func main() {
	// Pass the command-line arguments to the test function
	os.Args = append([]string{"test_fod"}, os.Args[1:]...)
	tests.RunTestFOD()
}
