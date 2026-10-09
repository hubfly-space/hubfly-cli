param(
    [string]$Version = 'latest',
    [string]$InstallDir = (Join-Path $env:LOCALAPPDATA 'HubFly\bin')
)

$ErrorActionPreference = 'Stop'
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

# Use the OS architecture, including when launched from a 32-bit shell.
$machineArch = $env:PROCESSOR_ARCHITEW6432
if (-not $machineArch) { $machineArch = $env:PROCESSOR_ARCHITECTURE }
switch ($machineArch.ToUpperInvariant()) {
    'AMD64' { $arch = 'amd64' }
    'ARM64' { $arch = 'arm64' }
    default { throw "Unsupported Windows architecture: $machineArch" }
}

$releaseBase = 'https://github.com/hubfly-space/hubfly-cli/releases'
if ($Version -eq 'latest') {
    $downloadBase = "$releaseBase/latest/download"
} else {
    if ($Version -notmatch '^v?\d+\.\d+\.\d+(?:-[A-Za-z0-9.-]+)?(?:\+[A-Za-z0-9.-]+)?$') {
        throw "Invalid release version: $Version"
    }
    if (-not $Version.StartsWith('v')) { $Version = "v$Version" }
    $downloadBase = "$releaseBase/download/$Version"
}

$asset = "hubfly_windows_$arch.zip"
$tempDir = Join-Path ([IO.Path]::GetTempPath()) ('hubfly-install-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tempDir | Out-Null
try {
    $archive = Join-Path $tempDir $asset
    $checksumFile = "$archive.sha256"
    Write-Host "Downloading $asset..."
    Invoke-WebRequest -UseBasicParsing -Uri "$downloadBase/$asset" -OutFile $archive
    Invoke-WebRequest -UseBasicParsing -Uri "$downloadBase/$asset.sha256" -OutFile $checksumFile
    $expected = ((Get-Content -Raw $checksumFile).Trim() -split '\s+')[0]
    $actual = (Get-FileHash -Algorithm SHA256 $archive).Hash
    if ($expected -notmatch '^[a-fA-F0-9]{64}$' -or $actual -ne $expected) {
        throw 'Release archive checksum verification failed.'
    }

    Expand-Archive -LiteralPath $archive -DestinationPath $tempDir
    $binary = Join-Path $tempDir 'hubfly.exe'
    if (-not (Test-Path -LiteralPath $binary -PathType Leaf)) {
        throw 'hubfly.exe was not found in the release archive.'
    }
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    $InstallDir = (Resolve-Path -LiteralPath $InstallDir).Path
    $target = Join-Path $InstallDir 'hubfly.exe'
    Copy-Item -LiteralPath $binary -Destination $target -Force

    $userPath = [string][Environment]::GetEnvironmentVariable('Path', 'User')
    if (($userPath -split ';') -notcontains $InstallDir) {
        $newPath = ($userPath.TrimEnd(';') + ';' + $InstallDir).TrimStart(';')
        [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
    }
    if (($env:Path -split ';') -notcontains $InstallDir) {
        $env:Path = $env:Path.TrimEnd(';') + ';' + $InstallDir
    }
    Write-Host "Installed hubfly to $target"
    & $target version
    if ($LASTEXITCODE -ne 0) { throw 'The installed CLI did not run successfully.' }
} finally {
    Remove-Item -LiteralPath $tempDir -Recurse -Force
}
