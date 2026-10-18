param([string]$InstallDir = (Join-Path $env:LOCALAPPDATA 'Programs\send-cli'))

$ErrorActionPreference = 'Stop'

if (-not $IsWindows -and $PSVersionTable.PSEdition -eq 'Core') {
    throw 'This uninstaller is for Windows. Use uninstall.sh on macOS, Linux, or Termux.'
}

$installDir = [System.IO.Path]::GetFullPath($InstallDir)
$binary = Join-Path $installDir 'send.exe'
$marker = Join-Path $installDir 'installer-state.json'
if (-not (Test-Path -LiteralPath $marker -PathType Leaf)) {
    throw 'No script-managed installation was found.'
}
$state = Get-Content -LiteralPath $marker -Raw | ConvertFrom-Json
if ($state.Installer -cne 'send-cli') { throw 'Invalid installation marker.' }

if (Test-Path -LiteralPath $binary) { Remove-Item -LiteralPath $binary -Force }
if ([bool]$state.PathAdded) {
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    $entries = @($userPath -split ';' | Where-Object { $_ -ne '' -and $_.TrimEnd('\') -ine $installDir.TrimEnd('\') })
    [Environment]::SetEnvironmentVariable('Path', ($entries -join ';'), 'User')
    Write-Host 'Removed send from your user PATH. Open a new terminal to refresh it.'
}
Remove-Item -LiteralPath $marker -Force
if (-not @(Get-ChildItem -LiteralPath $installDir -Force).Count) {
    Remove-Item -LiteralPath $installDir -Force
}
Write-Host "Uninstalled send from $binary. Provider settings were kept."
