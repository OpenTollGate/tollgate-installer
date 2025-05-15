import * as ip from 'ip';
import * as net from 'net';
import { SshConnector, RouterInfo } from './ssh-connector';
import { ScanResult } from '../../shared/types';

export class NetworkScanner {
  private readonly timeoutMs: number;
  private readonly subnetRanges: string[];
  private readonly sshConnector: SshConnector;

  constructor(config?: { timeoutMs?: number; subnetRanges?: string[] }) {
    this.timeoutMs = config?.timeoutMs || 300; // Default timeout: 1 second
    this.subnetRanges = config?.subnetRanges || ["192.168.8.0/24", '192.168.0.0/24', '192.168.1.0/24', '10.0.0.0/24']; // 192.168.0.0/16 (last resort)
    this.sshConnector = new SshConnector();
  }

  /**
   * Scans the network for potential routers
   * First tries to detect the default gateway, then scans subnet for SSH-enabled devices
   */
  public async scan(): Promise<ScanResult[]> {
    const results: ScanResult[] = [];
    
    try {
      // First, try to find the default gateway (most likely a router)
      // Gateway detection completely disabled
      console.log("Gateway detection disabled, proceeding with direct IP check");

      // Then scan subnets for other potential routers
      const subnetResults = await this.scanSubnetsForSsh();
      
      // Filter out duplicate IPs (including the gateway if found)
      const existingIps = new Set(results.map(r => r.ip));
      for (const result of subnetResults) {
        if (!existingIps.has(result.ip)) {
          results.push(result);
          existingIps.add(result.ip);
        }
      }

      return results;
    } catch (error) {
      console.error('Error scanning network:', error);
      // If gateway detection fails, still return subnet scan results if available
      return results.length > 0 ? results : [];
    }
  }

  /**
   * Scans configured subnets for devices with open SSH ports
   */
  private async scanSubnetsForSsh(): Promise<ScanResult[]> {
    const results: ScanResult[] = [];
    console.log("Starting subnet scan for SSH devices");

    try {
      // Scan all subnets
      for (const subnetRange of this.subnetRanges) {
        console.log(`Scanning subnet range: ${subnetRange}`);
        
        // Generate all IPs in the subnet
        const ips = this.expandSubnet(subnetRange);
        console.log(`Found ${ips.length} IPs in subnet ${subnetRange}`);
        
        // Scan each IP
        for (const ipAddress of ips) {
          try {
            // Check each IP and add to results if it has SSH open
            const deviceResult = await this.checkAndEnrichDevice(ipAddress);
            if (deviceResult) {
              results.push(deviceResult);

              return results; // TODO: stream results to support mutiple router detection
            }
          } catch (error) {
            console.error(`Error scanning ${ipAddress}:`, error);
            // Continue with next IP
          }
        }
      }
    } catch (error) {
      console.error("Error in subnet scanning:", error);
    }

    console.log(`Scan complete, found ${results.length} routers`);
    return results;
  }

  /**
   * Checks if a specific IP has port 22 (SSH) open
   */
  private checkSshPort(ipAddress: string): Promise<boolean> {
    return new Promise((resolve) => {
      const socket = new net.Socket();
      
      // Set timeout
      socket.setTimeout(this.timeoutMs);``
      
      // Handle connection events
      socket.on('connect', () => {
        socket.destroy();
        resolve(true);
      });
      
      socket.on('timeout', () => {
        socket.destroy();
        resolve(false);
      });
      
      socket.on('error', () => {
        socket.destroy();
        resolve(false);
      });
      
      // Attempt to connect to SSH port
      socket.connect(22, ipAddress);
    });
  }

  /**
   * Expands a subnet range into individual IP addresses
   */
  private expandSubnet(subnetRange: string): string[] {
    const ips: string[] = [];
    const [subnet, maskStr] = subnetRange.split('/');
    const mask = parseInt(maskStr, 10);
    
    if (isNaN(mask) || mask < 16 || mask > 30) {
      console.error(`Invalid subnet mask: ${maskStr}`);
      return [];
    }
    
    // Calculate the number of hosts in this subnet
    const numHosts = Math.pow(2, 32 - mask);
    
    // Calculate the base address
    const baseAddress = ip.toLong(subnet);
    
    // Generate all IPs in the subnet
    for (let i = 1; i < numHosts - 1; i++) {
      const ipLong = baseAddress + i;
      ips.push(ip.fromLong(ipLong));
    }
    
    return ips;
  }
  
