# Install the prebuilt Windows CLI. Run with PowerShell 5.1 or later.
[CmdletBinding()]
param(
    [string]$Version = $(if ($env:SEALOS_CONSOLE_VERSION) { $env:SEALOS_CONSOLE_VERSION } else { 'v0.5.0' }),
    [string]$InstallDir = $(if ($env:SEALOS_CONSOLE_INSTALL_DIR) { $env:SEALOS_CONSOLE_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'sealos-console\bin' })
)
$ErrorActionPreference = 'Stop'
if ($Version -cnotmatch '^v[0-9]+\.[0-9]+\.[0-9]+([.-][A-Za-z0-9.]+)?$') { throw 'Invalid version.' }
if ([string]::IsNullOrWhiteSpace($InstallDir)) { throw 'Install directory is empty.' }
if ($env:OS -ne 'Windows_NT') { throw 'Use install.sh on macOS or Linux.' }
$nativeArch = $env:PROCESSOR_ARCHITECTURE
if ($env:PROCESSOR_ARCHITEW6432) { $nativeArch = $env:PROCESSOR_ARCHITEW6432 }
try { $nativeArch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString() } catch { }
switch ($nativeArch.ToLowerInvariant()) {
    { $_ -in 'amd64', 'x64' } { $arch = 'amd64' }
    'arm64' { $arch = 'arm64' }
    default { throw 'Supported architectures: amd64 and arm64.' }
}
$InstallDir = [System.IO.Path]::GetFullPath($InstallDir)
$workDir = Join-Path ([System.IO.Path]::GetTempPath()) ('sealos-console-install-' + [guid]::NewGuid())
$staged = $null
[void][System.IO.Directory]::CreateDirectory($workDir)
try {
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
    $asset = "sealos-console_${Version}_windows_${arch}.zip"
    $base = "https://github.com/raymondtc/sealos-console/releases/download/$Version"
    $archive = Join-Path $workDir $asset
    $checksums = Join-Path $workDir 'checksums.txt'
    Invoke-WebRequest -UseBasicParsing -Uri "$base/checksums.txt" -OutFile $checksums -TimeoutSec 300
    Invoke-WebRequest -UseBasicParsing -Uri "$base/$asset" -OutFile $archive -TimeoutSec 300
    $checksumRows = @(Get-Content -LiteralPath $checksums | Where-Object { ($_ -split '\s+', 2)[1] -ceq $asset })
    if ($checksumRows.Count -ne 1) { throw 'Missing or duplicate checksum entry.' }
    $expected = ($checksumRows[0] -split '\s+', 2)[0]
    if ((Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant() -cne $expected) { throw 'Checksum mismatch; existing installation was not changed.' }
    # Extract only the executable to avoid arbitrary archive paths.
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $zip = [System.IO.Compression.ZipFile]::OpenRead($archive)
    $binary = Join-Path $workDir 'sealos-console.exe'
    try {
        $entries = @($zip.Entries | Where-Object { $_.FullName -ceq 'sealos-console.exe' })
        if ($entries.Count -ne 1) { throw 'Release executable is missing or duplicated.' }
        [System.IO.Compression.ZipFileExtensions]::ExtractToFile($entries[0], $binary)
    } finally { $zip.Dispose() }
    $reported = & $binary --version
    if ($LASTEXITCODE -ne 0 -or $reported -cne $Version) { throw 'Release version mismatch.' }
    [void][System.IO.Directory]::CreateDirectory($InstallDir)
    $destination = Join-Path $InstallDir 'sealos-console.exe'
    if (Test-Path -LiteralPath $destination) {
        $item = Get-Item -LiteralPath $destination -Force
        if ($item.PSIsContainer -or ($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint)) { throw 'Destination must be a regular file.' }
    }
    $staged = Join-Path $InstallDir ('.sealos-console-' + [guid]::NewGuid() + '.exe')
    [System.IO.File]::Copy($binary, $staged)
    if ([System.IO.File]::Exists($destination)) { [System.IO.File]::Replace($staged, $destination, $null) }
    else { [System.IO.File]::Move($staged, $destination) }
    $staged = $null
    Write-Output "Installed sealos-console $Version to $destination"
    Write-Output "Add this directory to PATH if needed: $InstallDir"
    Write-Output 'Run sealos-console skill install --agent codex (or your harness ID) to install the skill. Login profiles are preserved.'
} finally {
    if ($staged -and (Test-Path -LiteralPath $staged)) { Remove-Item -LiteralPath $staged -Force }
    if (Test-Path -LiteralPath $workDir) { Remove-Item -LiteralPath $workDir -Recurse -Force }
}
