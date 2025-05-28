import { pb, type FOD, type Evaluation } from './pocketbase';
import { storeActions } from './stores';

class RealtimeService {
  private fodsUnsubscribe: (() => void) | null = null;
  private evaluationsUnsubscribe: (() => void) | null = null;
  private reconnectInterval: number | null = null;
  private isConnecting = false;

  async connect() {
    if (this.isConnecting) return;
    
    this.isConnecting = true;
    storeActions.setConnectionStatus('connecting');
    
    try {
      // Test connection first
      await pb.health.check();
      
      // Subscribe to FODs collection
      await this.subscribeFods();
      
      // Subscribe to evaluations collection  
      await this.subscribeEvaluations();
      
      // Load initial data
      await this.loadInitialData();
      
      storeActions.setConnectionStatus('connected');
      console.log('✅ Connected to PocketBase realtime');
      
      // Clear any existing reconnect interval
      if (this.reconnectInterval) {
        clearInterval(this.reconnectInterval);
        this.reconnectInterval = null;
      }
      
    } catch (error) {
      console.error('❌ Failed to connect to PocketBase:', error);
      storeActions.setConnectionStatus('disconnected');
      
      // Set up auto-reconnect
      this.setupReconnect();
    } finally {
      this.isConnecting = false;
    }
  }

  private async subscribeFods() {
    try {
      // Unsubscribe from previous subscription if exists
      if (this.fodsUnsubscribe) {
        this.fodsUnsubscribe();
      }

      this.fodsUnsubscribe = await pb.collection('fods').subscribe('*', (e) => {
        const record = e.record as unknown as FOD;
        
        switch (e.action) {
          case 'create':
            storeActions.addFod(record);
            console.log('📦 New FOD:', record.drv_path);
            break;
          case 'update':
            storeActions.updateFod(record);
            console.log('🔄 Updated FOD:', record.drv_path);
            break;
          case 'delete':
            // Handle delete if needed
            break;
        }
      });
      
      console.log('🔗 Subscribed to FODs collection');
    } catch (error) {
      console.warn('⚠️ FODs collection not found or not accessible:', error);
    }
  }

  private async subscribeEvaluations() {
    try {
      // Unsubscribe from previous subscription if exists
      if (this.evaluationsUnsubscribe) {
        this.evaluationsUnsubscribe();
      }

      this.evaluationsUnsubscribe = await pb.collection('evaluations').subscribe('*', (e) => {
        const record = e.record as unknown as Evaluation;
        
        switch (e.action) {
          case 'create':
            storeActions.addEvaluation(record);
            console.log('📊 New evaluation:', record.evaluation_id);
            break;
          case 'update':
            storeActions.updateEvaluation(record);
            console.log('📈 Updated evaluation:', record.evaluation_id);
            break;
          case 'delete':
            // Handle delete if needed
            break;
        }
      });
      
      console.log('🔗 Subscribed to evaluations collection');
    } catch (error) {
      console.warn('⚠️ Evaluations collection not found or not accessible:', error);
    }
  }

  private async loadInitialData() {
    try {
      // Load recent FODs
      const fodsList = await pb.collection('fods').getList(1, 100, {
        sort: '-created',
      });
      
      fodsList.items.forEach(fod => {
        storeActions.addFod(fod as unknown as FOD);
      });
      
      console.log(`📥 Loaded ${fodsList.items.length} initial FODs`);
    } catch (error) {
      console.warn('⚠️ Could not load initial FODs:', error);
    }

    try {
      // Load recent evaluations
      const evalsList = await pb.collection('evaluations').getList(1, 20, {
        sort: '-created',
      });
      
      evalsList.items.forEach(evaluation => {
        storeActions.addEvaluation(evaluation as unknown as Evaluation);
      });
      
      console.log(`📥 Loaded ${evalsList.items.length} initial evaluations`);
    } catch (error) {
      console.warn('⚠️ Could not load initial evaluations:', error);
    }
  }

  private setupReconnect() {
    if (this.reconnectInterval) return;
    
    console.log('🔄 Setting up auto-reconnect...');
    this.reconnectInterval = setInterval(() => {
      console.log('🔄 Attempting to reconnect...');
      this.connect();
    }, 5000); // Try to reconnect every 5 seconds
  }

  disconnect() {
    console.log('🔌 Disconnecting from PocketBase...');
    
    if (this.fodsUnsubscribe) {
      this.fodsUnsubscribe();
      this.fodsUnsubscribe = null;
    }
    
    if (this.evaluationsUnsubscribe) {
      this.evaluationsUnsubscribe();
      this.evaluationsUnsubscribe = null;
    }
    
    if (this.reconnectInterval) {
      clearInterval(this.reconnectInterval);
      this.reconnectInterval = null;
    }
    
    storeActions.setConnectionStatus('disconnected');
  }

  // Manual data refresh
  async refresh() {
    console.log('🔄 Refreshing data...');
    storeActions.clearFods();
    await this.loadInitialData();
  }
}

// Export singleton instance
export const realtimeService = new RealtimeService();