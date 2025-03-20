#!/bin/bash
# Script to run the FOD Oracle API server

set -e

echo "Building API server..."
go build -o bin/api-server cmd/api/main.go

echo "Starting FOD Oracle API server..."
./bin/api-server