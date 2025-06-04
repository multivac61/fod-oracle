import { writable, derived } from 'svelte/store';
import type { FOD, Evaluation, EvaluationStats } from './pocketbase';

// Real-time FOD stream
export const fodsStore = writable<FOD[]>([]);

// Current evaluations
export const evaluationsStore = writable<Evaluation[]>([]);

// Connection status
export const connectionStatus = writable<'connected' | 'disconnected' | 'connecting'>('disconnected');

// Global stats
export const statsStore = writable<EvaluationStats>({
  total_evaluations: 0,
  running_evaluations: 0,
  completed_evaluations: 0,
  total_fods: 0,
  successful_fods: 0,
  failed_fods: 0,
  hash_mismatches: 0
});

// Filter options
export const filterOptions = writable({
  status: 'all' as 'all' | 'success' | 'failed' | 'pending' | 'hash_mismatch',
  search: '',
  evaluation: 'all' as string
});

// Derived filtered FODs
export const filteredFods = derived(
  [fodsStore, filterOptions],
  ([$fods, $filters]) => {
    let filtered = $fods;
    
    // Filter by status
    if ($filters.status !== 'all') {
      filtered = filtered.filter(fod => fod.rebuild_status === $filters.status);
    }
    
    // Filter by search term
    if ($filters.search) {
      const search = $filters.search.toLowerCase();
      filtered = filtered.filter(fod => 
        fod.drv_path.toLowerCase().includes(search) ||
        fod.output_path.toLowerCase().includes(search) ||
        fod.expected_hash.toLowerCase().includes(search)
      );
    }
    
    // Filter by evaluation
    if ($filters.evaluation !== 'all') {
      filtered = filtered.filter(fod => fod.evaluation_id === $filters.evaluation);
    }
    
    return filtered;
  }
);

// Derived stats from current FODs
export const realTimeStats = derived(fodsStore, ($fods) => {
  const stats = {
    total: $fods.length,
    success: 0,
    failed: 0,
    pending: 0,
    hash_mismatch: 0,
    discovered: 0
  };
  
  $fods.forEach(fod => {
    switch (fod.rebuild_status) {
      case 'success':
        stats.success++;
        break;
      case 'failed':
      case 'timeout':
        stats.failed++;
        break;
      case 'pending':
        stats.pending++;
        break;
      case 'hash_mismatch':
        stats.hash_mismatch++;
        break;
      case 'discovered':
        stats.discovered++;
        break;
    }
  });
  
  return stats;
});

// Recent activity (last 50 FODs)
export const recentFods = derived(fodsStore, ($fods) => {
  return $fods
    .sort((a, b) => new Date(b.created).getTime() - new Date(a.created).getTime())
    .slice(0, 50);
});

// Store actions
export const storeActions = {
  addFod: (fod: FOD) => {
    fodsStore.update(fods => [fod, ...fods]);
  },
  
  updateFod: (updatedFod: FOD) => {
    fodsStore.update(fods => 
      fods.map(fod => fod.id === updatedFod.id ? updatedFod : fod)
    );
  },
  
  clearFods: () => {
    fodsStore.set([]);
  },
  
  addEvaluation: (evaluation: Evaluation) => {
    evaluationsStore.update(evals => [evaluation, ...evals]);
  },
  
  updateEvaluation: (updatedEval: Evaluation) => {
    evaluationsStore.update(evals =>
      evals.map(evaluation => evaluation.id === updatedEval.id ? updatedEval : evaluation)
    );
  },
  
  setConnectionStatus: (status: 'connected' | 'disconnected' | 'connecting') => {
    connectionStatus.set(status);
  }
};