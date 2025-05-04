import React, { createContext, useContext, useEffect, useState } from 'react';
import NDK, { NDKEvent, NDKFilter } from '@nostr-dev-kit/ndk';

// Define the context type
interface NostrVersionContextType {
  versions: NDKEvent[];
  loading: boolean;
  error: string | null;
}

// Create context with default values
const NostrVersionContext = createContext<NostrVersionContextType>({
  versions: [],
  loading: true,
  error: null
});

// Custom hook to use the context
export const useNostrVersions = () => useContext(NostrVersionContext);

interface NostrVersionProviderProps {
  children: React.ReactNode;
}

const NostrVersionProvider: React.FC<NostrVersionProviderProps> = ({ children }) => {
  const [versions, setVersions] = useState<NDKEvent[]>([]);
  const [loading, setLoading] = useState<boolean>(true);
  const [error, setError] = useState<string | null>(null);
  
  // Nostr constants
  const TOLLGATE_OS_PUBKEY = 'a6c099cca43f9c5ed34a6df8212865aa63e76710a083ac7209152aadb4750da7';
  const UPDATE_KIND = 1063; // NIP-94 kind for file metadata
  
  useEffect(() => {
    let ndk: NDK;
    
    const connectToNostr = async () => {
      try {
        console.log("NostrVersionProvider: Initializing NDK");
        
        // Initialize NDK with default relays
        ndk = new NDK({
          explicitRelayUrls: [
            'wss://relay.damus.io',
            'wss://relay.nostr.band',
            'wss://nos.lol'
          ]
        });
        
        // Connect to relays
        console.log("NostrVersionProvider: Connecting to relays");
        await ndk.connect();
        console.log("NostrVersionProvider: Connected to relays");
        
        // Create a filter for NIP-94 events from the TollGateOS publisher
        const filter: NDKFilter = {
          kinds: [UPDATE_KIND],
          authors: [TOLLGATE_OS_PUBKEY],
          limit: 10 // Get several recent events
        };
        
        console.log("NostrVersionProvider: Creating subscription with filter:", filter);
        
        // Subscribe to events
        const subscription = ndk.subscribe(filter, { closeOnEose: false });
        
        // Handle events as they arrive
        subscription.on('event', (event: NDKEvent) => {
          console.log("NostrVersionProvider: Received new event:", event);
          // Add event to state if it's not already there
          setVersions(prevVersions => {
            // Check if event already exists in array
            const eventExists = prevVersions.some(e => e.id === event.id);
            if (!eventExists) {
              // Sort by created_at (newest first)
              const newVersions = [...prevVersions, event].sort(
                (a, b) => (b.created_at || 0) - (a.created_at || 0)
              );
              console.log("NostrVersionProvider: Updated versions array:", newVersions);
              return newVersions;
            }
            return prevVersions;
          });
          setLoading(false);
        });
        
        subscription.on('eose', () => {
          console.log("NostrVersionProvider: End of stored events");
          // If we didn't get any events, provide mock data for testing
          if (versions.length === 0) {
            console.log("NostrVersionProvider: No events received, using mock data");
            setVersions(getMockEvents(ndk));
          }
          setLoading(false);
        });
        
      } catch (err) {
        console.error("NostrVersionProvider: Error connecting to Nostr:", err);
        setError(`Failed to connect to Nostr: ${err instanceof Error ? err.message : String(err)}`);
        setLoading(false);
        
        // Provide mock data for testing
        setVersions(getMockEvents(ndk!));
      }
    };
    
    connectToNostr();
    
    // Cleanup function
    return () => {
      // Disconnect NDK when component unmounts
      if (ndk) {
        console.log("NostrVersionProvider: Disconnecting from relays");
        // ndk.pool.close(); // In newer versions of NDK
      }
    };
  }, []);
  
  /**
   * Creates mock events for testing when Nostr connection fails
   */
  const getMockEvents = (ndk: NDK): NDKEvent[] => {
    console.log("NostrVersionProvider: Creating mock events");
    
    // Create a few mock events based on the example JSON structure
    const mockEvents = [];
    
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
      
      mockEvents.push(event);
    }
    
    console.log("NostrVersionProvider: Created mock events:", mockEvents);
    return mockEvents;
  };
  
  return (
    <NostrVersionContext.Provider value={{ versions, loading, error }}>
      {children}
    </NostrVersionContext.Provider>
  );
};

export default NostrVersionProvider;