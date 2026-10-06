[CmdletBinding(SupportsShouldProcess = $true, ConfirmImpact = 'Medium')]
param(
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[A-Za-z0-9][A-Za-z0-9.-]*$')]
    [string]$NasHost,

    [ValidateSet('fake', 'agent')]
    [string]$Mode = 'fake',

    [string]$IdentityFile = (Join-Path $env:USERPROFILE '.ssh\a-nas-dev_ed25519'),

    [string]$WslDistribution = 'Ubuntu-24.04',

    [string]$WslUser = 'anas-dev'
)

$ErrorActionPreference = 'Stop'
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$target = "anas-dev@$NasHost"

Push-Location $repoRoot
try {
    $gitSafeDirectory = "safe.directory=$($repoRoot.Replace('\', '/'))"
    $versionOutput = & git -c $gitSafeDirectory rev-parse --short=12 HEAD
    if ($LASTEXITCODE -ne 0) {
        throw 'Cannot determine the Git version.'
    }
    $version = ([string]$versionOutput).Trim()
    if ($version -notmatch '^[0-9a-f]{7,12}$') {
        throw 'Cannot determine the Git version.'
    }
    $changes = & git -c $gitSafeDirectory status --porcelain
    if ($LASTEXITCODE -ne 0) {
        throw 'Cannot inspect the Git working tree.'
    }
    if ($changes) {
        throw 'Deployment requires a clean, committed working tree.'
    }
    if (-not (Test-Path -LiteralPath $IdentityFile -PathType Leaf)) {
        throw "SSH identity does not exist: $IdentityFile"
    }

    $summary = "build commit $version, deploy both binaries and local-console artifacts in $Mode mode, activate, verify, and rollback on failure"
    if (-not $PSCmdlet.ShouldProcess($target, $summary)) {
        Write-Host "Version: $version"
        Write-Host "Target:  $target"
        Write-Host "Mode:    $Mode"
        Write-Host 'No build, network connection, or remote change was performed.'
        return
    }

    & wsl.exe -d $WslDistribution -u $WslUser -- bash -lc "cd ~/workspace/A-NAS && make check VERSION=$version"
    if ($LASTEXITCODE -ne 0) { throw 'WSL make check failed.' }

    $apiBinary = Join-Path $repoRoot 'build\anas-api'
    $agentBinary = Join-Path $repoRoot 'build\anas-host-agent'
    $apiHash = (Get-FileHash -LiteralPath $apiBinary -Algorithm SHA256).Hash.ToLowerInvariant()
    $agentHash = (Get-FileHash -LiteralPath $agentBinary -Algorithm SHA256).Hash.ToLowerInvariant()
    $release = "apps/a-nas/releases/$version"
    $sshOptions = @(
        '-o', 'BatchMode=yes',
        '-o', 'IdentitiesOnly=yes',
        '-o', 'StrictHostKeyChecking=yes',
        '-i', $IdentityFile
    )

    & ssh.exe @sshOptions $target "install -d -m 0750 ~/$release"
    if ($LASTEXITCODE -ne 0) { throw 'Cannot create the remote release directory.' }

    $uploads = @(
        @{ Source = $apiBinary; Destination = "${target}:$release/anas-api.incoming" },
        @{ Source = $agentBinary; Destination = "${target}:$release/anas-host-agent.incoming" },
        @{ Source = (Join-Path $repoRoot 'deploy\systemd\user\anas-api.service'); Destination = "${target}:$release/anas-api.service.incoming" },
        @{ Source = (Join-Path $repoRoot 'deploy\systemd\user\anas-host-agent.service'); Destination = "${target}:$release/anas-host-agent.service.incoming" },
        @{ Source = (Join-Path $repoRoot 'scripts\run-kiosk.sh'); Destination = "${target}:$release/kiosk-launcher.incoming" },
        @{ Source = (Join-Path $repoRoot 'deploy\systemd\system\anas-kiosk@.service'); Destination = "${target}:$release/anas-kiosk@.service.incoming" },
        @{ Source = (Join-Path $repoRoot 'deploy\pam\a-nas-kiosk'); Destination = "${target}:$release/a-nas-kiosk.pam.incoming" },
        @{ Source = (Join-Path $repoRoot 'deploy\config\experimental-nas-kiosk.env'); Destination = "${target}:$release/kiosk.env.incoming" },
        @{ Source = (Join-Path $repoRoot 'scripts\remote-activate-release.sh'); Destination = "${target}:$release/activate.incoming" }
    )
    foreach ($upload in $uploads) {
        & scp.exe @sshOptions $upload.Source $upload.Destination
        if ($LASTEXITCODE -ne 0) { throw "Upload failed: $($upload.Source)" }
    }

    $activate = "chmod 0700 ~/$release/activate.incoming && ~/$release/activate.incoming $version $apiHash $agentHash $Mode"
    & ssh.exe @sshOptions $target $activate
    if ($LASTEXITCODE -ne 0) { throw 'Remote activation failed; inspect the user journal and release directory.' }

    Write-Host "A-NAS $version is active on $NasHost in $Mode mode."
    Write-Host "API SHA-256:        $apiHash"
    Write-Host "Host Agent SHA-256: $agentHash"
} finally {
    Pop-Location
}
