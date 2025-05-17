import { Client, SFTPWrapper } from 'ssh2';
import * as fs from 'fs';
import * as path from 'path';
import { exec } from 'child_process';
import { promisify } from 'util';

export interface LoginAttempt {
  ip: string;
  success: boolean;
  error?: string;
}

export interface RouterReleaseInfo {
  distribution: string;
  version: string;
  revision: string;
  target?: string;
  description?: string;
}

export interface RouterInfo {
  kernel?: string;
  hostname?: string;
  system?: string;
  model?: string;
  board_name: string;
  release?: RouterReleaseInfo;
}

export class SshConnector {
  private connections: Map<string, Client> = new Map();
  private readonly username: string = 'root';
  private readonly connectionTimeout: number = 10000; // 10 seconds

  /**
   * Attempts to establish an SSH connection to the specified IP
   * First tries with no password, then with the provided password if specified
   */
  public async connect(ip: string, password?: string): Promise<LoginAttempt> {
    console.log(`Attempting to connect to ${ip} via SSH with${password ? '' : 'out'} password`);
    try {
      // Close any existing connection to this IP
      await this.closeConnection(ip);
      
      // Create a new SSH client
      const client = new Client();
      
      // Connect to the router
      console.log(`Connecting to router at ${ip}...`);
      await this.connectClient(client, ip, password);
      
      // Store the connection for later use
      this.connections.set(ip, client);
      console.log(`Successfully connected to ${ip} via SSH`);
      
      return {
        ip,
        success: true
      };
    } catch (error) {
      console.error(`SSH connection error to ${ip}:`, error);
      return {
        ip,
        success: false,
        error: error instanceof Error ? error.message : 'Unknown error'
      };
    }
  }

  /**
   * Gets information about the connected router using 'ubus call system board'
   */
  public async getRouterInfo(ip: string): Promise<RouterInfo | undefined> {
    try {
      console.log(`Getting router info for ${ip} in SSH connector`);
      let client = this.connections.get(ip);
      
      // If no connection exists, try to establish one before proceeding
      if (!client) {
        console.log(`No existing SSH connection to ${ip}, attempting to reconnect...`);
        const reconnectResult = await this.connect(ip, '');
        console.log(`Reconnect result for ${ip}:`, reconnectResult);
        
        if (!reconnectResult.success) {
          console.error(`Failed to reconnect to ${ip}: ${reconnectResult.error}`);
          // Return placeholder data but mark as incompatible
          return undefined;
        }
        
        // Get the newly established connection
        client = this.connections.get(ip);
        if (!client) {
          throw new Error(`Connection established but client not found for ${ip}`);
        }
      }

      console.log(`Executing 'ubus call system board' for ${ip}`);
      // Execute the ubus command to get board info
      const ubusOutput = await this.executeCommand(
        client,
        "ubus call system board"
      );
      console.log(`ubus output for ${ip}:`, ubusOutput);
      
      // Try to parse the JSON output
      try {
        const boardInfo = JSON.parse(ubusOutput);
        console.log(`Parsed board info for ${ip}:`, boardInfo);
        return boardInfo;
      } catch (jsonError) {
        console.error(`Error parsing ubus output for ${ip}:`, jsonError);
        // Return undefined if parsing fails
        return undefined;
      }
    } catch (error) {
      console.error(`Error getting router info for ${ip}:`, error);
      return undefined;
    }
  }

  /**
   * Closes an SSH connection to a specific IP
   */
  public async closeConnection(ip: string): Promise<void> {
    const client = this.connections.get(ip);
    if (client) {
      client.end();
      this.connections.delete(ip);
    }
  }

  /**
   * Closes all active SSH connections
   */
  public async closeAllConnections(): Promise<void> {
    for (const [ip, client] of this.connections.entries()) {
      client.end();
    }
    this.connections.clear();
  }

