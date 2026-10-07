[CmdletBinding(SupportsShouldProcess = $true, ConfirmImpact = 'Medium')]
param(
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[A-Za-z0-9][A-Za-z0-9.-]*$')]
    [string]$NasHost,

    [ValidateSet('fake', 'agent')]
    [string]$Mode = 'fake',

    [string]$IdentityFile = (Join-Path $env:USERPROFILE '.ssh\a-nas-dev_ed25519'),

    [string]$WslDistribution = 'Ubuntu-24.04',

    [string]$WslUser = 'anas-dev',

    [string]$ScreensaverVideo,

    [switch]$StageOnly
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
    $productVersion = $version
    $releaseTags = @(& git -c $gitSafeDirectory tag --points-at HEAD --sort=-version:refname --list 'v[0-9]*')
    if ($LASTEXITCODE -ne 0) {
        throw 'Cannot inspect release tags.'
    }
    foreach ($releaseTag in $releaseTags) {
        $candidate = ([string]$releaseTag).Trim()
        if ($candidate -match '^v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$') {
            $productVersion = $candidate
            break
        }
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
    $screensaverPath = $null
    $screensaverHash = $null
    if ($ScreensaverVideo) {
        if (-not (Test-Path -LiteralPath $ScreensaverVideo -PathType Leaf)) {
            throw "Screen saver video does not exist: $ScreensaverVideo"
        }
        $screensaverPath = (Resolve-Path -LiteralPath $ScreensaverVideo).Path
        $screensaverHash = (Get-FileHash -LiteralPath $screensaverPath -Algorithm SHA256).Hash.ToLowerInvariant()
    }

    $action = if ($StageOnly) { 'stage for a root-managed system upgrade without touching running services' } else { "activate in $Mode mode, verify, and rollback on failure" }
    $screenSaverSummary = if ($screensaverPath) { ', including the external screen saver video' } else { '' }
    $summary = "build commit $version as product $productVersion, deploy both binaries and local-console artifacts$screenSaverSummary, then $action"
    if (-not $PSCmdlet.ShouldProcess($target, $summary)) {
        Write-Host "Release: $version"
        Write-Host "Product: $productVersion"
        Write-Host "Target:  $target"
        Write-Host "Mode:    $Mode"
        Write-Host 'No build, network connection, or remote change was performed.'
        return
    }

    $apiBinary = Join-Path $repoRoot 'build\anas-api'
    $agentBinary = Join-Path $repoRoot 'build\anas-host-agent'
    $validationManifest = Join-Path $repoRoot 'build\.validated-build'
    $reuseValidation = $false
    if ((Test-Path -LiteralPath $validationManifest -PathType Leaf) -and
        (Test-Path -LiteralPath $apiBinary -PathType Leaf) -and
        (Test-Path -LiteralPath $agentBinary -PathType Leaf)) {
        $validation = @{}
        foreach ($line in Get-Content -LiteralPath $validationManifest) {
            $parts = $line -split '=', 2
            if ($parts.Count -eq 2) { $validation[$parts[0]] = $parts[1] }
        }
        $currentApiHash = (Get-FileHash -LiteralPath $apiBinary -Algorithm SHA256).Hash.ToLowerInvariant()
        $currentAgentHash = (Get-FileHash -LiteralPath $agentBinary -Algorithm SHA256).Hash.ToLowerInvariant()
        $reuseValidation =
            $validation.commit -eq $version -and
            $validation.product_version -eq $productVersion -and
            $validation.api_sha256 -eq $currentApiHash -and
            $validation.host_agent_sha256 -eq $currentAgentHash
    }
    if ($reuseValidation) {
        Write-Host "Reusing full validation for $productVersion ($version)."
    } else {
        & wsl.exe -d $WslDistribution -u $WslUser -- bash -lc "cd ~/workspace/A-NAS && make check VERSION=$productVersion"
        if ($LASTEXITCODE -ne 0) { throw 'WSL make check failed.' }
    }

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
        @{ Source = (Join-Path $repoRoot 'deploy\systemd\system\anas-api.service'); Destination = "${target}:$release/anas-api-system.service.incoming" },
        @{ Source = (Join-Path $repoRoot 'deploy\systemd\system\anas-host-agent.service'); Destination = "${target}:$release/anas-host-agent-system.service.incoming" },
        @{ Source = (Join-Path $repoRoot 'scripts\install-v1.0.1-system-services.sh'); Destination = "${target}:$release/install-v1.0.1-system-services.sh.incoming" },
        @{ Source = (Join-Path $repoRoot 'scripts\provision-v1.0.1-rc.sh'); Destination = "${target}:$release/provision-v1.0.1-rc.sh.incoming" },
        @{ Source = (Join-Path $repoRoot 'scripts\run-kiosk.sh'); Destination = "${target}:$release/kiosk-launcher.incoming" },
        @{ Source = (Join-Path $repoRoot 'deploy\systemd\system\anas-kiosk@.service'); Destination = "${target}:$release/anas-kiosk@.service.incoming" },
        @{ Source = (Join-Path $repoRoot 'deploy\pam\a-nas-kiosk'); Destination = "${target}:$release/a-nas-kiosk.pam.incoming" },
        @{ Source = (Join-Path $repoRoot 'deploy\config\experimental-nas-kiosk.env'); Destination = "${target}:$release/kiosk.env.incoming" },
        @{ Source = (Join-Path $repoRoot 'deploy\chromium\policies\managed\a-nas.json'); Destination = "${target}:$release/a-nas-chromium-policy.json.incoming" },
        @{ Source = (Join-Path $repoRoot 'scripts\remote-activate-release.sh'); Destination = "${target}:$release/activate.incoming" }
    )
    if ($screensaverPath) {
        $uploads += @{ Source = $screensaverPath; Destination = "${target}:$release/screensaver.mp4.incoming" }
    }
    foreach ($upload in $uploads) {
        & scp.exe @sshOptions $upload.Source $upload.Destination
        if ($LASTEXITCODE -ne 0) { throw "Upload failed: $($upload.Source)" }
    }

    $remoteAction = if ($StageOnly) { 'stage-only' } else { 'activate' }
    $activateArguments = @($version, $apiHash, $agentHash, $Mode, $productVersion, $remoteAction)
    if ($screensaverHash) { $activateArguments += $screensaverHash }
    $activate = "chmod 0700 ~/$release/activate.incoming && ~/$release/activate.incoming $($activateArguments -join ' ')"
    & ssh.exe @sshOptions $target $activate
    if ($LASTEXITCODE -ne 0) { throw 'Remote activation failed; inspect the user journal and release directory.' }

    if ($StageOnly) {
        Write-Host "A-NAS $productVersion ($version) is staged on $NasHost for root installation."
    } else {
        Write-Host "A-NAS $productVersion ($version) is active on $NasHost in $Mode mode."
    }
    Write-Host "API SHA-256:        $apiHash"
    Write-Host "Host Agent SHA-256: $agentHash"
    if ($screensaverHash) { Write-Host "Screen saver SHA-256: $screensaverHash" }
} finally {
    Pop-Location
}
