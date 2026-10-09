[CmdletBinding()]
param(
    [Parameter(Position=0)] [string] $RouterIp,
    [Parameter(Position=1)] [string] $RouterPassword,
    [Parameter(Position=2)] [string] $LightningAddress,
    [string] $ReleaseTag = "latest",
    [string] $ReleaseRepo = "OpenTollGate/tollgate-installer"
)

$ErrorActionPreference = "Stop"
$arch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
if ($arch -notin @("X64", "Amd64")) {
    throw "Unsupported Windows architecture '$arch'; the published Windows asset is amd64 only."
}
$asset = "tollgate-installer-windows-amd64.exe"
$base = "https://github.com/$ReleaseRepo/releases/$ReleaseTag/download"
$dir = Join-Path ([System.IO.Path]::GetTempPath()) "tollgate-installer"
New-Item -ItemType Directory -Force -Path $dir | Out-Null
$exe = Join-Path $dir $asset
$sums = Join-Path $dir "SHA256SUMS"

Invoke-WebRequest -Uri "$base/$asset" -OutFile $exe
Invoke-WebRequest -Uri "$base/SHA256SUMS" -OutFile $sums
$expectedLine = Get-Content $sums | Where-Object { $_ -match "(?:^|\s)[*]?$( [regex]::Escape($asset) )$" } | Select-Object -First 1
if (-not $expectedLine) { throw "SHA256SUMS does not contain $asset" }
$expected = ($expectedLine -split "\s+")[0].ToLowerInvariant()
$actual = (Get-FileHash -Algorithm SHA256 -Path $exe).Hash.ToLowerInvariant()
if ($actual -ne $expected) { throw "SHA256 mismatch for ${asset}: expected $expected, got $actual" }

$args = @()
if ($RouterIp -or $RouterPassword -or $LightningAddress) {
    if (-not ($RouterIp -and $RouterPassword -and $LightningAddress)) {
        throw "Headless mode requires RouterIp, RouterPassword, and LightningAddress."
    }
    $args = @($RouterIp, $RouterPassword, $LightningAddress)
}
& $exe @args
exit $LASTEXITCODE