  /**
   * Establishes an SSH connection using the provided client
   */
  private connectClient(client: Client, ip: string, password?: string): Promise<void> {
    return new Promise((resolve, reject) => {
      // Flag to track if this client has been cleaned up
      let isCleanedUp = false;
      
      // Function to clean up resources
      const cleanupClient = () => {
        if (isCleanedUp) return;
        isCleanedUp = true;
        
        // Remove all listeners
        client.removeAllListeners();
        
        // Destroy connection if needed
        try {
          // End the client connection if it's active
          if (client) {
            client.end();
          }
        } catch (cleanupErr) {
          console.error(`Error cleaning up SSH client for ${ip}:`, cleanupErr);
        }
      };
      
      // Set connection timeout
      const timeout = setTimeout(() => {
        console.error(`SSH connection to ${ip} timed out after ${this.connectionTimeout}ms`);
        cleanupClient();
        reject(new Error(`Connection to ${ip} timed out`));
      }, this.connectionTimeout);

      // Setup success handler
      client.on('ready', () => {
        clearTimeout(timeout);
        resolve();
      });

      // Setup error handler
      client.on('error', (err) => {
        console.error(`SSH connection error to ${ip}:`, err);
        clearTimeout(timeout);
        cleanupClient();
        reject(err);
      });
      
      // Setup close handler for unexpected closures
      client.on('close', () => {
        if (!isCleanedUp) {
          console.warn(`SSH connection to ${ip} closed unexpectedly`);
          clearTimeout(timeout);
          cleanupClient();
          reject(new Error(`Connection to ${ip} closed unexpectedly`));
        }
      });

      // Try to connect with the provided configuration
      try {
        client.connect({
          host: ip,
          port: 22,
          username: this.username,
          password: password || '',
          readyTimeout: this.connectionTimeout,
          // Disable host key checking - required for routers that change keys after firmware updates
          hostHash: 'none',
          // Hash the hostname to avoid issues with known_hosts file
          hostVerifier: () => true, // Always accept host keys
          // Support multiple key algorithms
          algorithms: {
            serverHostKey: ['ssh-rsa', 'ecdsa-sha2-nistp256', 'ssh-ed25519']
          }
        });
      } catch (connectErr) {
        // Catch any immediate errors from connect()
        console.error(`Immediate error connecting to ${ip}:`, connectErr);
        clearTimeout(timeout);
        cleanupClient();
        reject(connectErr);
      }
    });
  }

  /**
   * Transfers a file to the router using scp command
   * @param ip Router IP address
   * @param localPath Path to the local file to transfer
   * @param remotePath Path on the router where the file should be placed
   * @param username Username for SSH connection (defaults to 'root')
   * @param password Optional password for SSH connection
   * @returns Promise that resolves to true if transfer was successful
   */
  public async transferFile(
    ip: string,
    localPath: string,
    remotePath: string,
    username: string = this.username,
    password?: string
  ): Promise<boolean> {
    console.log(`Transferring file from ${localPath} to ${username}@${ip}:${remotePath}`);
    
    const execPromise = promisify(exec);
    
    try {
      // First, ensure the target directory exists
      // Extract the directory from the remote path
      const remoteDir = path.dirname(remotePath);
      
      // Get an existing SSH client or establish a new connection
      let client = this.connections.get(ip);
      if (!client) {
        const connectResult = await this.connect(ip, password);
        if (!connectResult.success) {
          console.error(`Failed to connect to ${ip} for directory creation`);
          return false;
        }
        client = this.connections.get(ip);
        if (!client) {
          console.error(`Connection established but client not found for ${ip}`);
          return false;
        }
      }
      
      // Create the directory structure first using SSH
      try {
        await this.executeCommand(client, `mkdir -p ${remoteDir}`);
        console.log(`Created directory ${remoteDir} on router`);
      } catch (dirError) {
        console.error(`Error creating directory on router: ${dirError}`);
        // Continue anyway since we're assuming /tmp exists
      }
      
      // Use the native scp command for file transfer with -O for older protocol
      // The -O option uses the old SCP protocol which doesn't require sftp-server
      // Additional options to handle host key changes that occur during router firmware updates:
      // - StrictHostKeyChecking=no: Don't verify the host key against known_hosts
      // - UserKnownHostsFile=/dev/null: Don't use or update the known_hosts file
      // - LogLevel=error: Only show errors, not warnings
      let scpCommand = `scp -O -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=error "${localPath}" "${username}@${ip}:${remotePath}"`;
      
      // Add password handling if needed
      // In a real application, you'd want to use a more secure approach than passing password via env
      if (password) {
        console.log('Using password for scp transfer');
        // Use sshpass if a password is provided (might need to be installed)
        scpCommand = `sshpass -p "${password}" ${scpCommand}`;
      }
      
      console.log(`Executing SCP command (password hidden): ${scpCommand.replace(password || '', '****')}`);
      const { stdout, stderr } = await execPromise(scpCommand);
      
      if (stderr && !stderr.includes('Warning')) {
        console.error(`SCP error: ${stderr}`);
        return false;
      }
      
      console.log(`SCP output: ${stdout}`);
      return true;
    } catch (error) {
      console.error(`Error transferring file to ${ip}:`, error);
      return false;
    }
  }
  
