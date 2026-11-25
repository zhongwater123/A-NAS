[CmdletBinding(SupportsShouldProcess = $true, ConfirmImpact = 'Medium')]
param(
    # The NAS to copy the repository to over SSH, as the staging account
    # anas-dev. Without it the repository is only written here.
    [ValidatePattern('^[A-Za-z0-9][A-Za-z0-9.-]*$')]
    [string]$NasHost,

    [string]$IdentityFile = (Join-Path $env:USERPROFILE '.ssh\a-nas-dev_ed25519'),

    [string]$WslDistribution = 'Ubuntu-24.04',

    [string]$WslUser = 'anas-dev',

    # The checkout in WSL that builds the release, at the same commit.
    [string]$WslCheckout = '~/workspace/A-NAS',

    # The signed apt repository make release writes (docs/runbooks/deploy-a-nas-packages.md).
    [string]$Repository = 'D:\A-NAS-releases\apt',

    # The release content store: the models, runtimes, Qdrant, llama.cpp and
    # the assistant trial's Hermes runtime (scripts/build-content-packages.sh).
    [string]$Content = 'D:\A-NAS-releases\content'
)

# Puts this commit's packages into the signed apt repository (ADR 0034):
# make check and the system test unless they already passed for this commit's
# packages, then make release. With -NasHost it copies the repository's new
# and changed files to anas-dev@NAS:apps/a-nas/repository and prints the root
# commands of the deploy runbook. It changes nothing that runs on the NAS.

$ErrorActionPreference = 'Stop'
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path

# A Windows path as WSL sees it, such as D:\A-NAS-releases -> /mnt/d/A-NAS-releases.
function ConvertTo-WslPath([string]$Path) {
    $full = [System.IO.Path]::GetFullPath($Path)
    if ($full -notmatch '^([A-Za-z]):\\(.*)$') { throw "Not a drive path: $full" }
    "/mnt/$($Matches[1].ToLowerInvariant())/$($Matches[2].Replace('\', '/'))"
}

Push-Location $repoRoot
try {
    $gitSafeDirectory = "safe.directory=$($repoRoot.Replace('\', '/'))"
    $commit = ([string](& git -c $gitSafeDirectory rev-parse --short=12 HEAD)).Trim()
    if ($LASTEXITCODE -ne 0 -or $commit -notmatch '^[0-9a-f]{12}$') { throw 'Cannot determine the commit.' }
    $changes = & git -c $gitSafeDirectory status --porcelain
    if ($LASTEXITCODE -ne 0) { throw 'Cannot inspect the Git working tree.' }
    if ($changes) { throw 'A release is built from a commit: commit everything first.' }
    if ($NasHost -and -not (Test-Path -LiteralPath $IdentityFile -PathType Leaf)) {
        throw "SSH identity does not exist: $IdentityFile"
    }
    New-Item -ItemType Directory -Path $Repository -Force | Out-Null
    $wslRepository = ConvertTo-WslPath $Repository
    $wslContent = ConvertTo-WslPath $Content

    $target = if ($NasHost) { "the repository $Repository and anas-dev@$NasHost" } else { "the repository $Repository" }
    if (-not $PSCmdlet.ShouldProcess($target, "check, test and publish commit $commit")) { return }

    # The WSL checkout builds the commit; the gates run only when their records
    # do not name its packages (scripts/release.sh).
    $gate = @(
        'set -e',
        "cd $WslCheckout",
        "test `"`$(git rev-parse --short=12 HEAD)`" = $commit || { echo 'check out $commit in $WslCheckout first' >&2; exit 2; }",
        "make release REPO=$wslRepository CONTENT=$wslContent 2>/dev/null || {",
        '  make check',
        '  make system-test',
        "  make release REPO=$wslRepository CONTENT=$wslContent",
        '}'
    ) -join "`n"
    & wsl.exe -d $WslDistribution -u $WslUser -- bash -lc $gate
    if ($LASTEXITCODE -ne 0) { throw 'The gates or make release failed in WSL.' }

    if ($NasHost) {
        $remote = "anas-dev@$NasHost"
        $sshOptions = @('-o', 'BatchMode=yes', '-o', 'IdentitiesOnly=yes', '-o', 'StrictHostKeyChecking=yes', '-i', $IdentityFile)
        # What the NAS holds, by path and size; pool files never change once
        # published, so a file with the same path and size is not sent again.
        $held = @{}
        $listing = & ssh.exe @sshOptions $remote 'mkdir -p apps/a-nas/repository && cd apps/a-nas/repository && find . -type f -printf "%P %s\n"'
        if ($LASTEXITCODE -ne 0) { throw "Cannot list the repository on $NasHost." }
        foreach ($line in $listing) {
            $parts = ([string]$line).Split(' ')
            if ($parts.Count -eq 2) { $held[$parts[0]] = $parts[1] }
        }
        $root = (Resolve-Path -LiteralPath $Repository).Path
        $sent = 0
        foreach ($file in Get-ChildItem -LiteralPath $root -Recurse -File) {
            $relative = $file.FullName.Substring($root.Length + 1).Replace('\', '/')
            if ($relative.StartsWith('.')) { continue }
            if ($relative -notlike 'dists/*' -and $held[$relative] -eq [string]$file.Length) { continue }
            $directory = [System.IO.Path]::GetDirectoryName($relative).Replace('\', '/')
            & ssh.exe @sshOptions $remote "mkdir -p 'apps/a-nas/repository/$directory'"
            if ($LASTEXITCODE -ne 0) { throw "Cannot create apps/a-nas/repository/$directory on $NasHost." }
            & scp.exe @sshOptions $file.FullName "${remote}:apps/a-nas/repository/$relative"
            if ($LASTEXITCODE -ne 0) { throw "Copy failed: $relative" }
            $sent++
        }
        Write-Host "Copied $sent files to ${remote}:apps/a-nas/repository."
        Write-Host 'As root on the NAS, follow docs/runbooks/deploy-a-nas-packages.md from step 3.'
    } else {
        Write-Host "The repository is in $Repository; copy it to the NAS (docs/runbooks/deploy-a-nas-packages.md, step 2)."
    }
} finally {
    Pop-Location
}
