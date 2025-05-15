import React, { createContext, useContext, useEffect, useState } from 'react';
import NDK, { NDKEvent, NDKFilter } from '@nostr-dev-kit/ndk';
import { isReleaseCompatible } from '../utils/releaseUtils';

// Define the context type
interface NostrReleaseContextType {
  releases: NDKEvent[];
  loading: boolean;
  error: string | null;
}

// Create context with default values
const NostrReleaseContext = createContext<NostrReleaseContextType>({
  releases: [],
  loading: true,
  error: null
});

// Custom hook to use the context
export const useNostrReleases = () => useContext(NostrReleaseContext);

interface NostrReleaseProviderProps {
  children: React.ReactNode;
}

const NostrReleaseProvider: React.FC<NostrReleaseProviderProps> = ({ children }) => {
  const [releases, setReleases] = useState<NDKEvent[]>([]);
  const [loading, setLoading] = useState<boolean>(true);
  const [error, setError] = useState<string | null>(null);
  
  // Nostr constants
  const TOLLGATE_OS_PUBKEY = 'a6c099cca43f9c5ed34a6df8212865aa63e76710a083ac7209152aadb4750da7';
  const UPDATE_KIND = 1063; // NIP-94 kind for file metadata
  
  useEffect(() => {
    let ndk: NDK;
    let hasReceivedEvents = false;
    
    const connectToNostr = async () => {
      try {
        console.log("NostrReleaseProvider: Initializing NDK");
        
        // Initialize NDK with default relays
        ndk = new NDK({
          explicitRelayUrls: [
            'wss://relay.damus.io',
            'wss://relay.nostr.band',
            'wss://nos.lol'
          ]
        });
        
        // Connect to relays
        await ndk.connect();
        
        // Create a filter for NIP-94 events from the TollGateOS publisher
        const filter: NDKFilter = {
          kinds: [UPDATE_KIND],
          authors: [TOLLGATE_OS_PUBKEY],
          limit: 100 // Get several recent events
        };
        
        
        // Subscribe to events
        const subscription = ndk.subscribe(filter, { closeOnEose: false });
        
        // Handle events as they arrive
        subscription.on('event', (event: NDKEvent) => {
          hasReceivedEvents = true; // Mark that we've received at least one event
          
          // Add event to state if it's not already there
          setReleases(prevReleases => {
            // Check if event already exists in array
            const eventExists = prevReleases.some(e => e.id === event.id);
            if (!eventExists) {
              // Sort by created_at (newest first)
              const newReleases = [...prevReleases, event].sort(
                (a, b) => (b.created_at || 0) - (a.created_at || 0)
              );
              return newReleases;
            }
            return prevReleases;
          });
          setLoading(false);
        });
        
        subscription.on('eose', () => {
          // Only use mock data if we haven't received any events
          if (!hasReceivedEvents) {
            console.log("NostrReleaseProvider: No events received, using mock data");
            setReleases(getMockReleases(ndk));
          }
          setLoading(false);
        });
        
      } catch (err) {
        console.error("NostrReleaseProvider: Error connecting to Nostr:", err);
        setError(`Failed to connect to Nostr: ${err instanceof Error ? err.message : String(err)}`);
        setLoading(false);
        
        // Provide mock data for testing
        setReleases(getMockReleases(ndk!));
      }
    };
    
    connectToNostr();
    
    // Cleanup function
    return () => {
      // Disconnect NDK when component unmounts
      if (ndk) {
        console.log("NostrReleaseProvider: Disconnecting from relays");
        // ndk.pool.close(); // In newer versions of NDK
      }
    };
  }, []);
  
  /**
   * Creates mock releases for testing when Nostr connection fails
   */
  const getMockReleases = (ndk: NDK): NDKEvent[] => {
    console.log("NostrReleaseProvider: Creating mock releases");
    
    // Create a few mock events based on the example JSON structure
    const mockReleases = [];
    
    for (let i = 0; i < 3; i++) {
      const event = new NDKEvent(ndk);
      event.kind = UPDATE_KIND;
      event.pubkey = TOLLGATE_OS_PUBKEY;
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
      
      mockReleases.push(event);
    }
    
    console.log("NostrReleaseProvider: Created mock releases:", mockReleases);
    return mockReleases;
  };
  
  return (
    <NostrReleaseContext.Provider value={{
      releases,
      loading,
      error
    }}>
      {children}
    </NostrReleaseContext.Provider>
  );
};

export default NostrReleaseProvider;