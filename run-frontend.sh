#!/bin/bash
# Script to run the FOD Oracle frontend

set -e

cd frontend

echo "Installing dependencies..."
pnpm install

echo "Starting development server..."
pnpm run dev -- --open

