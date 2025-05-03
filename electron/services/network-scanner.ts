import * as ip from 'ip';
import * as net from 'net';
// default-gateway is now imported dynamically to handle ESM compatibility

export interface ScanResult {
  ip: string;
  sshOpen: boolean;
  meta?: any;
}

export class NetworkScanner {
  private readonly timeoutMs: number;
  private readonly subnetRanges: string[];

  constructor(config?: { timeoutMs?: number; subnetRanges?: string[] }) {
    this.timeoutMs = config?.timeoutMs || 1000; // Default timeout: 1 second
    this.subnetRanges = config?.subnetRanges || ['192.168.0.0/24', "192.168.8.0/24", '192.168.1.0/24', '10.0.0.0/24'];
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
        const isSshOpen = await this.checkSshPort(gatewayIp);
        results.push({
          ip: gatewayIp,
          sshOpen: isSshOpen,
          meta: { isGateway: true }
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
      // Dynamically import the ES Module
      const defaultGateway = await import('default-gateway');
      // Try to get the default gateway for IPv4
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
        const scanPromise = this.checkSshPort(ipAddress).then(sshOpen => {
          if (sshOpen) {
            results.push({ ip: ipAddress, sshOpen: true });
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
}