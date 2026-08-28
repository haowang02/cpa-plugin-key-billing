$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

$repository = "haowang02/cpa-plugin-key-billing"
$pluginName = "cpa-key-billing"
$pluginFile = "$pluginName.dll"
$pluginDir = Join-Path (Get-Location).Path "plugins"
$tempDir = $null
$stagedFile = $null

function Fail {
    param([string]$Message)
    throw "${pluginName}: $Message"
}

function Download-File {
    param(
        [string]$Uri,
        [string]$OutFile
    )
    foreach ($attempt in 1..3) {
        try {
            Invoke-WebRequest -UseBasicParsing -Uri $Uri -OutFile $OutFile
            return
        }
        catch {
            if ($attempt -eq 3) {
                Fail "failed to download: $Uri"
            }
            Start-Sleep -Seconds $attempt
        }
    }
}

function Get-LatestRelease {
    $uri = "https://api.github.com/repos/${repository}/releases/latest"
    foreach ($attempt in 1..3) {
        try {
            $release = Invoke-RestMethod -UseBasicParsing -Uri $uri
            $tag = [string]$release.tag_name
            if ($tag -notmatch '^v([0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?)$') {
                Fail "latest release has an invalid tag: $tag"
            }
            return [pscustomobject]@{
                Tag = $tag
                Version = $Matches[1]
            }
        }
        catch {
            if ($attempt -eq 3) {
                Fail "failed to resolve the latest release"
            }
            Start-Sleep -Seconds $attempt
        }
    }
}

try {
    if ($env:OS -ne "Windows_NT") {
        Fail "this installer only supports Windows"
    }
    $architecture = $env:PROCESSOR_ARCHITEW6432
    if ([string]::IsNullOrWhiteSpace($architecture)) {
        $architecture = $env:PROCESSOR_ARCHITECTURE
    }
    if ([string]::IsNullOrWhiteSpace($architecture)) {
        Fail "unable to detect the processor architecture"
    }
    switch ($architecture.ToUpperInvariant()) {
        "AMD64" { $targetArch = "amd64" }
        "ARM64" { $targetArch = "arm64" }
        default { Fail "unsupported architecture: $architecture" }
    }

    $latestRelease = Get-LatestRelease
    $asset = "${pluginName}_$($latestRelease.Version)_windows_${targetArch}.zip"
    $downloadUrl = "https://github.com/${repository}/releases/download/$($latestRelease.Tag)/${asset}"
    $checksumsUrl = "https://github.com/${repository}/releases/download/$($latestRelease.Tag)/checksums.txt"

    $tempDir = Join-Path ([IO.Path]::GetTempPath()) ("${pluginName}." + [guid]::NewGuid().ToString("N"))
    New-Item -ItemType Directory -Path $tempDir | Out-Null
    $archive = Join-Path $tempDir $asset
    $checksums = Join-Path $tempDir "checksums.txt"

    Write-Host "Downloading $asset..."
    Download-File -Uri $downloadUrl -OutFile $archive
    Download-File -Uri $checksumsUrl -OutFile $checksums

    $assetPattern = [regex]::Escape($asset)
    $expectedChecksum = $null
    foreach ($line in Get-Content -Path $checksums) {
        if ($line -match "^([0-9a-fA-F]{64})\s+\*?${assetPattern}$") {
            $expectedChecksum = $Matches[1].ToLowerInvariant()
            break
        }
    }
    if (-not $expectedChecksum) {
        Fail "checksums file does not contain $asset"
    }
    $actualChecksum = (Get-FileHash -Algorithm SHA256 -Path $archive).Hash.ToLowerInvariant()
    if ($actualChecksum -ne $expectedChecksum) {
        Fail "download checksum mismatch"
    }

    try {
        Expand-Archive -LiteralPath $archive -DestinationPath $tempDir -Force
    }
    catch {
        Fail "failed to extract $asset"
    }
    $extractedFile = Join-Path $tempDir $pluginFile
    if (-not (Test-Path -LiteralPath $extractedFile) -or (Get-Item -LiteralPath $extractedFile).Length -eq 0) {
        Fail "release archive contains a missing or empty $pluginFile"
    }

    New-Item -ItemType Directory -Force -Path $pluginDir | Out-Null
    $stagedFile = Join-Path $pluginDir ".${pluginFile}.tmp.$PID"
    Copy-Item -LiteralPath $extractedFile -Destination $stagedFile
    Move-Item -Force -LiteralPath $stagedFile -Destination (Join-Path $pluginDir $pluginFile)
    $stagedFile = $null

    Write-Host "Installed: $(Join-Path $pluginDir $pluginFile)"
    Write-Host "Restart CLIProxyAPI to load the plugin."
}
catch {
    Write-Error $_.Exception.Message
    exit 1
}
finally {
    if ($stagedFile -and (Test-Path -LiteralPath $stagedFile)) {
        Remove-Item -Force -LiteralPath $stagedFile
    }
    if ($tempDir -and (Test-Path -LiteralPath $tempDir)) {
        Remove-Item -Recurse -Force -LiteralPath $tempDir
    }
}
