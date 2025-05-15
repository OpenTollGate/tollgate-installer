import * as fs from 'fs';
import * as path from 'path';
import NDK, { NDKEvent, NDKFilter, NDKRelaySet } from '@nostr-dev-kit/ndk';

export interface UpdateInfo {
  latest: string;
  installed: string;
  canUpgrade: boolean;
  nip94Event?: NDKEvent;
  allVersions?: NDKEvent[];
}

export class UpdateManager {
  private ndk: NDK;
  private readonly TOLLGATE_OS_PUBKEY = '5075e61f0b048148b60105c1dd72bbeae1957336ae5824087e52efa374f8416a';
  private readonly UPDATE_KIND = 1063; // NIP-94 kind for file metadata

  constructor() {
    console.log("DEBUG - UpdateManager: Constructor called");
    // Initialize NDK with default relays
    this.ndk = new NDK({
      explicitRelayUrls: [
        'wss://relay.damus.io',
        'wss://relay.tollgate.me',
        'wss://nos.lol',
        'wss://nostr.mom',
      ]
    });
    console.log("DEBUG - UpdateManager: NDK initialized with relays");
  }

  /**
   * Connects to Nostr network and fetches update information
   */
  public async checkForUpdates(): Promise<UpdateInfo> {
    try {
      console.log("DEBUG - UpdateManager: checkForUpdates called");
      
      // Connect to relays
      console.log("DEBUG - UpdateManager: Attempting to connect to NDK relays");
      try {
        await this.ndk.connect();
        console.log("DEBUG - UpdateManager: Successfully connected to NDK relays");
      } catch (connectError) {
        console.error("DEBUG - UpdateManager: Error connecting to NDK relays", connectError);
        throw connectError;
      }

      // Get currently installed version (placeholder)
      const installedVersion = this.getInstalledVersion();
      console.log("DEBUG - UpdateManager: Installed version:", installedVersion);

      // Fetch NIP-94 events for TollGateOS releases
      const releaseEvents = await this.fetchReleaseEvents();
      
      console.log("DEBUG - fetchReleaseEvents result:", releaseEvents);
      console.log("DEBUG - fetchReleaseEvents count:", releaseEvents.length);
      
      if (!releaseEvents || releaseEvents.length === 0) {
        return {
          latest: 'unknown',
          installed: installedVersion || 'not installed',
          canUpgrade: false,
          allVersions: []
        };
      }

      // Latest release is the first one (they're sorted by created_at)
      const latestReleaseEvent = releaseEvents[0];

      // Extract version from the event
      const latestVersion = this.getVersionFromEvent(latestReleaseEvent);

      // Determine if upgrade is available
      const canUpgrade = installedVersion && latestVersion
        ? this.compareVersions(latestVersion, installedVersion) > 0
        : false;

      const result = {
        latest: latestVersion || 'unknown',
        installed: installedVersion || 'not installed',
        canUpgrade,
        nip94Event: latestReleaseEvent,
        allVersions: releaseEvents
      };
      
      console.log("DEBUG - checkForUpdates returning:", result);
      return result;
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
   * Fetches TollGateOS release events from Nostr
   */
  private async fetchReleaseEvents(): Promise<NDKEvent[]> {
    try {
      // Create a filter for NIP-94 events from the TollGateOS publisher
      const filter: NDKFilter = {
        kinds: [this.UPDATE_KIND],
        authors: [this.TOLLGATE_OS_PUBKEY],
        since: 1747216016,
        // limit: 10 // Get several recent events
      };

      // Fetch events from relays
      console.log("DEBUG - Fetching events with filter:", filter);
      const events = await this.ndk.fetchEvents(filter);
      
      console.log("DEBUG - Raw events fetched:", events);
      
      if (!events || events.size === 0) {
        console.log('DEBUG - No release events found');
        return [];
      }

      console.log(`events: ${events}`)

      // Convert to array and sort by created_at (newest first)
      const eventsArray = Array.from(events.values()).sort(
        (a, b) => b.created_at! - a.created_at!
      );

      console.log("DEBUG - Sorted events array:", eventsArray);
      return eventsArray;
    } catch (error) {
      console.error('Error fetching release events:', error);
      return [];
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
  /**
   * Returns mock events for testing when Nostr connection fails
   */
  private getMockEvents(): NDKEvent[] {
    console.log("DEBUG - UpdateManager: Creating mock events");
    
    // Create a few mock events based on the example JSON structure
    const mockEvents = [];
    
    for (let i = 0; i < 3; i++) {
      const event = new NDKEvent(this.ndk);
      event.kind = this.UPDATE_KIND;
      event.pubkey = this.TOLLGATE_OS_PUBKEY;
      event.created_at = Math.floor(Date.now() / 1000) - (i * 86400); // Today, yesterday, etc.
      
      // Add tags similar to the example
      event.tags = [
        ["url", `https://example.com/firmware${i}.bin`],
        ["m", "application/octet-stream"],
        ["filename", `openwrt-23.05.${3-i}-gl-mt600${i}-squashfs-sysupgrade.bin`],
        ["architecture", "aarch64_cortex-a53"],
        ["model", `gl-mt600${i}`],
        ["openwrt_version", `23.05.${3-i}`],
        ["tollgate_os_version", `v0.0.${i}`]
      ];
      
      event.content = `TollGate OS Firmware for gl-mt600${i}`;
      
      mockEvents.push(event);
    }
    
    console.log("DEBUG - UpdateManager: Created mock events:", mockEvents);
    return mockEvents;
  }

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