  /**
   * Determines if a router is running OpenWrt based on its information
   */
  private isOpenwrt(routerInfo: RouterInfo | undefined): boolean {
    // No routerInfo means not OpenWrt
    if (!routerInfo) {
      return false;
    }
    
    // If we have release info and it indicates OpenWrt, that's a strong signal
    if (routerInfo.release?.distribution?.toLowerCase() === 'openwrt') {
      return true;
    }
    
    // Otherwise, having a board_name is a good indicator it might be OpenWrt
    return true;
  }
  
  /**
   * Gets OpenWrt board information
   *
   * This is a simplified version. Since we can't directly execute commands
   * (executeCommand is private in SshConnector), we use the information
   * we can get from getRouterInfo.
   */
  private async getOpenWrtBoardInfo(ipAddress: string): Promise<any> {
    try {
      // Get router information using the public method
      const routerInfo = await this.sshConnector.getRouterInfo(ipAddress);
      
      // If no info is available, return empty object
      if (!routerInfo) {
        return {};
      }
      
      // The router info from ubus call system board already has the format we want
      // Just return it directly
      return routerInfo;
    } catch (error) {
      console.error(`Error getting board info from ${ipAddress}:`, error);
      return {};
    }
  }
  
  /**
   * Gets router information using the SshConnector
   */
  private async getRouterInfo(ipAddress: string): Promise<RouterInfo | undefined> {
    try {
      return await this.sshConnector.getRouterInfo(ipAddress);
    } catch (error) {
      console.error(`Error getting router info from ${ipAddress}:`, error);
      return undefined;
    }
  }
  
  /**
   * Enriches a device with SSH information, including OpenWrt detection and board info
   * This extracts the common logic used for both gateway and subnet scanning
   */
  private async enrichDeviceWithSSHInfo(ipAddress: string): Promise<Partial<ScanResult['meta']>> {
    try {
      console.log(`Enriching device ${ipAddress} with SSH info`);
      
      // First get router info which also checks connectivity
      const routerInfo = await this.getRouterInfo(ipAddress);
      console.log(`Router info for ${ipAddress}:`, routerInfo);
      
      // If we got router info, use it to determine if it's OpenWrt and get board info
      if (routerInfo) {
        // Use our helper function to determine if it's OpenWrt
        const isOpenwrt = this.isOpenwrt(routerInfo);
        console.log(`Is ${ipAddress} an OpenWrt router?`, isOpenwrt);
        
        // For OpenWrt routers, we can use the routerInfo directly as boardInfo
        // since it's already in the format we want from 'ubus call system board'
        const boardInfo = isOpenwrt ? routerInfo : {};
        console.log(`Board info for ${ipAddress}:`, boardInfo);
        
        const result = {
          isOpenwrt,
          boardInfo,
          routerInfo
        };
        
        console.log(`Enriched device info for ${ipAddress}:`, result);
        return result;
      }
      
      console.log(`No router info available for ${ipAddress}`);
      return {};
    } catch (error) {
      console.error(`Error getting router details for ${ipAddress}:`, error);
      return {};
    }
  }
  
  /**
   * Helper method to check if a device has SSH open and enrich it with device info
   * Returns a ScanResult if SSH is open, or null if not
   */
  private async checkAndEnrichDevice(ipAddress: string): Promise<ScanResult | null> {
    try {
      console.log(`Checking SSH port for ${ipAddress}...`);
      const sshOpen = await this.checkSshPort(ipAddress);
      
      if (!sshOpen) {
        return null;
      }
      
      console.log(`Found router with SSH enabled: ${ipAddress}`);
      
      // Initialize meta with basic status
      let meta: ScanResult['meta'] = { status: 'ready' };
      
      // Enrich with additional device information
      const deviceInfo = await this.enrichDeviceWithSSHInfo(ipAddress);
      meta = { ...meta, ...deviceInfo };
      
      console.log(`Final meta object for ${ipAddress}:`, meta);
      
      return {
        ip: ipAddress,
        sshOpen: true,
        meta
      };
    } catch (error) {
      console.error(`Error checking device ${ipAddress}:`, error);
      return null;
    }
  }
}