  /**
   * Quick connection attempt with a short timeout
   * This is suitable for checking if a router is back online after reboot
   */
  private async quickConnectAttempt(ip: string, timeoutMs: number = 1000): Promise<LoginAttempt> {
    console.log(`Quick connect attempt to ${ip} with ${timeoutMs}ms timeout`);
    try {
      // Close any existing connection
      await this.closeConnection(ip);
      
      // Create a new client
      const client = new Client();
      
      // Custom connection promise with short timeout
      const connectPromise = new Promise<void>((resolve, reject) => {
        // Flag to track if this client has been cleaned up
        let isCleanedUp = false;
        
        // Function to clean up resources
        const cleanupClient = () => {
          if (isCleanedUp) return;
          isCleanedUp = true;
          
          // Remove all listeners
          client.removeAllListeners();
          
          // Destroy connection if needed
          try {
            if (client) {
              client.end();
            }
          } catch (cleanupErr) {
            console.error(`Error cleaning up SSH client for ${ip}:`, cleanupErr);
          }
        };
        
        // Set a short connection timeout
        const timeout = setTimeout(() => {
          console.log(`Quick connection attempt to ${ip} timed out after ${timeoutMs}ms`);
          cleanupClient();
          reject(new Error(`Quick connection timed out`));
        }, timeoutMs);
        
        // Setup handlers
        client.once('ready', () => {
          clearTimeout(timeout);
          resolve();
        });
        
        client.once('error', (err) => {
          clearTimeout(timeout);
          cleanupClient();
          reject(err);
        });
        
        client.once('close', () => {
          if (!isCleanedUp) {
            clearTimeout(timeout);
            cleanupClient();
            reject(new Error('Connection closed'));
          }
        });
        
        // Try to connect with minimal options and short timeout
        try {
          client.connect({
            host: ip,
            port: 22,
            username: this.username,
            password: '',
            readyTimeout: timeoutMs,
            hostHash: 'none',
            hostVerifier: () => true,
            algorithms: {
              serverHostKey: ['ssh-rsa', 'ecdsa-sha2-nistp256', 'ssh-ed25519']
            }
          });
        } catch (connectErr) {
          clearTimeout(timeout);
          cleanupClient();
          reject(connectErr);
        }
      });
      
      // Try to connect with the short timeout
      await connectPromise;
      
      // If we got here, the connection was successful
      // Store it for future use
      this.connections.set(ip, client);
      
      return {
        ip,
        success: true
      };
    } catch (error) {
      // Log but don't show stack trace for timeouts - they're expected during polling
      if (error instanceof Error && error.message.includes('timed out')) {
        console.log(`Quick connect to ${ip} timed out`);
      } else {
        console.error(`Quick connect error to ${ip}:`, error);
      }
      
      return {
        ip,
        success: false,
        error: error instanceof Error ? error.message : 'Unknown error'
      };
    }
  }

