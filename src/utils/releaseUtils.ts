import { NDKEvent } from '@nostr-dev-kit/ndk';

/**
 * Checks if a release is compatible with a router based on model information
 * 
 * @param release The Nostr event containing release information
 * @param routerBoardName The board name of the router to check against
 * @returns True if the release is compatible with the router, false otherwise
 */
export const isReleaseCompatible = (release: NDKEvent, routerBoardName?: string): boolean => {
  // If no board name is provided, assume incompatible
  if (!routerBoardName) return false;
  
  // Get the model name from the release event
  const supportedDevices = release.getMatchingTags('supported_devices')?.[0]?.[1];
  
  // If no model name is specified in the release, assume incompatible
  if (!supportedDevices) return false;

  console.log(`${supportedDevices} / ${routerBoardName}`)
  
  // Check if the model name is a substring of the board name
  return supportedDevices.toLowerCase().includes(routerBoardName.toLowerCase());
};

/**
 * Get a display-friendly version number from a Nostr event
 * 
 * @param release The Nostr event containing release information
 * @returns A formatted version string
 */
export const getReleaseVersion = (release: NDKEvent): string => {
  return release.getMatchingTags("tollgate_os_version")?.[0]?.[1] || release.id.substring(0, 8);
};

/**
 * Get the formatted release date for a release
 * 
 * @param release The Nostr event containing release information
 * @returns A formatted date string
 */
export const getReleaseDate = (release: NDKEvent): string => {
  return release.created_at
    ? `${new Date(release.created_at * 1000).toLocaleDateString()} ${new Date(release.created_at * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}`
    : "Unknown";
};

/**
 * Get model details for a release
 * 
 * @param release The Nostr event containing release information
 * @returns The model name or "Unknown"
 */
export const getReleaseModel = (release: NDKEvent): string => {
  return release.getMatchingTags("model")?.[0]?.[1] || "Unknown";
};

/**
 * Get architecture details for a release
 * 
 * @param release The Nostr event containing release information 
 * @returns The architecture or "Unknown"
 */
export const getReleaseArchitecture = (release: NDKEvent): string => {
  return release.getMatchingTags("architecture")?.[0]?.[1] || "Unknown";
};

/**
 * Get OpenWRT version details
 * 
 * @param release The Nostr event containing release information
 * @returns The OpenWRT version or "Unknown"
 */
export const getReleaseOpenWrtVersion = (release: NDKEvent): string => {
  return release.getMatchingTags("openwrt_version")?.[0]?.[1] || "Unknown";
};

/**
 * Get the device ID for a release
 *
 * @param release The Nostr event containing release information
 * @returns The device ID or "Unknown"
 */
export const getReleaseDeviceId = (release: NDKEvent): string => {
  return release.getMatchingTags("supported_devices")?.[0]?.[1] || "Unknown";
};

/**
 * Truncate text to a specified maximum length, adding ellipsis if needed
 *
 * @param text The text to truncate
 * @param maxLength The maximum allowed length (including ellipsis)
 * @returns The truncated text with ellipsis if necessary
 */
export const truncateText = (text: string, maxLength: number): string => {
  if (text.length <= maxLength) return text;
  return text.substring(0, maxLength - 3) + '...';
};