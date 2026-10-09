# Launcher host support

The launcher downloads a release binary and starts the local web UI on port
8099. `curl | bash` requires a POSIX shell; it is **not** a native Windows
command. Use the entry point below for the host you are actually running on.

| Host target | One-liner / entry point | Published asset |
| --- | --- | --- |
| Linux x86_64 | `curl -fsSL https://raw.githubusercontent.com/OpenTollGate/tollgate-installer/main/install-and-test.sh | bash` | `linux-amd64` |
| Linux aarch64 | same `curl | bash` command | `linux-arm64` |
| macOS Intel | same `curl | bash` command | `darwin-amd64` |
| macOS Apple Silicon | same `curl | bash` command | `darwin-arm64` |
| Windows amd64 | `powershell -ExecutionPolicy Bypass -File .\\install-and-test.ps1` | `windows-amd64.exe` |

On Windows, Git Bash (included with Git for Windows), MSYS2, or Cygwin provide
the bash environment needed for the shell one-liner; stock Windows does not.
Native Windows should use `install-and-test.ps1`. The PowerShell script accepts
`RouterIp RouterPassword LightningAddress` for headless mode; with no arguments
it starts the interactive UI.

WSL2 is Linux, but its virtual network is NAT'd. Automatic LAN/ARP router
discovery will likely find nothing there; use the explicit-router-IP headless
form instead. The same shell launcher also recognizes Git Bash/MSYS/Cygwin
`uname` values and selects the `.exe` asset.