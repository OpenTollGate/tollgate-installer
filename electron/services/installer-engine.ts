import * as fs from 'fs';
import * as path from 'path';
import { SshConnector } from './ssh-connector';
import axios from 'axios';
import { createWriteStream } from 'fs';
import { promisify } from 'util';
import { pipeline } from 'stream';
import { tmpdir } from 'os';
import NDK, { NDKEvent } from '@nostr-dev-kit/ndk';

export interface InstallStatus {
  success: boolean;
  step: string;
  progress: number;
  error?: string;
}

export class InstallerEngine {
  private sshConnector: SshConnector;
  private streamPipeline = promisify(pipeline);
  private downloadDir: string;
  
  constructor() {
    this.sshConnector = new SshConnector();
    // Create a dedicated directory for firmware downloads
    this.downloadDir = path.join(tmpdir(), 'tollgate-firmware');
    if (!fs.existsSync(this.downloadDir)) {
      fs.mkdirSync(this.downloadDir, { recursive: true });
    }
  }
  
  /**
   * Downloads a file from a URL to a local path
   * @param url URL to download from
   * @param outputPath Path to save the file
   * @returns Promise that resolves to the path of the downloaded file
   */
  private async downloadFile(url: string, outputPath?: string): Promise<string> {
    console.log(`Downloading file from ${url}`);
    
    // Generate a filename if outputPath is not provided
    if (!outputPath) {
      const urlParts = url.split('/');
      const filename = urlParts[urlParts.length - 1];
      outputPath = path.join(this.downloadDir, filename);
    }
    
    try {
      // Create output directory if it doesn't exist
      const outputDir = path.dirname(outputPath);
      if (!fs.existsSync(outputDir)) {
        fs.mkdirSync(outputDir, { recursive: true });
      }
      
      // Download the file
      const response = await axios({
        method: 'GET',
        url: url,
        responseType: 'stream',
      });
      
      await this.streamPipeline(response.data, createWriteStream(outputPath));
      
      console.log(`File downloaded to ${outputPath}`);
      return outputPath;
    } catch (error) {
      console.error(`Error downloading file from ${url}:`, error);
      throw error;
    }
  }

