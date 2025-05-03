import * as fs from 'fs';
import * as path from 'path';
import NDK, { NDKEvent, NDKFilter, NDKRelaySet } from '@nostr-dev-kit/ndk';

export interface UpdateInfo {
  latest: string;
  installed: string;
  canUpgrade: boolean;
  nip94Event?: NDKEvent;
}

export class UpdateManager {
  private ndk: NDK;
  private readonly TOLLGATE_OS_PUBKEY = 'a6c099cca43f9c5ed34a6df8212865aa63e76710a083ac7209152aadb4750da7';
  private readonly UPDATE_KIND = 1063; // NIP-94 kind for file metadata

  constructor() {
    // Initialize NDK with default relays
    this.ndk = new NDK({
      explicitRelayUrls: [
        'wss://relay.damus.io',
        'wss://relay.nostr.band',
        'wss://nos.lol'
      ]
    });
  }

  /**
   * Connects to Nostr network and fetches update information
   */
  public async checkForUpdates(): Promise<UpdateInfo> {
    try {
      // Connect to relays
      await this.ndk.connect();

      // Get currently installed version (placeholder)
      const installedVersion = this.getInstalledVersion();

      // Fetch NIP-94 events for TollGateOS releases
      const latestReleaseEvent = await this.fetchLatestReleaseEvent();

      if (!latestReleaseEvent) {
        return {
          latest: 'unknown',
          installed: installedVersion || 'not installed',
          canUpgrade: false
        };
      }

      // Extract version from the event
      const latestVersion = this.getVersionFromEvent(latestReleaseEvent);

      // Determine if upgrade is available
      const canUpgrade = installedVersion && latestVersion 
        ? this.compareVersions(latestVersion, installedVersion) > 0
        : false;

      return {
        latest: latestVersion || 'unknown',
        installed: installedVersion || 'not installed',
        canUpgrade,
        nip94Event: latestReleaseEvent
      };
    } catch (error) {
      console.error('Error checking for updates:', error);
      return {
        latest: 'error',
        installed: this.getInstalledVersion() || 'unknown',
        canUpgrade: false
      };
    } finally {
      // Disconnect from NDK
      // this.ndk.disconnect();
    }
  }

  /**
   * Fetches the latest TollGateOS release event from Nostr
   */
  private async fetchLatestReleaseEvent(): Promise<NDKEvent | null> {
    try {
      // Create a filter for NIP-94 events from the TollGateOS publisher
      const filter: NDKFilter = {
        kinds: [this.UPDATE_KIND],
        authors: [this.TOLLGATE_OS_PUBKEY],
        limit: 10 // Get several recent events to find the latest version
      };

      // Fetch events from relays
      const events = await this.ndk.fetchEvents(filter);
      
      if (!events || events.size === 0) {
        console.log('No release events found');
        return null;
      }

      // Convert to array and sort by created_at (newest first)
      const eventsArray = Array.from(events.values()).sort(
        (a, b) => b.created_at! - a.created_at!
      );

      // Process events to find the latest compatible release
      // In a real implementation, you'd filter for specific router models/architectures
      return eventsArray[0]; // Just return the newest for this prototype
    } catch (error) {
      console.error('Error fetching release events:', error);
      return null;
    }
  }

  /**
   * Gets the version from a NIP-94 event
   */
  private getVersionFromEvent(event: NDKEvent): string | null {
    try {
      // Find the tollgate_os_version tag
      const versionTag = event.tags.find(
        tag => tag[0] === 'tollgate_os_version'
      );

      if (versionTag && versionTag[1]) {
        return versionTag[1];
      }

      return null;
    } catch (error) {
      console.error('Error extracting version from event:', error);
      return null;
    }
  }

  /**
   * Gets the currently installed TollGateOS version
   * In a real implementation, this would read from a local config file
   */
  private getInstalledVersion(): string | null {
    // This is a placeholder for real implementation
    // In a real app, you would read this from a configuration file
    // where the installed version is stored
    return 'v0.0.0';
  }

  /**
   * Compares two semver-like versions
   * Returns:
   * - positive number if v1 > v2
   * - negative number if v1 < v2
   * - 0 if v1 === v2
   */
  private compareVersions(v1: string, v2: string): number {
    // Remove 'v' prefix if present
    const v1Clean = v1.startsWith('v') ? v1.substring(1) : v1;
    const v2Clean = v2.startsWith('v') ? v2.substring(1) : v2;
    
    // Split into components
    const v1Components = v1Clean.split('.').map(Number);
    const v2Components = v2Clean.split('.').map(Number);
    
    // Compare each component
    for (let i = 0; i < Math.max(v1Components.length, v2Components.length); i++) {
      const comp1 = i < v1Components.length ? v1Components[i] : 0;
      const comp2 = i < v2Components.length ? v2Components[i] : 0;
      
      if (comp1 !== comp2) {
        return comp1 - comp2;
      }
    }
    
    return 0; // Versions are equal
  }
}