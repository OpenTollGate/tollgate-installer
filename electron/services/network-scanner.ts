import * as ip from 'ip';
import * as net from 'net';
import { SshConnector, RouterInfo } from './ssh-connector';
// default-gateway is imported dynamically in detectDefaultGateway()

export interface ScanResult {
  ip: string;
  sshOpen: boolean;
  meta?: {
    isGateway?: boolean;
    status?: string;
    isOpenwrt?: boolean;
    boardInfo?: {
      board_name?: string;
      model?: string;
      hostname?: string;
      release?: {
        distribution?: string;
        version?: string;
        revision?: string;
      }
    }
    routerInfo?: RouterInfo;
  };
}

export class NetworkScanner {
  private readonly timeoutMs: number;
  private readonly subnetRanges: string[];
  private readonly sshConnector: SshConnector;

  constructor(config?: { timeoutMs?: number; subnetRanges?: string[] }) {
    this.timeoutMs = config?.timeoutMs || 1000; // Default timeout: 1 second
    this.subnetRanges = config?.subnetRanges || ['192.168.0.0/24', "192.168.8.0/24", '192.168.1.0/24', '10.0.0.0/24']; // 192.168.0.0/16 (last resort)
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
      const gatewayIp = await this.detectDefaultGateway();
      if (gatewayIp) {
        console.log(`Default gateway detected: ${gatewayIp}`);
        const isSshOpen = await this.checkSshPort(gatewayIp);
        
        let meta: ScanResult['meta'] = {
          isGateway: true,
          status: isSshOpen ? 'ready' : 'no-ssh'
        };
        
        // If SSH is open, enrich the device information
        if (isSshOpen) {
          const deviceInfo = await this.enrichDeviceWithSSHInfo(gatewayIp);
          meta = { ...meta, ...deviceInfo };
        }
        
        results.push({
          ip: gatewayIp,
          sshOpen: isSshOpen,
          meta
        });
      }

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
   * Attempts to detect the default gateway IP
   */
  private async detectDefaultGateway(): Promise<string | null> {
    try {
      // Dynamic import using the native JavaScript import() function
      // This syntax will be preserved in the output JavaScript
      const defaultGateway = await import('default-gateway');
      
      // Access the v4 function from the imported module
      const { gateway } = await defaultGateway.v4();
      return gateway;
    } catch (error) {
      console.error('Error detecting default gateway:', error);
      return null;
    }
  }

  /**
   * Scans configured subnets for devices with open SSH ports
   */
  private async scanSubnetsForSsh(): Promise<ScanResult[]> {
    const results: ScanResult[] = [];
    const scanPromises: Promise<void>[] = [];

    for (const subnetRange of this.subnetRanges) {
      // Generate all IPs in the subnet
      const ips = this.expandSubnet(subnetRange);
      
      // Scan each IP
      for (const ipAddress of ips) {
        const scanPromise = this.checkSshPort(ipAddress).then(async sshOpen => {
          if (sshOpen) {
            console.log(`Found router with SSH enabled: ${ipAddress}`);
            
            let meta: ScanResult['meta'] = { status: 'ready' };
            
            // If SSH is open, enrich the device information
            const deviceInfo = await this.enrichDeviceWithSSHInfo(ipAddress);
            meta = { ...meta, ...deviceInfo };
            
            results.push({
              ip: ipAddress,
              sshOpen: true,
              meta
            });
          }
        }).catch(() => {
          // Ignore errors for individual IP scans
        });
        
        scanPromises.push(scanPromise);
      }
    }

    // Wait for all scan operations to complete
    await Promise.all(scanPromises);
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
   * Checks if a device is running OpenWrt
   */
  private async checkIfOpenwrt(ipAddress: string): Promise<boolean> {
    try {
      // We can indirectly check if it's OpenWrt by using getRouterInfo
      // which already connects and gets information from the device
      const routerInfo = await this.sshConnector.getRouterInfo(ipAddress);
      
      // If we successfully got board info, check if it looks like OpenWrt
      if (routerInfo && routerInfo.boardName && routerInfo.boardName !== 'error') {
        // OpenWrt typically has a board name, so this is a good indicator
        return true;
      }
      
      return false;
    } catch (error) {
      console.error(`Error checking if ${ipAddress} is OpenWrt:`, error);
      return false;
    }
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
      // Get basic router information using the public method
      const routerInfo = await this.sshConnector.getRouterInfo(ipAddress);
      
      if (!routerInfo || routerInfo.boardName === 'error') {
        return {};
      }
      
      // Convert RouterInfo to the format we want
      return {
        board_name: routerInfo.boardName,
        model: routerInfo.boardName,
        release: {
          distribution: 'OpenWrt',
          architecture: routerInfo.architecture
        }
      };
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
      // First get router info which also checks connectivity
      const routerInfo = await this.getRouterInfo(ipAddress);
      
      // If we got router info, use it to determine if it's OpenWrt and get board info
      if (routerInfo) {
        const isOpenwrt = routerInfo.boardName !== 'error' && routerInfo.boardName !== 'disconnected';
        const boardInfo = isOpenwrt ? await this.getOpenWrtBoardInfo(ipAddress) : {};
        
        return {
          isOpenwrt,
          boardInfo,
          routerInfo
        };
      }
      
      return {};
    } catch (error) {
      console.error(`Error getting router details for ${ipAddress}:`, error);
      return {};
    }
  }
}