  /**
   * Polls the router's SSH port until it becomes available
   * This is used after a firmware update when the router reboots
   * @param ip Router IP address
   * @param maxAttempts Maximum number of reconnection attempts
   * @param interval Interval between attempts in milliseconds
   */
  public async pollForAvailability(ip: string, maxAttempts: number = 30, interval: number = 5000): Promise<boolean> {
    console.log(`Polling ${ip} for availability, max attempts: ${maxAttempts}, interval: ${interval}ms`);
    
    // Close any existing connection first
    await this.closeConnection(ip);
    
    for (let attempt = 1; attempt <= maxAttempts; attempt++) {
      // Track if we need to try again
      let shouldRetry = true;
      
      try {
        console.log(`Poll attempt ${attempt}/${maxAttempts} for ${ip}`);
        
        // Use quick connect with 1000ms timeout to check if router is responding
        // This prevents long hang times when the router is still rebooting
        const result = await this.quickConnectAttempt(ip, 1000);
        
        if (result.success) {
          console.log(`Successfully reconnected to ${ip} after ${attempt} attempts`);
          return true;
        } else {
          // Not a thrown error, but still failed
          console.log(`Connection attempt ${attempt} failed: ${result.error || 'Unknown reason'}`);
        }
      } catch (error) {
        // Explicitly handle any uncaught errors from the connect method
        console.error(`Exception during poll attempt ${attempt} for ${ip}:`, error);
        
        // Ensure any remaining connection resources are cleaned up
        try {
          await this.closeConnection(ip);
        } catch (cleanupError) {
          console.error(`Error cleaning up connection during polling:`, cleanupError);
        }
      }
      
      // Wait before next attempt
      if (attempt < maxAttempts && shouldRetry) {
        console.log(`Waiting ${interval}ms before next connection attempt to ${ip}`);
        await new Promise(resolve => setTimeout(resolve, interval));
      }
    }
    
    console.error(`Failed to reconnect to ${ip} after ${maxAttempts} attempts`);
    return false;
  }
  
  /**
   * Makes the executeCommand method public so it can be used by the InstallerEngine
   */
  public async executeRemoteCommand(ip: string, command: string): Promise<string> {
    let client = this.connections.get(ip);
    
    if (!client) {
      console.log(`No existing SSH connection to ${ip}, attempting to reconnect...`);
      const reconnectResult = await this.connect(ip, '');
      
      if (!reconnectResult.success) {
        throw new Error(`Failed to reconnect to ${ip}: ${reconnectResult.error}`);
      }
      
      client = this.connections.get(ip);
      if (!client) {
        throw new Error(`Connection established but client not found for ${ip}`);
      }
    }
    
    return this.executeCommand(client, command);
  }

  /**
   * Executes a command on the remote device via SSH
   */
  private executeCommand(client: Client, command: string): Promise<string> {
    console.log(`Executing SSH command: ${command}`);
    return new Promise((resolve, reject) => {
      client.exec(command, (err, stream) => {
        if (err) {
          console.error(`SSH exec error for command "${command}":`, err);
          return reject(err);
        }

        let output = '';
        stream.on('data', (data: Buffer) => {
          const dataStr = data.toString();
          console.log(`SSH stdout: ${dataStr}`);
          output += dataStr;
        });

        stream.stderr.on('data', (data: Buffer) => {
          console.error(`SSH stderr: ${data.toString()}`);
        });

        stream.on('close', () => {
          console.log(`SSH command complete: "${command}", output: "${output}"`);
          resolve(output);
        });

        stream.on('error', (err: Error) => {
          reject(err);
        });
      });
    });
  }
}