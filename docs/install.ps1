param(
    [string]$InstallDir = (Join-Path $env:LOCALAPPDATA 'Programs\send-cli'),
    [switch]$SkipPath
)

$ErrorActionPreference = 'Stop'

if (-not $IsWindows -and $PSVersionTable.PSEdition -eq 'Core') {
    throw 'This installer is for Windows. Use install.sh on macOS, Linux, or Termux.'
}

try {
    $release = Invoke-RestMethod -Uri 'https://api.github.com/repos/binbard/send-cli/releases/latest' -Headers @{
        Accept = 'application/vnd.github+json'
        'User-Agent' = 'send-cli-installer'
    }
} catch {
    throw 'Could not find a published send release. Try again after the first versioned release.'
}

$tag = [string]$release.tag_name
if ($tag -cnotmatch '^v[0-9][A-Za-z0-9.+-]*$') {
    throw 'The latest release has an invalid version tag.'
}
$version = $tag.Substring(1)

try {
    $osArch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
} catch {
    $osArch = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
}
switch ($osArch.ToUpperInvariant()) {
    'X64' { $arch = 'amd64' }
    'AMD64' { $arch = 'amd64' }
    'ARM64' { $arch = 'arm64' }
    default { throw "Unsupported Windows architecture: $osArch" }
}

$archiveName = "send-$version-windows-$arch.zip"
$baseUrl = "https://github.com/binbard/send-cli/releases/download/$tag"
$installDir = [System.IO.Path]::GetFullPath($InstallDir)
$binary = Join-Path $installDir 'send.exe'
$marker = Join-Path $installDir 'installer-state.json'
$state = $null
if (Test-Path -LiteralPath $marker -PathType Leaf) {
    $state = Get-Content -LiteralPath $marker -Raw | ConvertFrom-Json
    if ($state.Installer -cne 'send-cli') { throw 'Invalid installation marker.' }
}
if ((Test-Path -LiteralPath $binary) -and -not $state) {
    throw "$binary already exists and was not installed by this script."
}

$tempDir = Join-Path ([System.IO.Path]::GetTempPath()) ("send-install-" + [guid]::NewGuid().ToString('N'))
$archivePath = Join-Path $tempDir $archiveName
$checksumsPath = Join-Path $tempDir 'checksums.txt'
$extractDir = Join-Path $tempDir 'extracted'
$extractedBinary = Join-Path $extractDir 'send.exe'
$stagedBinary = Join-Path $installDir 'send.exe.new'
$backupBinary = Join-Path $installDir ("send.exe.backup-" + [guid]::NewGuid().ToString('N'))

try {
    New-Item -ItemType Directory -Path $tempDir | Out-Null
    Invoke-WebRequest -Uri "$baseUrl/$archiveName" -OutFile $archivePath -UseBasicParsing
    Invoke-WebRequest -Uri "$baseUrl/checksums.txt" -OutFile $checksumsPath -UseBasicParsing

    $expected = $null
    foreach ($line in Get-Content -LiteralPath $checksumsPath) {
        if ($line -match '^([a-fA-F0-9]{64})\s+\*?(.+)$' -and $Matches[2] -ceq $archiveName) {
            if ($expected) { throw "Duplicate checksum for $archiveName." }
            $expected = $Matches[1].ToLowerInvariant()
        }
    }
    if (-not $expected) { throw "Missing checksum for $archiveName." }
    $actual = (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actual -cne $expected) { throw "Checksum mismatch for $archiveName." }

    Add-Type -AssemblyName System.IO.Compression.FileSystem -ErrorAction SilentlyContinue
    $zip = [System.IO.Compression.ZipFile]::OpenRead($archivePath)
    try {
        if ($zip.Entries.Count -ne 1 -or $zip.Entries[0].FullName -cne 'send.exe') {
            throw 'The release archive must contain only send.exe.'
        }
    } finally {
        $zip.Dispose()
    }
    Expand-Archive -LiteralPath $archivePath -DestinationPath $extractDir

    New-Item -ItemType Directory -Path $installDir -Force | Out-Null
    Copy-Item -LiteralPath $extractedBinary -Destination $stagedBinary -Force
    if (Test-Path -LiteralPath $binary) {
        [System.IO.File]::Replace($stagedBinary, $binary, $backupBinary)
        Remove-Item -LiteralPath $backupBinary -Force
    } else {
        [System.IO.File]::Move($stagedBinary, $binary)
    }

    $pathAdded = if ($state) { [bool]$state.PathAdded } else { $false }
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    $pathEntries = @($userPath -split ';' | Where-Object { $_ -ne '' })
    $alreadyOnPath = @($pathEntries | Where-Object { $_.TrimEnd('\') -ieq $installDir.TrimEnd('\') }).Count -gt 0
    if (-not $alreadyOnPath -and -not $SkipPath) {
        $updatedPath = (@($pathEntries) + $installDir) -join ';'
        [Environment]::SetEnvironmentVariable('Path', $updatedPath, 'User')
        $pathAdded = $true
        Write-Host 'Added send to your user PATH. Open a new terminal to use it.'
    }
    @{ Installer = 'send-cli'; PathAdded = $pathAdded } | ConvertTo-Json -Compress | Set-Content -LiteralPath $marker -Encoding UTF8
    Write-Host "Installed send $version to $binary"
    Write-Host 'Run this script again to update.'
} finally {
    if (Test-Path -LiteralPath $stagedBinary) { Remove-Item -LiteralPath $stagedBinary -Force }
    if (Test-Path -LiteralPath $archivePath) { Remove-Item -LiteralPath $archivePath -Force }
    if (Test-Path -LiteralPath $checksumsPath) { Remove-Item -LiteralPath $checksumsPath -Force }
    if (Test-Path -LiteralPath $extractedBinary) { Remove-Item -LiteralPath $extractedBinary -Force }
    if (Test-Path -LiteralPath $extractDir) { Remove-Item -LiteralPath $extractDir -Force }
    if (Test-Path -LiteralPath $tempDir) { Remove-Item -LiteralPath $tempDir -Force }
}
