# TollGate Installer — Low-Level Design Document (LLDD)

## 1. Module Breakdown & Interfaces

### 1.1. Electron Frontend UI (React)
- **Framework:** React (using functional components, hooks, and state management)
- **Look & Feel:** The UI must visually match the style of the TollGate captive portal (see docs/tollgate-captive-portal-ui-lookandfeel.png for reference; match brand colors, typography, form input styling, and friendly onboarding/feedback patterns as shown)
- **Responsibilities:**
  - Display introduction, minimal welcome, and guided instructions
  - Single password entry field (mask input) and QR scanning for password entry (using device camera via Electron APIs)
  - Progress feedback for scan, connect, install, finish
  - Error reporting & retry interface with friendly UI cues
  - Display list of discovered routers (allowing user to select/inspect/skip)
  - After successful install, present a "Flash next router" workflow for rapid consecutive installs
- **Primary Inputs:** Router password (manual or via QR)
- **Outputs:** User feedback, progress, error/status messages, trigger next-install cycle

### 1.2. Local Network & Gateway Scanner (Electron Backend)
- **Language/Runtime:** Node.js inside Electron
- **Algorithm:**
  - Use system APIs (cross-platform: Windows, macOS, Linux) to detect local default gateway IP(s), favoring these as likely routers for flashing
  - If not found or for fallbacks, perform subnet TCP port 22 scan (see HLDD)
  - Return responsive candidate list to React frontend via Electron IPC
- **Security:**
  - Strictly limit scan ranges to LAN/private subnets
  - Do not expose raw scan APIs to frontend—backend only

#### Example Pseudocode (JS):
```javascript
const gatewayIp = detectDefaultGateway();
let candidates = [];
if (gatewayIp) candidates.push(gatewayIp);
candidates.push(...scanSubnetForSsh());
return candidates;
```

### 1.3. SSH Connector (Electron Backend)
- **Libraries:** ssh2, node-ssh
- **Process:**
  - For each candidate, attempt SSH as 'root' with no password. On failure, prompt for password (manual or QR), then retry.
  - On first successful login, proceed. Record results and errors for UI feedback.
- **Frontend interaction:** Communicate via Electron IPC/status messages.

### 1.4 Installer Engine (Electron Backend)
- **Flow:**
  - Use SSH session for all remote commands
  - Transfer TollGateOS image with scp or SFTP
  - Verify checksum
  - Run install script, monitor outputs/errors
- **Edge Cases:** Permission, storage, arch mismatch, transfer errors; communicate actionable errors to frontend.

### 1.5 Update Manager (NDK/NOSTR-NIP94)
- **Functionality:**
  - Use NOSTR (NDK library) as the update fetch mechanism
  - Fetch NIP-94 (kind 1063) events to discover new TollGateOS releases from official relay(s)
  - Parse version and changelog/info from event contents
  - Compare fetched version to locally installed; prompt for update if needed
  - No direct HTTP fetch for updates; all update info comes from NOSTR relays
- **Security/Privacy:**
  - NOSTR relays are open; filter only events signed by recognized TollGate OS publishers
  - Nothing sent to relays unless publishing own update (optional, opt-in)
  - No telemetry or tracking

---

## 2. Data Structures

```typescript
type ScanResult = { ip: string, sshOpen: boolean, meta?: any }
type LoginAttempt = { ip: string, success: boolean, error?: string }
type InstallStatus = { step: string, progress: number, error?: string }
type Config = { subnetRanges: string[], timeoutMs: number, ssh: { username: 'root' } }
type UpdateInfo = { latest: string, installed: string, canUpgrade: boolean, nip94Event?: object }
```

---

## 3. Sequence Flows

### 3.1 Network Scan & Login
1. Electron backend detects system/default gateway using platform APIs
2. Scan gateway first, then perform subnet TCP/22 scan if needed
3. Try SSH ('root', no password), then prompt for password (manual/QR) on failure
4. On first success, proceed to install, else guide user on retry/help

### 3.2 Installation
1. Transfer TollGateOS image (scp/SFTP)
2. Run remote install script; parse results/verify
3. Report to UI, allow “Flash next router” start

### 3.3 Update Check (NDK/NOSTR-NIP94)
1. On app/init and post-install, connect to NOSTR via NDK
2. Subscribe to kind 1063/NIP-94 upgrade events published by TollGate OS maintainers
3. Compare versions and parse update info
4. Present update prompt if new version found

---

## 4. Error & Security Handling

- **Timeouts:** All network ops time-limited (configurable)
- **Secrets:** Keep credentials only in backend memory
- **User Feedback:** Friendly plain-language errors, debug details for advanced users
- **Limits:** Throttle repeated login attempts

---

## 5. Technology Notes & Configurability

- **Platform:** Electron (frontend React, backend Node.js+NDK+ssh2)
- **Styling:** All UI elements themed to visually match the captive portal (see PNG and portal site)
- **Distribution:** Signed installers for Win/macOS/Linux; updates via NOSTR NIP-94
- **Config:** CLI and config-file overrides for advanced users

---

## 6. Open Questions & Future Work

- Direct integration with captive portal for look/feel sharing/components?
- Multi-router parallel install support for deployment shops?
- QR entry expansion: support WiFi or future TollGate config as well?

---