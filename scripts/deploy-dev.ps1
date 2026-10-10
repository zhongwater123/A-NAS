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

    # Where the model that deploy\models\embeddinggemma-2-740m.json pins lives
    # on this machine (docs/architecture/photo-ai.md).
    [string]$ModelDirectory = 'D:\A-NAS-models\embeddinggemma-2-740m',

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

    $action = if ($StageOnly) { 'stage for a root-managed system upgrade without touching running services' } else { "activate in $Mode mode, verify, and rollback on failure" }
    $summary = "build commit $version as product $productVersion, deploy both binaries, local AI and local-console artifacts, then $action"
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

    # Local AI is part of every release (ADR 0016). The model and the Python
    # runtime travel once per content; the release carries their manifests.
    $modelManifestPath = Join-Path $repoRoot 'deploy\models\embeddinggemma-2-740m.json'
    $modelManifest = Get-Content -LiteralPath $modelManifestPath -Raw | ConvertFrom-Json
    $modelPath = Join-Path $ModelDirectory $modelManifest.file
    $runtimeManifestPath = Join-Path $repoRoot 'build\ai-runtime.json'
    if (-not (Test-Path -LiteralPath $runtimeManifestPath -PathType Leaf)) {
        throw 'Missing build\ai-runtime.json: run scripts/build-ai-runtime.sh in WSL first.'
    }
    $runtimeManifest = Get-Content -LiteralPath $runtimeManifestPath -Raw | ConvertFrom-Json
    $runtimePath = Join-Path $repoRoot "build\ai-runtime\$($runtimeManifest.file)"
    foreach ($content in @(@{ Path = $modelPath; Sha = $modelManifest.sha256 }, @{ Path = $runtimePath; Sha = $runtimeManifest.sha256 })) {
        if (-not (Test-Path -LiteralPath $content.Path -PathType Leaf)) {
            throw "Missing local AI file: $($content.Path)"
        }
        if ((Get-FileHash -LiteralPath $content.Path -Algorithm SHA256).Hash.ToLowerInvariant() -ne $content.Sha) {
            throw "Local AI file does not match its manifest: $($content.Path)"
        }
    }
    $workerArchive = Join-Path $repoRoot 'build\ai-worker.tar'
    & git -c $gitSafeDirectory archive --format=tar -o $workerArchive HEAD ai/anas_ai
    if ($LASTEXITCODE -ne 0) { throw 'Cannot archive the AI Worker.' }

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

    # Uploads a file the NAS staging area does not hold yet and checks it there.
    function Send-StagedContent([string]$Source, [string]$Directory, [string]$Name, [string]$Sha) {
        $remote = "$Directory/$Name"
        & ssh.exe @sshOptions $target "printf '%s  %s\n' $Sha ~/$remote | sha256sum --check --quiet - >/dev/null 2>&1"
        if ($LASTEXITCODE -eq 0) {
            Write-Host "The NAS already holds $Name."
            return
        }
        & ssh.exe @sshOptions $target "install -d -m 0750 ~/$Directory"
        if ($LASTEXITCODE -ne 0) { throw "Cannot create ~/$Directory on the NAS." }
        & scp.exe @sshOptions $Source "${target}:$remote.incoming"
        if ($LASTEXITCODE -ne 0) { throw "Upload failed: $Source" }
        & ssh.exe @sshOptions $target "printf '%s  %s\n' $Sha ~/$remote.incoming | sha256sum --check --quiet - && mv -f ~/$remote.incoming ~/$remote"
        if ($LASTEXITCODE -ne 0) { throw "The uploaded file does not match its manifest: $Name" }
    }
    Send-StagedContent $modelPath "apps/a-nas/models/$($modelManifest.sha256)" $modelManifest.file $modelManifest.sha256
    Send-StagedContent $runtimePath 'apps/a-nas/ai-runtimes' $runtimeManifest.file $runtimeManifest.sha256

    $uploads = @(
        @{ Source = $apiBinary; Destination = "${target}:$release/anas-api.incoming" },
        @{ Source = $agentBinary; Destination = "${target}:$release/anas-host-agent.incoming" },
        @{ Source = (Join-Path $repoRoot 'deploy\systemd\user\anas-api.service'); Destination = "${target}:$release/anas-api.service.incoming" },
        @{ Source = (Join-Path $repoRoot 'deploy\systemd\user\anas-host-agent.service'); Destination = "${target}:$release/anas-host-agent.service.incoming" },
        @{ Source = (Join-Path $repoRoot 'deploy\systemd\system\anas-api.service'); Destination = "${target}:$release/anas-api-system.service.incoming" },
        @{ Source = (Join-Path $repoRoot 'deploy\systemd\system\anas-host-agent.service'); Destination = "${target}:$release/anas-host-agent-system.service.incoming" },
        @{ Source = (Join-Path $repoRoot 'deploy\systemd\system\anas-photos.service'); Destination = "${target}:$release/anas-photos-system.service.incoming" },
        @{ Source = (Join-Path $repoRoot 'deploy\systemd\system\anas-ai.socket'); Destination = "${target}:$release/anas-ai-system.socket.incoming" },
        @{ Source = (Join-Path $repoRoot 'deploy\systemd\system\anas-ai.service'); Destination = "${target}:$release/anas-ai-system.service.incoming" },
        @{ Source = $workerArchive; Destination = "${target}:$release/ai-worker.tar.incoming" },
        @{ Source = $modelManifestPath; Destination = "${target}:$release/ai-model.json.incoming" },
        @{ Source = $runtimeManifestPath; Destination = "${target}:$release/ai-runtime.json.incoming" },
        @{ Source = (Join-Path $repoRoot 'scripts\install-v1.0.1-system-services.sh'); Destination = "${target}:$release/install-v1.0.1-system-services.sh.incoming" },
        @{ Source = (Join-Path $repoRoot 'scripts\provision-v1.0.1-rc.sh'); Destination = "${target}:$release/provision-v1.0.1-rc.sh.incoming" },
        @{ Source = (Join-Path $repoRoot 'scripts\install-screensavers.sh'); Destination = "${target}:$release/install-screensavers.sh.incoming" },
        @{ Source = (Join-Path $repoRoot 'scripts\run-kiosk.sh'); Destination = "${target}:$release/kiosk-launcher.incoming" },
        @{ Source = (Join-Path $repoRoot 'deploy\systemd\system\anas-kiosk@.service'); Destination = "${target}:$release/anas-kiosk@.service.incoming" },
        @{ Source = (Join-Path $repoRoot 'deploy\pam\a-nas-kiosk'); Destination = "${target}:$release/a-nas-kiosk.pam.incoming" },
        @{ Source = (Join-Path $repoRoot 'deploy\config\experimental-nas-kiosk.env'); Destination = "${target}:$release/kiosk.env.incoming" },
        @{ Source = (Join-Path $repoRoot 'deploy\chromium\policies\managed\a-nas.json'); Destination = "${target}:$release/a-nas-chromium-policy.json.incoming" },
        @{ Source = (Join-Path $repoRoot 'deploy\caddy\Caddyfile'); Destination = "${target}:$release/Caddyfile.incoming" },
        @{ Source = (Join-Path $repoRoot 'scripts\remote-activate-release.sh'); Destination = "${target}:$release/activate.incoming" }
    )
    foreach ($upload in $uploads) {
        & scp.exe @sshOptions $upload.Source $upload.Destination
        if ($LASTEXITCODE -ne 0) { throw "Upload failed: $($upload.Source)" }
    }

    $remoteAction = if ($StageOnly) { 'stage-only' } else { 'activate' }
    $activateArguments = @($version, $apiHash, $agentHash, $Mode, $productVersion, $remoteAction)
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
} finally {
    Pop-Location
}
