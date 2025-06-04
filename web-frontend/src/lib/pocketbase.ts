import PocketBase from 'pocketbase';

// PocketBase client instance - use current hostname and port
const getBaseUrl = () => {
  if (typeof window !== 'undefined') {
    // In browser: use current hostname and port
    return `${window.location.protocol}//${window.location.host}`;
  }
  // Fallback for SSR
  return 'http://localhost:8080';
};

export const pb = new PocketBase(getBaseUrl());

// Disable auto-cancellation to avoid issues with concurrent requests
pb.autoCancellation(false);

// Types for our data structures
export interface FOD {
  id: string;
  drv_path: string;
  output_path: string;
  hash_algorithm: string;
  expected_hash: string;
  actual_hash?: string;
  rebuild_status: 'pending' | 'discovered' | 'success' | 'failed' | 'timeout' | 'hash_mismatch';
  hash_mismatch?: boolean;
  error_message?: string;
  evaluation_id?: string;
  discovered_at?: string;
  created: string;
  updated: string;
}

export interface Evaluation {
  id: string;
  evaluation_id: string;
  input: string;
  status: 'running' | 'completed' | 'failed';
  start_time: string;
  end_time?: string;
  total_fods: number;
  processed_fods: number;
  failed_fods: number;
  current_package?: string;
  progress_percent: number;
  last_update: string;
  created: string;
  updated: string;
}

export interface EvaluationStats {
  total_evaluations: number;
  running_evaluations: number;
  completed_evaluations: number;
  total_fods: number;
  successful_fods: number;
  failed_fods: number;
  hash_mismatches: number;
}

// Helper function to extract package name from derivation path
export function extractPackageName(drvPath: string): string {
  const match = drvPath.match(/\/nix\/store\/[^-]+-(.+?)\.drv$/);
  return match ? match[1] : 'unknown';
}

// Helper function to format time ago
export function timeAgo(timestamp: string): string {
  const now = new Date();
  const time = new Date(timestamp);
  const diff = now.getTime() - time.getTime();
  
  const seconds = Math.floor(diff / 1000);
  const minutes = Math.floor(seconds / 60);
  const hours = Math.floor(minutes / 60);
  const days = Math.floor(hours / 24);
  
  if (days > 0) return `${days}d ago`;
  if (hours > 0) return `${hours}h ago`;
  if (minutes > 0) return `${minutes}m ago`;
  return `${seconds}s ago`;
}

// Format status badge color
export function getStatusColor(status: string): string {
  switch (status) {
    case 'success':
      return 'text-fod-green border-fod-green';
    case 'failed':
    case 'timeout':
    case 'hash_mismatch':
      return 'text-fod-red border-fod-red';
    case 'running':
    case 'pending':
      return 'text-fod-yellow border-fod-yellow';
    default:
      return 'text-gray-400 border-gray-400';
  }
}