  /**
   * Installs TollGateOS on the router at the specified IP
   * Assumes an SSH connection has already been established
   * @param ip IP address of the router
   * @param releaseData The serialized Nostr event containing the release information
   */
  public async install(ip: string, releaseData: any): Promise<InstallStatus> {
    // Deserialize the data back to an NDKEvent object
    const releaseEvent = NDKEvent.deserialize(new NDK(), releaseData);
    
    try {
      // Step 1: Preparation
      console.log(`Starting installation on router ${ip}`);
      await this.updateStatus('preparing', 10);
      
      // Get router information to determine compatibility
      const routerInfo = await this.sshConnector.getRouterInfo(ip);
      
      // Check if router info is available
      if (!routerInfo) {
        return {
          success: false,
          step: 'compatibility-check',
          progress: 0,
          error: 'Unable to get router information'
        };
      }
      
      // Verify compatibility based on board_name
      const isOpenwrt = routerInfo.release?.distribution?.toLowerCase() === 'openwrt';
      if (!isOpenwrt || !routerInfo.board_name) {
        return {
          success: false,
          step: 'compatibility-check',
          progress: 0,
          error: `Router is not running OpenWrt or missing board info: ${routerInfo.system || 'unknown'}`
        };
      }
      
      console.log(`Router running OpenWrt: ${routerInfo.board_name}`);
      
      // Step 2: Extract release information
      const firmwareUrl = releaseEvent.getMatchingTags('url')?.[0]?.[1];
      if (!firmwareUrl) {
        return {
          success: false,
          step: 'download-preparation',
          progress: 15,
          error: 'Firmware URL not found in release information'
        };
      }
      
      console.log(`Firmware URL: ${firmwareUrl}`);
      
      // Step 3: Download the firmware
      await this.updateStatus('downloading', 20);
      let localFirmwarePath;
      try {
        localFirmwarePath = await this.downloadFile(firmwareUrl);
      } catch (downloadError) {
        return {
          success: false,
          step: 'downloading',
          progress: 25,
          error: `Failed to download firmware: ${downloadError instanceof Error ? downloadError.message : String(downloadError)}`
        };
      }
      
      // Step 4: Transfer the firmware to the router
      await this.updateStatus('transferring', 40);
      const remoteFilePath = `/tmp/firmware-update.bin`;
      const transferSuccess = await this.sshConnector.transferFile(ip, localFirmwarePath, remoteFilePath);
      
      if (!transferSuccess) {
        return {
          success: false,
          step: 'transferring',
          progress: 50,
          error: 'Failed to transfer firmware to router'
        };
      }
      
      console.log(`Firmware transferred to router at ${remoteFilePath}`);
      
      // Step 5: Verify the transfer
      await this.updateStatus('verifying', 60);
      try {
        const verifyResult = await this.sshConnector.executeRemoteCommand(ip, `ls -l ${remoteFilePath}`);
        console.log(`Verification result: ${verifyResult}`);
      } catch (verifyError) {
        return {
          success: false,
          step: 'verifying',
          progress: 65,
          error: `Failed to verify firmware on router: ${verifyError instanceof Error ? verifyError.message : String(verifyError)}`
        };
      }
      
      // Step 6: Execute the sysupgrade command
      await this.updateStatus('installing', 70);
      try {
        console.log('Starting firmware upgrade with sysupgrade...');
        // -n flag prevents preserving settings
        await this.sshConnector.executeRemoteCommand(ip, `sysupgrade -n ${remoteFilePath}`);
      } catch (upgradeError) {
        // It's normal for the connection to drop during upgrade, so this is not necessarily an error
        console.log(`SSH connection dropped during upgrade (expected): ${upgradeError}`);
      }
      
      // Step 7: Wait for the router to come back online
      await this.updateStatus('waiting-for-reboot', 80);
      console.log('Waiting for router to reboot...');
      
      const routerReturned = await this.sshConnector.pollForAvailability(ip, 30, 5000);
      if (!routerReturned) {
        return {
          success: false,
          step: 'waiting-for-reboot',
          progress: 85,
          error: 'Router did not come back online after firmware upgrade'
        };
      }
      
      // Step 8: Verify the installation
      await this.updateStatus('verifying-installation', 90);
      try {
        // Verify that the router is running TollGate OS
        const versionInfo = await this.sshConnector.executeRemoteCommand(ip, 'cat /etc/tollgate-version 2>/dev/null || echo "Not TollGate OS"');
        
        if (versionInfo.includes('Not TollGate OS')) {
          return {
            success: false,
            step: 'verifying-installation',
            progress: 95,
            error: 'Router did not boot into TollGate OS after upgrade'
          };
        }
        
        console.log(`TollGate OS version: ${versionInfo.trim()}`);
      } catch (finalVerifyError) {
        return {
          success: false,
          step: 'verifying-installation',
          progress: 95,
          error: `Failed to verify TollGate OS installation: ${finalVerifyError instanceof Error ? finalVerifyError.message : String(finalVerifyError)}`
        };
      }
      
      // Complete!
      console.log('Installation completed successfully!');
      return {
        success: true,
        step: 'complete',
        progress: 100
      };
    } catch (error) {
      console.error(`Installation error for ${ip}:`, error);
      return {
        success: false,
        step: 'error',
        progress: 0,
        error: error instanceof Error ? error.message : 'Unknown error during installation'
      };
    }
  }

  /**
   * In a real implementation, this would:
   * 1. Find the appropriate image for the router model/system
   * 2. Download it from a repository if not locally available
   * 3. Return the path to the image file
   */
  private async getImageForRouter(board_name: string, system: string): Promise<string> {
    // This is a placeholder for the actual implementation
    // In a real app, you would have logic to determine the appropriate image
    // based on the router's board name and system
    
    // For this prototype, we'll just return a fake path
    return path.join(__dirname, '..', '..', 'images', `tollgate-${board_name}-${system}.bin`);
  }

  /**
   * Simulates a delay for a given step and updates the status
   */
  private async updateStatus(step: string, progress: number): Promise<void> {
    return new Promise((resolve) => {
      // Simulate a delay for demonstration purposes
      setTimeout(() => {
        console.log(`Install step: ${step}, progress: ${progress}%`);
        resolve();
      }, 1000); // 1 second delay per step
    });
  }

  /**
   * In a real implementation, this would:
   * 1. Upload the image to the router using SCP/SFTP
   * 2. Verify the transfer was successful
   */
  private async transferImageToRouter(ip: string, imagePath: string): Promise<boolean> {
    // This is a placeholder for the actual implementation
    console.log(`Simulating transfer of ${imagePath} to router at ${ip}`);
    
    // In a real app, you would use the SSH connection to transfer the file
    // using SCP/SFTP, something like:
    //
    // const sftp = await sshClient.sftp();
    // await sftp.fastPut(localPath, remotePath);
    
    // Simulate success
    return true;
  }

  /**
   * In a real implementation, this would:
   * 1. Execute the installation script on the router
   * 2. Monitor its output
   * 3. Return the result
   */
  private async executeInstallScript(ip: string, remotePath: string): Promise<boolean> {
    // This is a placeholder for the actual implementation
    console.log(`Simulating execution of install script for image at ${remotePath} on router ${ip}`);
    
    // In a real app, you would execute the installation command via SSH
    // and monitor its output, something like:
    //
    // const output = await sshConnector.executeCommand(
    //   `sysupgrade -n ${remotePath}`
    // );
    
    // Simulate success
    return true;
  }
}