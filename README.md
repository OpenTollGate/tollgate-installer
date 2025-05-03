# TollGate Installer

Easily install TollGate OS on your fresh router with just a few clicks using this desktop app.

## Overview

TollGate Installer is an Electron-based desktop application designed for fast, repeatable installation of TollGate OS onto fresh routers with minimal technical expertise. It uses native Node.js capabilities for network scanning, SSH connection, and installation, providing a guided, streamlined user experience via a modern desktop UI.

## Features

- **Automated Router Detection**: Automatically scans your network for routers
- **One-Click Installation**: Install TollGate OS with minimal user interaction
- **Secure SSH Connection**: Establishes secure connections to routers
- **Cross-Platform**: Works on Windows, macOS, and Linux
- **NOSTR Integration**: Uses NOSTR (NDK library) to check for updates via NIP-94
- **Optimized for Consecutive Installations**: Quickly install on multiple routers

## Requirements

- Node.js 16+
- npm or yarn
- Electron
- Router with SSH access

## Development

### Setup

```bash
# Clone the repository
git clone https://github.com/yourusername/tollgate-installer.git
cd tollgate-installer

# Install dependencies
npm install

# Start the development server
npm run dev
```

### Build

```bash
# Build for the current platform
npm run build

# Create distributable packages
npm run dist
```

## Project Structure

```
tollgate-installer/
├── electron/             # Electron main process files
│   ├── main.ts           # Main entry point
│   ├── preload.ts        # Preload script for contextBridge
│   └── services/         # Backend services
│       ├── network-scanner.ts    # Network scanning service
│       ├── ssh-connector.ts      # SSH connection service
│       ├── installer-engine.ts   # Installation service
│       └── update-manager.ts     # NOSTR-based update manager
├── src/                  # Frontend React application
│   ├── components/       # React components
│   ├── styles/           # Global styles
│   ├── App.tsx           # Main React component
│   └── index.tsx         # React entry point
├── dist/                 # Build output
├── package.json          # Project configuration
└── README.md             # Project documentation
```

## How It Works

1. **Detection**: The app scans your network for devices with open SSH ports
2. **Authentication**: Connect to the router using SSH (password entry or QR code)
3. **Installation**: TollGateOS is transferred and installed on the router
4. **Verification**: The app confirms successful installation
5. **Updates**: The app checks for updates to TollGateOS using NOSTR

## License

[MIT License](LICENSE)