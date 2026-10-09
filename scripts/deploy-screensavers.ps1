# Stages a screen saver pool on the NAS, uploading only the videos it does not
# have yet. Screen saver videos are long-lived media, not part of a release
# (ADR 0014); root then installs the pool with install-screensavers.sh.
[CmdletBinding(SupportsShouldProcess = $true, ConfirmImpact = 'Medium')]
param(
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[A-Za-z0-9][A-Za-z0-9.-]*$')]
    [string]$NasHost,

    [Parameter(Mandatory = $true)]
    [string[]]$Video,

    [string]$IdentityFile = (Join-Path $env:USERPROFILE '.ssh\a-nas-dev_ed25519')
)

$ErrorActionPreference = 'Stop'
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$target = "anas-dev@$NasHost"
$staging = 'apps/a-nas/screensavers'

if ($Video.Count -gt 32) {
    throw 'A screen saver pool cannot contain more than 32 videos.'
}
$videos = @()
foreach ($candidate in $Video) {
    if (-not (Test-Path -LiteralPath $candidate -PathType Leaf)) {
        throw "Screen saver video does not exist: $candidate"
    }
    $resolvedPath = (Resolve-Path -LiteralPath $candidate).Path
    if ([System.IO.Path]::GetExtension($resolvedPath) -ine '.mp4') {
        throw "Screen saver video must be an MP4 file: $resolvedPath"
    }
    $hash = (Get-FileHash -LiteralPath $resolvedPath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($videos.Hash -contains $hash) {
        throw "Screen saver video was provided more than once: $resolvedPath"
    }
    $videos += [PSCustomObject]@{ Path = $resolvedPath; Hash = $hash }
}
if (-not (Test-Path -LiteralPath $IdentityFile -PathType Leaf)) {
    throw "SSH identity does not exist: $IdentityFile"
}

$summary = "stage a screen saver pool of $($videos.Count) videos, uploading only those the NAS does not have"
if (-not $PSCmdlet.ShouldProcess($target, $summary)) {
    foreach ($item in $videos) {
        Write-Host "$($item.Hash)  $($item.Path)"
    }
    Write-Host 'No network connection or remote change was performed.'
    return
}

$sshOptions = @(
    '-o', 'BatchMode=yes',
    '-o', 'IdentitiesOnly=yes',
    '-o', 'StrictHostKeyChecking=yes',
    '-i', $IdentityFile
)
$hashes = ($videos | ForEach-Object { $_.Hash }) -join ' '

& ssh.exe @sshOptions $target "install -d -m 0750 ~/$staging/objects ~/$staging/pools"
if ($LASTEXITCODE -ne 0) { throw 'Cannot create the remote staging directory.' }
& scp.exe @sshOptions (Join-Path $repoRoot 'scripts\remote-stage-screensavers.sh') "${target}:$staging/remote-stage-screensavers.sh"
if ($LASTEXITCODE -ne 0) { throw 'Cannot upload the staging script.' }

$missing = @(& ssh.exe @sshOptions $target "bash ~/$staging/remote-stage-screensavers.sh missing $hashes" |
    Where-Object { $_ -match '^[0-9a-f]{64}$' })
if ($LASTEXITCODE -ne 0) { throw 'Cannot list the videos the NAS lacks.' }
foreach ($item in $videos) {
    if ($missing -contains $item.Hash) {
        & scp.exe @sshOptions $item.Path "${target}:$staging/objects/$($item.Hash).mp4.incoming"
        if ($LASTEXITCODE -ne 0) { throw "Upload failed: $($item.Path)" }
    }
}

$pool = @(& ssh.exe @sshOptions $target "bash ~/$staging/remote-stage-screensavers.sh commit $hashes" |
    Where-Object { $_ -match '^[0-9a-f]{16}$' }) | Select-Object -Last 1
if ($LASTEXITCODE -ne 0 -or -not $pool) { throw 'Staging the screen saver pool failed.' }

Write-Host "Uploaded $($missing.Count) of $($videos.Count) videos; the NAS already had the others."
foreach ($item in $videos) {
    Write-Host "$($item.Hash)  $($item.Path)"
}
Write-Host "Screen saver pool $pool is staged on $NasHost. Install it as root:"
Write-Host "  /opt/a-nas/current/install-screensavers.sh $pool"
