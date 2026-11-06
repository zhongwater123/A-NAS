# Stages the companion's animations on the NAS, uploading only the files it does
# not have yet. Like the screen saver videos they are long-lived media, not part
# of a release (ADR 0014); root then installs the pool with
# install-companion-animations.sh. The animations come from a dsh-pet checkout at
# the commit scripts/vendor-dsh-pet.py pins and, for Safari, from the assets-mov zip it
# pins, and must match the hashes in web/src/companion/dsh-pet/animations.sha256.
[CmdletBinding(SupportsShouldProcess = $true, ConfirmImpact = 'Medium')]
param(
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[A-Za-z0-9][A-Za-z0-9.-]*$')]
    [string]$NasHost,

    [Parameter(Mandatory = $true)]
    [string]$DshPet,

    [Parameter(Mandatory = $true)]
    [string]$MovZip,

    [string]$IdentityFile = (Join-Path $env:USERPROFILE '.ssh\a-nas-dev_ed25519')
)

$ErrorActionPreference = 'Stop'
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$manifestPath = Join-Path $repoRoot 'web\src\companion\dsh-pet\animations.sha256'
$target = "anas-dev@$NasHost"
$staging = 'apps/a-nas/companion'

# The HEVC animations are checked one by one against the manifest, from a private copy.
$movDir = Join-Path ([System.IO.Path]::GetTempPath()) ('dsh-pet-mov-' + [guid]::NewGuid().ToString('N'))
# A private copy, not a change on the NAS: made and removed even under -WhatIf.
Expand-Archive -LiteralPath $MovZip -DestinationPath $movDir -WhatIf:$false
try {

# The manifest goes to the NAS as is, where a CR would end up in every name.
if ([System.IO.File]::ReadAllText($manifestPath).Contains("`r")) {
    throw "Manifest has CRLF line endings; check it out again so .gitattributes applies: $manifestPath"
}
$animations = @()
foreach ($line in (Get-Content -LiteralPath $manifestPath -Encoding UTF8)) {
    if ($line -notmatch '^([0-9a-f]{64})  (.+)$') {
        throw "Malformed manifest line: $line"
    }
    $path = if ($Matches[2].EndsWith('.mov')) { Join-Path $movDir $Matches[2] } else { Join-Path $DshPet (Join-Path 'dsh-pet\assets\webm' $Matches[2]) }
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
        throw "Animation does not exist: $path"
    }
    $hash = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($hash -ne $Matches[1]) {
        throw "Animation does not match the pinned manifest: $path"
    }
    $animations += [PSCustomObject]@{ Path = $path; Hash = $hash }
}
if (-not (Test-Path -LiteralPath $IdentityFile -PathType Leaf)) {
    throw "SSH identity does not exist: $IdentityFile"
}

$summary = "stage the companion's $($animations.Count) animations, uploading only those the NAS does not have"
if (-not $PSCmdlet.ShouldProcess($target, $summary)) {
    Write-Host "All $($animations.Count) animations match $manifestPath."
    Write-Host 'No network connection or remote change was performed.'
    return
}

$sshOptions = @(
    '-o', 'BatchMode=yes',
    '-o', 'IdentitiesOnly=yes',
    '-o', 'StrictHostKeyChecking=yes',
    '-i', $IdentityFile
)

& ssh.exe @sshOptions $target "install -d -m 0750 ~/$staging/objects ~/$staging/pools"
if ($LASTEXITCODE -ne 0) { throw 'Cannot create the remote staging directory.' }
& scp.exe @sshOptions (Join-Path $repoRoot 'scripts\remote-stage-companion-animations.sh') "${target}:$staging/remote-stage-companion-animations.sh"
if ($LASTEXITCODE -ne 0) { throw 'Cannot upload the staging script.' }
& scp.exe @sshOptions $manifestPath "${target}:$staging/pools/.incoming.sums"
if ($LASTEXITCODE -ne 0) { throw 'Cannot upload the manifest.' }

$missing = @(& ssh.exe @sshOptions $target "bash ~/$staging/remote-stage-companion-animations.sh missing" |
    Where-Object { $_ -match '^[0-9a-f]{64}$' })
if ($LASTEXITCODE -ne 0) { throw 'Cannot list the animations the NAS lacks.' }
$uploaded = @()
foreach ($item in $animations) {
    if ($missing -contains $item.Hash -and $uploaded -notcontains $item.Hash) {
        & scp.exe @sshOptions $item.Path "${target}:$staging/objects/$($item.Hash).incoming"
        if ($LASTEXITCODE -ne 0) { throw "Upload failed: $($item.Path)" }
        $uploaded += $item.Hash
    }
}

$pool = @(& ssh.exe @sshOptions $target "bash ~/$staging/remote-stage-companion-animations.sh commit" |
    Where-Object { $_ -match '^[0-9a-f]{16}$' }) | Select-Object -Last 1
if ($LASTEXITCODE -ne 0 -or -not $pool) { throw 'Staging the companion animations failed.' }

Write-Host "Uploaded $($uploaded.Count) of $($animations.Count) animations; the NAS already had the others."
Write-Host "Companion animation pool $pool is staged on $NasHost. Install it as root:"
Write-Host "  /opt/a-nas/current/install-companion-animations.sh $pool"

} finally {
    Remove-Item -LiteralPath $movDir -Recurse -Force -ErrorAction SilentlyContinue -WhatIf:$false
}
