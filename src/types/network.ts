import { RouterInfo } from '../../electron/services/ssh-connector';

/**
 * Result of a network scan for potential routers
 */
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
        architecture?: string;
      }
    }
    routerInfo?: RouterInfo;
  };
}