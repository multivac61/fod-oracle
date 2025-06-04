<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { 
    fodsStore, 
    evaluationsStore, 
    connectionStatus, 
    realTimeStats, 
    recentFods, 
    filterOptions 
  } from '$lib/stores';
  import { realtimeService } from '$lib/realtime';
  import { extractPackageName, timeAgo, getStatusColor } from '$lib/pocketbase';
  import { 
    Activity, 
    Database, 
    Globe, 
    AlertCircle, 
    CheckCircle, 
    Clock, 
    Hash,
    Search,
    Filter,
    RefreshCw
  } from 'lucide-svelte';

  let searchTerm = '';
  let statusFilter = 'all';
  let autoScroll = true;
  let fodStreamElement: HTMLElement;

  // Subscribe to stores
  $: fods = $fodsStore;
  $: evaluations = $evaluationsStore;
  $: stats = $realTimeStats;
  $: recentFodsList = $recentFods;
  $: connected = $connectionStatus === 'connected';

  // Update filter options when local filters change
  $: filterOptions.set({
    status: statusFilter as any,
    search: searchTerm,
    evaluation: 'all'
  });

  onMount(async () => {
    console.log('🚀 Starting FOD Oracle Dashboard...');
    await realtimeService.connect();
    
    // Auto-scroll to new FODs if enabled
    fodsStore.subscribe(() => {
      if (autoScroll && fodStreamElement) {
        setTimeout(() => {
          fodStreamElement.scrollTop = 0;
        }, 100);
      }
    });
  });

  onDestroy(() => {
    realtimeService.disconnect();
  });

  function handleRefresh() {
    realtimeService.refresh();
  }

  function clearSearch() {
    searchTerm = '';
  }

  function getStatusIcon(status: string) {
    switch (status) {
      case 'success': return CheckCircle;
      case 'failed': 
      case 'timeout': 
      case 'hash_mismatch': return AlertCircle;
      case 'pending':
      case 'discovered': return Clock;
      default: return Activity;
    }
  }
</script>

<svelte:head>
  <title>🔮 FOD Oracle - Real-time Dashboard</title>
</svelte:head>

