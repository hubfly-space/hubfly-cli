param([Parameter(Mandatory = $true)][string]$BinaryPath)

$ErrorActionPreference = 'Stop'
$testDir = Join-Path ([IO.Path]::GetTempPath()) ('hubfly-installer-test-' + [Guid]::NewGuid().ToString('N'))
$originalUserPath = [Environment]::GetEnvironmentVariable('Path', 'User')
$originalProcessPath = $env:Path
$installerTestCorruptChecksum = $false
New-Item -ItemType Directory -Path $testDir | Out-Null
try {
    $stage = Join-Path $testDir 'stage'
    New-Item -ItemType Directory -Path $stage | Out-Null
    Copy-Item -LiteralPath $BinaryPath -Destination (Join-Path $stage 'hubfly.exe')
    $installerTestArchive = Join-Path $testDir 'release.zip'
    Compress-Archive -LiteralPath (Join-Path $stage 'hubfly.exe') -DestinationPath $installerTestArchive

    # Supply local release fixtures so this test does not install a published CLI.
    function Invoke-WebRequest {
        param([switch]$UseBasicParsing, [string]$Uri, [string]$OutFile)
        if ($Uri.EndsWith('.sha256')) {
            $digest = (Get-FileHash -Algorithm SHA256 $installerTestArchive).Hash
            if ($installerTestCorruptChecksum) { $digest = '0' * 64 }
            Set-Content -LiteralPath $OutFile -Value "$digest  release.zip"
        } elseif ($Uri.EndsWith('.zip')) {
            Copy-Item -LiteralPath $installerTestArchive -Destination $OutFile
        } else {
            throw "Unexpected download: $Uri"
        }
    }

    $installDir = Join-Path $testDir 'install with spaces'
    & "$PSScriptRoot/../install.ps1" -InstallDir $installDir
    if (-not (Test-Path -LiteralPath (Join-Path $installDir 'hubfly.exe'))) {
        throw 'Installer did not create hubfly.exe'
    }
    if (($env:Path -split ';') -notcontains $installDir) { throw 'Installer did not update PATH' }

    $installerTestCorruptChecksum = $true
    $badInstallDir = Join-Path $testDir 'bad-checksum'
    $rejected = $false
    try {
        & "$PSScriptRoot/../install.ps1" -Version v1.2.3 -InstallDir $badInstallDir
    } catch {
        if ($_.Exception.Message -notlike '*checksum verification failed*') { throw }
        $rejected = $true
    }
    if (-not $rejected -or (Test-Path -LiteralPath $badInstallDir)) {
        throw 'Installer accepted a corrupt release archive'
    }
    Write-Host 'PowerShell installer checks passed.'
} finally {
    [Environment]::SetEnvironmentVariable('Path', $originalUserPath, 'User')
    $env:Path = $originalProcessPath
    Remove-Item -LiteralPath $testDir -Recurse -Force
}
