# Stages a media pool on the NAS, uploading only the files it does not have
# yet: the local console's screen saver videos (-Kind screensavers) or the
# companion's animations (-Kind companion). Both are long-lived media, not part
# of a release (ADR 0014); root then installs the pool with
# install-media-pool.sh. The companion's animations come from a dsh-pet
# checkout at the commit scripts/vendor-dsh-pet.py pins and, for Safari, from
# the assets-mov zip it pins, and must match the hashes in
# web/src/companion/dsh-pet/animations.sha256.
[CmdletBinding(SupportsShouldProcess = $true, ConfirmImpact = 'Medium', DefaultParameterSetName = 'screensavers')]
param(
    [Parameter(Mandatory = $true)]
    [ValidateSet('screensavers', 'companion')]
    [string]$Kind,

    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[A-Za-z0-9][A-Za-z0-9.-]*$')]
    [string]$NasHost,

    [Parameter(Mandatory = $true, ParameterSetName = 'screensavers')]
    [string[]]$Video,

    [Parameter(Mandatory = $true, ParameterSetName = 'companion')]
    [string]$DshPet,

    [Parameter(Mandatory = $true, ParameterSetName = 'companion')]
    [string]$MovZip,

    [string]$IdentityFile = (Join-Path $env:USERPROFILE '.ssh\a-nas-dev_ed25519')
)

$ErrorActionPreference = 'Stop'
if ($Kind -ne $PSCmdlet.ParameterSetName) {
    throw '-Video goes with -Kind screensavers; -DshPet and -MovZip go with -Kind companion.'
}
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$target = "anas-dev@$NasHost"
$staging = "apps/a-nas/$Kind"
$movDir = $null

try {

if ($Kind -eq 'screensavers') {
    $suffix = '.mp4'
    $label = 'screen saver'
    $noun = 'videos'
    if ($Video.Count -gt 32) {
        throw 'A screen saver pool cannot contain more than 32 videos.'
    }
    $files = @()
    foreach ($candidate in $Video) {
        if (-not (Test-Path -LiteralPath $candidate -PathType Leaf)) {
            throw "Screen saver video does not exist: $candidate"
        }
        $resolvedPath = (Resolve-Path -LiteralPath $candidate).Path
        if ([System.IO.Path]::GetExtension($resolvedPath) -ine '.mp4') {
            throw "Screen saver video must be an MP4 file: $resolvedPath"
        }
        $hash = (Get-FileHash -LiteralPath $resolvedPath -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($files.Hash -contains $hash) {
            throw "Screen saver video was provided more than once: $resolvedPath"
        }
        $files += [PSCustomObject]@{ Path = $resolvedPath; Hash = $hash }
    }
    # The pool is the videos in this order, named on the command line.
    $stageArguments = ($files | ForEach-Object { $_.Hash }) -join ' '
} else {
    $suffix = ''
    $label = 'companion animation'
    $noun = 'animations'
    $manifestPath = Join-Path $repoRoot 'web\src\companion\dsh-pet\animations.sha256'
    # The HEVC animations are checked one by one against the manifest, from a private copy.
    $movDir = Join-Path ([System.IO.Path]::GetTempPath()) ('dsh-pet-mov-' + [guid]::NewGuid().ToString('N'))
    # A private copy, not a change on the NAS: made and removed even under -WhatIf.
    Expand-Archive -LiteralPath $MovZip -DestinationPath $movDir -WhatIf:$false
    # The manifest goes to the NAS as is, where a CR would end up in every name.
    if ([System.IO.File]::ReadAllText($manifestPath).Contains("`r")) {
        throw "Manifest has CRLF line endings; check it out again so .gitattributes applies: $manifestPath"
    }
    $files = @()
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
        $files += [PSCustomObject]@{ Path = $path; Hash = $hash }
    }
    # The pool is the manifest, uploaded before the files.
    $stageArguments = ''
}
if (-not (Test-Path -LiteralPath $IdentityFile -PathType Leaf)) {
    throw "SSH identity does not exist: $IdentityFile"
}

$summary = "stage a $label pool of $($files.Count) $noun, uploading only those the NAS does not have"
if (-not $PSCmdlet.ShouldProcess($target, $summary)) {
    if ($Kind -eq 'screensavers') {
        foreach ($item in $files) {
            Write-Host "$($item.Hash)  $($item.Path)"
        }
    } else {
        Write-Host "All $($files.Count) animations match $manifestPath."
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
$stage = "bash ~/$staging/remote-stage-media-pool.sh $Kind"

& ssh.exe @sshOptions $target "install -d -m 0750 ~/$staging/objects ~/$staging/pools"
if ($LASTEXITCODE -ne 0) { throw 'Cannot create the remote staging directory.' }
& scp.exe @sshOptions (Join-Path $repoRoot 'scripts\remote-stage-media-pool.sh') "${target}:$staging/remote-stage-media-pool.sh"
if ($LASTEXITCODE -ne 0) { throw 'Cannot upload the staging script.' }
if ($Kind -eq 'companion') {
    & scp.exe @sshOptions $manifestPath "${target}:$staging/pools/.incoming.sums"
    if ($LASTEXITCODE -ne 0) { throw 'Cannot upload the manifest.' }
}

$missing = @(& ssh.exe @sshOptions $target "$stage missing $stageArguments" |
    Where-Object { $_ -match '^[0-9a-f]{64}$' })
if ($LASTEXITCODE -ne 0) { throw "Cannot list the $noun the NAS lacks." }
$uploaded = @()
foreach ($item in $files) {
    # Two animations may share a file; it is uploaded once.
    if ($missing -contains $item.Hash -and $uploaded -notcontains $item.Hash) {
        & scp.exe @sshOptions $item.Path "${target}:$staging/objects/$($item.Hash)$suffix.incoming"
        if ($LASTEXITCODE -ne 0) { throw "Upload failed: $($item.Path)" }
        $uploaded += $item.Hash
    }
}

$pool = @(& ssh.exe @sshOptions $target "$stage commit $stageArguments" |
    Where-Object { $_ -match '^[0-9a-f]{16}$' }) | Select-Object -Last 1
if ($LASTEXITCODE -ne 0 -or -not $pool) { throw "Staging the $label pool failed." }

Write-Host "Uploaded $($uploaded.Count) of $($files.Count) $noun; the NAS already had the others."
if ($Kind -eq 'screensavers') {
    foreach ($item in $files) {
        Write-Host "$($item.Hash)  $($item.Path)"
    }
}
Write-Host "The $label pool $pool is staged on $NasHost. Install it as root:"
if ($Kind -eq 'screensavers') {
    # The a-nas package installs both names (docs/runbooks/operate-local-kiosk.md).
    Write-Host "  /usr/lib/a-nas/install-screensavers.sh $pool"
} else {
    Write-Host "  /usr/lib/a-nas/install-media-pool.sh $Kind $pool"
}

} finally {
    if ($movDir) {
        Remove-Item -LiteralPath $movDir -Recurse -Force -ErrorAction SilentlyContinue -WhatIf:$false
    }
}
