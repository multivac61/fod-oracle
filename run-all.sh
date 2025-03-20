#!/bin/bash
# Script to build and run both API server and frontend

set -e

# Build API server
echo "Building API server..."
go build -o bin/api-server cmd/api/main.go

# Start API server in the background
echo "Starting API server..."
./bin/api-server >api.log 2>&1 &
API_PID=$!

# Wait for API server to start
echo "Waiting for API server to start..."
sleep 3

# Install frontend dependencies
echo "Installing frontend dependencies..."
cd frontend
pnpm install

# Start frontend
echo "Starting frontend development server..."
pnpm run dev -- --open

# When frontend is killed, also kill API server
trap "kill $API_PID" EXIT