<div class="min-h-screen bg-fod-dark">
  <!-- Header -->
  <header class="bg-fod-gray border-b border-gray-700 px-6 py-4">
    <div class="max-w-7xl mx-auto flex items-center justify-between">
      <div class="flex items-center space-x-3">
        <div class="text-2xl">🔮</div>
        <h1 class="text-xl font-bold text-fod-green">FOD Oracle</h1>
        <span class="text-sm text-gray-400">Real-time Dashboard</span>
      </div>
      
      <div class="flex items-center space-x-4">
        <!-- Connection Status -->
        <div class="flex items-center space-x-2">
          <div class="w-2 h-2 rounded-full {connected ? 'bg-fod-green' : 'bg-fod-red'}"></div>
          <span class="text-sm text-gray-400">
            {connected ? 'Connected' : 'Disconnected'}
          </span>
        </div>
        
        <!-- Refresh Button -->
        <button 
          class="fod-button-secondary flex items-center space-x-2"
          on:click={handleRefresh}
        >
          <RefreshCw size={16} />
          <span>Refresh</span>
        </button>
      </div>
    </div>
  </header>

  <div class="max-w-7xl mx-auto px-6 py-6">
    <!-- Stats Grid -->
    <div class="grid grid-cols-2 md:grid-cols-4 lg:grid-cols-6 gap-4 mb-8">
      <div class="fod-stat">
        <div class="fod-stat-value">{stats.total}</div>
        <div class="fod-stat-label">Total FODs</div>
      </div>
      
      <div class="fod-stat">
        <div class="fod-stat-value text-fod-green">{stats.success}</div>
        <div class="fod-stat-label">Success</div>
      </div>
      
      <div class="fod-stat">
        <div class="fod-stat-value text-fod-red">{stats.failed}</div>
        <div class="fod-stat-label">Failed</div>
      </div>
      
      <div class="fod-stat">
        <div class="fod-stat-value text-fod-yellow">{stats.pending}</div>
        <div class="fod-stat-label">Pending</div>
      </div>
      
      <div class="fod-stat">
        <div class="fod-stat-value text-orange-400">{stats.hash_mismatch}</div>
        <div class="fod-stat-label">Hash Mismatch</div>
      </div>
      
      <div class="fod-stat">
        <div class="fod-stat-value text-blue-400">{stats.discovered}</div>
        <div class="fod-stat-label">Discovered</div>
      </div>
    </div>

    <!-- Main Content Grid -->
    <div class="grid lg:grid-cols-3 gap-6">
      <!-- FOD Stream -->
      <div class="lg:col-span-2">
        <div class="fod-card">
          <div class="flex items-center justify-between mb-4">
            <h2 class="text-lg font-semibold text-fod-green flex items-center space-x-2">
              <Activity size={20} />
              <span>Live FOD Stream</span>
            </h2>
            
            <!-- Controls -->
            <div class="flex items-center space-x-4">
              <label class="flex items-center space-x-2 text-sm">
                <input 
                  type="checkbox" 
                  bind:checked={autoScroll}
                  class="rounded bg-gray-700 border-gray-600 text-fod-green focus:ring-fod-green"
                />
                <span>Auto-scroll</span>
              </label>
            </div>
          </div>

          <!-- Filters -->
          <div class="flex flex-wrap gap-4 mb-4">
            <!-- Search -->
            <div class="flex-1 min-w-64">
              <div class="relative">
                <Search class="absolute left-3 top-1/2 transform -translate-y-1/2 text-gray-400" size={16} />
                <input
                  type="text"
                  placeholder="Search FODs, paths, hashes..."
                  bind:value={searchTerm}
                  class="w-full pl-10 pr-4 py-2 bg-gray-800 border border-gray-600 rounded-lg text-white placeholder-gray-400 focus:border-fod-green focus:ring-1 focus:ring-fod-green"
                />
                {#if searchTerm}
                  <button
                    on:click={clearSearch}
                    class="absolute right-3 top-1/2 transform -translate-y-1/2 text-gray-400 hover:text-white"
                  >
                    ×
                  </button>
                {/if}
              </div>
            </div>

            <!-- Status Filter -->
            <div class="flex items-center space-x-2">
              <Filter size={16} class="text-gray-400" />
              <select
                bind:value={statusFilter}
                class="bg-gray-800 border border-gray-600 rounded-lg px-3 py-2 text-white focus:border-fod-green focus:ring-1 focus:ring-fod-green"
              >
                <option value="all">All Status</option>
                <option value="success">Success</option>
                <option value="failed">Failed</option>
                <option value="pending">Pending</option>
                <option value="hash_mismatch">Hash Mismatch</option>
                <option value="discovered">Discovered</option>
              </select>
            </div>
          </div>

          <!-- FOD List -->
          <div 
            bind:this={fodStreamElement}
            class="h-96 overflow-y-auto space-y-2 pr-2"
          >
            {#if recentFodsList.length === 0}
              <div class="text-center py-12 text-gray-500">
                <Database size={48} class="mx-auto mb-4 opacity-50" />
                <p class="text-lg mb-2">Ready for real-time FOD streaming!</p>
                <p class="text-sm">FODs will appear here as they are discovered during evaluation</p>
              </div>
            {:else}
              {#each recentFodsList as fod (fod.id)}
                <div class="fod-item fod-item-{fod.rebuild_status}">
                  <div class="flex items-start justify-between">
                    <div class="flex-1 min-w-0">
                      <div class="flex items-center space-x-2 mb-1">
                        <svelte:component 
                          this={getStatusIcon(fod.rebuild_status)} 
                          size={16} 
                          class={getStatusColor(fod.rebuild_status)}
                        />
                        <span class="font-medium text-white">
                          {extractPackageName(fod.drv_path)}
                        </span>
                        <span class="px-2 py-1 text-xs rounded border {getStatusColor(fod.rebuild_status)}">
                          {fod.rebuild_status}
                        </span>
                      </div>
                      <div class="text-sm text-gray-400 font-mono truncate mb-1">
                        {fod.drv_path}
                      </div>
                      {#if fod.expected_hash}
                        <div class="flex items-center space-x-2 text-xs text-gray-500">
                          <Hash size={12} />
                          <span class="font-mono">{fod.expected_hash.slice(0, 16)}...</span>
                        </div>
                      {/if}
                    </div>
                    <div class="text-xs text-gray-500 ml-4">
                      {timeAgo(fod.created)}
                    </div>
                  </div>
                </div>
              {/each}
            {/if}
          </div>
        </div>
      </div>

      <!-- Sidebar -->
      <div class="space-y-6">
        <!-- Quick Actions -->
        <div class="fod-card">
          <h3 class="text-lg font-semibold text-white mb-4 flex items-center space-x-2">
            <Globe size={20} />
            <span>Quick Actions</span>
          </h3>
          
          <div class="space-y-3">
            <a 
              href="/_/" 
              target="_blank"
              class="block fod-button-secondary text-center"
            >
              🛠️ PocketBase Admin
            </a>
            
            <a 
              href="/api/fod-status" 
              target="_blank"
              class="block fod-button-secondary text-center"
            >
              🩺 API Status
            </a>
            
            <button 
              class="w-full fod-button-secondary"
              on:click={() => window.location.reload()}
            >
              🔄 Reload Dashboard
            </button>
          </div>
        </div>

        <!-- Connection Info -->
        <div class="fod-card">
          <h3 class="text-lg font-semibold text-white mb-4">Connection Info</h3>
          
          <div class="space-y-2 text-sm">
            <div class="flex justify-between">
              <span class="text-gray-400">Status:</span>
              <span class="{connected ? 'text-fod-green' : 'text-fod-red'}">
                {connected ? 'Connected' : 'Disconnected'}
              </span>
            </div>
            
            <div class="flex justify-between">
              <span class="text-gray-400">Total FODs:</span>
              <span class="text-white">{stats.total}</span>
            </div>
            
            <div class="flex justify-between">
              <span class="text-gray-400">Evaluations:</span>
              <span class="text-white">{evaluations.length}</span>
            </div>
          </div>
        </div>

        <!-- Instructions -->
        <div class="fod-card">
          <h3 class="text-lg font-semibold text-white mb-4">How to Use</h3>
          
          <div class="text-sm text-gray-400 space-y-2">
            <p>
              <strong class="text-white">Real-time streaming:</strong><br />
              Run <code class="bg-gray-800 px-1 rounded">./fod-oracle -expr "(import &lt;nixpkgs&gt; {'{'}{'}'}).hello" --web</code>
            </p>
            
            <p>
              <strong class="text-white">Standalone mode:</strong><br />
              Run <code class="bg-gray-800 px-1 rounded">./fod-oracle --web</code>
            </p>
            
            <p class="text-xs pt-2 border-t border-gray-700">
              FODs will appear in real-time as they are discovered during evaluation.
            </p>
          </div>
        </div>
      </div>
    </div>
  </div>
</div>
