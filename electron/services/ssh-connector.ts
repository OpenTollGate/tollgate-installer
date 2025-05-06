import { Client } from 'ssh2';

export interface LoginAttempt {
  ip: string;
  success: boolean;
  error?: string;
}

export interface RouterInfo {
  boardName: string;
  architecture: string;
  compatible: boolean;
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
   * Gets information about the connected router, including board name and architecture
   */
  public async getRouterInfo(ip: string): Promise<RouterInfo> {
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
          return {
            boardName: 'disconnected',
            architecture: 'unknown',
            compatible: false
          };
        }
        
        // Get the newly established connection
        client = this.connections.get(ip);
        if (!client) {
          throw new Error(`Connection established but client not found for ${ip}`);
        }
      }

      console.log(`Executing commands to get board info for ${ip}`);
      // Get the board name from /proc/cmdline
      const boardName = await this.executeCommand(
        client,
        "cat /tmp/sysinfo/board_name"
      );
      console.log(`Board name for ${ip}:`, boardName);

      // Get the architecture
      const architecture = await this.executeCommand(
        client,
        "uname -m"
      );
      console.log(`Architecture for ${ip}:`, architecture);

      // Determine if the router is compatible with TollGateOS
      // This is a simplification - in a real implementation, you'd check against
      // a list of supported boards and architectures
      const compatible = Boolean(boardName && architecture);
      console.log(`Router ${ip} compatible:`, compatible);

      const result = {
        boardName: boardName.trim(),
        architecture: architecture.trim(),
        compatible
      };
      console.log(`Router info result for ${ip}:`, result);
      return result;
    } catch (error) {
      console.error(`Error getting router info for ${ip}:`, error);
      return {
        boardName: 'error',
        architecture: 'unknown',
        compatible: false
      };
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
      const timeout = setTimeout(() => {
        client.removeAllListeners();
        reject(new Error(`Connection to ${ip} timed out`));
      }, this.connectionTimeout);

      client.on('ready', () => {
        clearTimeout(timeout);
        resolve();
      });

      client.on('error', (err) => {
        clearTimeout(timeout);
        reject(err);
      });

      // Try to connect with the provided configuration
      client.connect({
        host: ip,
        port: 22,
        username: this.username,
        password: password || '',
        readyTimeout: this.connectionTimeout,
        // For development or testing, this allows connecting to routers with changed host keys
        // For production, this should be replaced with proper host key verification
        algorithms: {
          serverHostKey: ['ssh-rsa', 'ecdsa-sha2-nistp256', 'ssh-ed25519']
        }
      });
    });
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