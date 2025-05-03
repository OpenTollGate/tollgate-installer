# TollGate Installer — High-Level Design Document (HLDD)

## 1. Overview

TollGate Installer is an Electron-based desktop application designed for fast, repeatable installation of TollGate OS onto fresh routers with minimal technical expertise. It uses native Node.js capabilities for network scanning, SSH connection, and installation, providing a guided, streamlined user experience via a modern desktop UI. The software is optimized for consecutive router installations, making it easy to rapidly move on to the next device after each install.

---

## 2. System Architecture

### Core Components

- **Electron Frontend UI**
  - Cross-platform desktop GUI built with web technologies, served within Electron's Chromium shell
  - Guides the user through scanning, password entry (manual or QR), installation, and subsequent device workflows
  - Provides streamlined prompts/actions to quickly begin flashing the next router after finishing one

- **Node.js-powered Network & Router Detector**
  - Runs as part of the Electron backend process
  - Uses system APIs to detect the computer's default gateway IP(s), preferring these addresses as likely router candidates for flashing
  - If no relevant gateway found or for completeness, can also scan the local subnet for devices with open SSH ports (port 22)
  - Returns candidate router IP addresses to the frontend

- **SSH Connector**
  - Attempts SSH login on detected IPs as 'root' with no password; on failure, prompts the user for a password (manual or QR)
  - Establishes secure SSH sessions used for installation

- **Installer Engine**
  - Transfers TollGateOS image to the router (using scp/SFTP)
  - Executes installation and setup scripts via the SSH session

- **Update Manager**
  - Checks and reports on new TollGateOS releases post-install
  - Manages auto-updating of the Electron application itself

---

## 3. Data Flow & User Journey

1. User launches the TollGate Installer desktop app
2. The app uses system APIs to detect the default gateway/router IP; if found, targets this address for flashing; otherwise or additionally, scans the subnet for SSH-enabled devices
3. App attempts SSH login to each target IP as 'root' with an empty password
4. If any login is successful, the install process begins; if all fail, the UI presents two options: enter password manually or scan a QR code
5. App then retries SSH with the supplied password
6. On successful connection, the app uploads and installs TollGateOS
7. User is guided through post-install steps
8. After completing one install, the UI presents a quick-start option for flashing the next router, resetting the workflow for rapid re-use
9. The app reports available updates as needed

**Note:**
- No manual IP or username entry is required from the user
- For each new router, the cycle can repeat quickly, minimizing user clicks/input between installs
- The user can supply passwords either manually or by scanning a QR code
- All credentials are handled transiently and securely

---

## 4. External Interfaces

- **Electron & Node.js APIs:** For local subnet scanning, system gateway detection (OS-specific APIs/utilities), camera access (for QR scanning)
- **SSH Protocol:** For remote shell access and file transfer
- **HTTP:** For software update checks for both TollGateOS and the Electron app

---

## 5. Security Considerations

- User credentials are never stored, only used for the session
- The scanning and authentication process respects local network scope and only attempts login with user consent
- All SSH communications are encrypted
- Electron app is sandboxed and signed for distribution
- Any QR scanning libraries are verified for privacy and security

---

## 6. Key Design Choices

- Workflow optimized for rapid, repeated installs across multiple routers
- Gateway IP used as a priority target for flashing when available, for highest likelihood of success
- Electron desktop app enables full native access to system APIs
- Password prompts support both manual and QR input
- Modular and extensible architecture