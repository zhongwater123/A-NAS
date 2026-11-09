[CmdletBinding(SupportsShouldProcess = $true, ConfirmImpact = 'Medium')]
param(
    # The NAS to stage on over SSH. Give it or -BundleDirectory.
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

    # Where the object detector that deploy\models\dfine-m-obj365.json pins
    # lives, as exported from the official weights
    # (docs/research/photo-ai-object-detection.md).
    [string]$DetectorDirectory = 'D:\A-NAS-models\dfine-obj365\official',

    # Where the face models that deploy\models\yunet-2026may.json and the
    # face embedder's manifest pin live (docs/architecture/photo-people.md).
    [string]$FaceDetectorDirectory = 'D:\A-NAS-models\faces\yunet',
    [string]$FaceEmbedderDirectory = 'D:\A-NAS-models\faces\auraface',

    # Where the animal model that deploy\models\dinov2-small.json pins lives
    # (docs/architecture/photo-people.md#宠物).
    [string]$AnimalEmbedderDirectory = 'D:\A-NAS-models\pets\dinov2-small',

    # Where the text reader's language model and vision encoder, which
    # deploy\models\qwen3.5-4b-q4_k_m.json and qwen3.5-4b-mmproj-f16.json pin,
    # live as converted by ai\eval\ocr_models.sh (docs/architecture/photo-ocr.md).
    [string]$OcrDirectory = 'D:\A-NAS-models\ocr\qwen3.5-4b',

    [switch]$StageOnly,

    # Writes the release into this directory as a bundle instead of uploading
    # it: a deploy machine that only copies files stages it on the NAS with
    # the bundle's stage.sh (docs/runbooks/stage-a-release-bundle.md). The
    # bundle goes to a-nas-<commit>, its content files to content, which later
    # bundles share. A bundle is always stage-only.
    [string]$BundleDirectory,

    # Files the bundle puts into the staged release directory as they are,
    # such as the container agent or the assistant trial bundle.
    [string[]]$ExtraFile = @(),

    # Root scripts the bundle puts into the staging account's home, where root
    # installs them after checking their hashes.
    [string[]]$RootScript = @()
)

$ErrorActionPreference = 'Stop'
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$bundle = -not [string]::IsNullOrEmpty($BundleDirectory)
if ($bundle -eq (-not [string]::IsNullOrEmpty($NasHost))) {
    throw 'Give either -NasHost or -BundleDirectory.'
}
if (-not $bundle -and ($ExtraFile.Count -gt 0 -or $RootScript.Count -gt 0)) {
    throw '-ExtraFile and -RootScript belong to a bundle: give -BundleDirectory.'
}
$target = if ($bundle) { $BundleDirectory } else { "anas-dev@$NasHost" }

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
    if (-not $bundle -and -not (Test-Path -LiteralPath $IdentityFile -PathType Leaf)) {
        throw "SSH identity does not exist: $IdentityFile"
    }

    $action = if ($bundle) { 'write them as a stage-only bundle for a deploy machine' } elseif ($StageOnly) { 'stage for a root-managed system upgrade without touching running services' } else { "activate in $Mode mode, verify, and rollback on failure" }
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

    # Local AI is part of every release (ADR 0016). The models and the Python
    # runtime travel once per content; the release carries their manifests.
    # Each model has the role its release link is named after.
    $modelManifestPath = Join-Path $repoRoot 'deploy\models\embeddinggemma-2-740m.json'
    $detectorManifestPath = Join-Path $repoRoot 'deploy\models\dfine-m-obj365.json'
    $faceDetectorManifestPath = Join-Path $repoRoot 'deploy\models\yunet-2026may.json'
    $faceEmbedderManifestPath = Join-Path $repoRoot 'deploy\models\auraface-v1.json'
    $animalEmbedderManifestPath = Join-Path $repoRoot 'deploy\models\dinov2-small.json'
    $ocrModelManifestPath = Join-Path $repoRoot 'deploy\models\qwen3.5-4b-q4_k_m.json'
    $ocrMmprojManifestPath = Join-Path $repoRoot 'deploy\models\qwen3.5-4b-mmproj-f16.json'
    $models = @(
        @{ Manifest = $modelManifestPath; Directory = $ModelDirectory },
        @{ Manifest = $detectorManifestPath; Directory = $DetectorDirectory },
        @{ Manifest = $faceDetectorManifestPath; Directory = $FaceDetectorDirectory },
        @{ Manifest = $faceEmbedderManifestPath; Directory = $FaceEmbedderDirectory },
        @{ Manifest = $animalEmbedderManifestPath; Directory = $AnimalEmbedderDirectory },
        @{ Manifest = $ocrModelManifestPath; Directory = $OcrDirectory },
        @{ Manifest = $ocrMmprojManifestPath; Directory = $OcrDirectory }
    )
    foreach ($model in $models) {
        $model.Pinned = Get-Content -LiteralPath $model.Manifest -Raw | ConvertFrom-Json
        $model.Path = Join-Path $model.Directory $model.Pinned.file
    }
    $runtimeManifestPath = Join-Path $repoRoot 'build\ai-runtime.json'
    if (-not (Test-Path -LiteralPath $runtimeManifestPath -PathType Leaf)) {
        throw 'Missing build\ai-runtime.json: run scripts/build-ai-runtime.sh in WSL first.'
    }
    $runtimeManifest = Get-Content -LiteralPath $runtimeManifestPath -Raw | ConvertFrom-Json
    $runtimePath = Join-Path $repoRoot "build\ai-runtime\$($runtimeManifest.file)"
    $contents = @($models | ForEach-Object { @{ Path = $_.Path; Sha = $_.Pinned.sha256 } }) + @(@{ Path = $runtimePath; Sha = $runtimeManifest.sha256 })
    foreach ($content in $contents) {
        if (-not (Test-Path -LiteralPath $content.Path -PathType Leaf)) {
            throw "Missing local AI file: $($content.Path)"
        }
        if ((Get-FileHash -LiteralPath $content.Path -Algorithm SHA256).Hash.ToLowerInvariant() -ne $content.Sha) {
            throw "Local AI file does not match its manifest: $($content.Path)"
        }
    }
    # Web search's runtime travels the same way (ADR 0030).
    $searchRuntimeManifestPath = Join-Path $repoRoot 'build\search-runtime.json'
    if (-not (Test-Path -LiteralPath $searchRuntimeManifestPath -PathType Leaf)) {
        throw 'Missing build\search-runtime.json: run scripts/build-search-runtime.sh in WSL first.'
    }
    $searchRuntimeManifest = Get-Content -LiteralPath $searchRuntimeManifestPath -Raw | ConvertFrom-Json
    $searchRuntimePath = Join-Path $repoRoot "build\search-runtime\$($searchRuntimeManifest.file)"
    if (-not (Test-Path -LiteralPath $searchRuntimePath -PathType Leaf)) {
        throw "Missing the web search runtime: $searchRuntimePath"
    }
    if ((Get-FileHash -LiteralPath $searchRuntimePath -Algorithm SHA256).Hash.ToLowerInvariant() -ne $searchRuntimeManifest.sha256) {
        throw "The web search runtime does not match its manifest: $searchRuntimePath"
    }
    # The vector index's Qdrant travels the same way (ADR 0028):
    # scripts/fetch-qdrant.sh keeps the tarball its manifest pins.
    $qdrantManifestPath = Join-Path $repoRoot 'deploy\qdrant\qdrant.json'
    $qdrant = Get-Content -LiteralPath $qdrantManifestPath -Raw | ConvertFrom-Json
    $qdrantPath = Join-Path $repoRoot "build\qdrant\$($qdrant.sha256)\$($qdrant.file)"
    if (-not (Test-Path -LiteralPath $qdrantPath -PathType Leaf)) {
        throw 'Missing the pinned Qdrant: run scripts/fetch-qdrant.sh in WSL first.'
    }
    if ((Get-FileHash -LiteralPath $qdrantPath -Algorithm SHA256).Hash.ToLowerInvariant() -ne $qdrant.sha256) {
        throw "Qdrant does not match its manifest: $qdrantPath"
    }
    # So does the text reader's llama.cpp: scripts/fetch-llama.sh keeps the
    # tarball its manifest pins.
    $llamaManifestPath = Join-Path $repoRoot 'deploy\llama\llama.json'
    $llama = Get-Content -LiteralPath $llamaManifestPath -Raw | ConvertFrom-Json
    $llamaPath = Join-Path $repoRoot "build\llama\$($llama.sha256)\$($llama.file)"
    if (-not (Test-Path -LiteralPath $llamaPath -PathType Leaf)) {
        throw 'Missing the pinned llama.cpp: run scripts/fetch-llama.sh in WSL first.'
    }
    if ((Get-FileHash -LiteralPath $llamaPath -Algorithm SHA256).Hash.ToLowerInvariant() -ne $llama.sha256) {
        throw "llama.cpp does not match its manifest: $llamaPath"
    }
    # The host packages the release needs (ADR 0031), which make check builds.
    $hostPackage = Join-Path $repoRoot 'build\a-nas-host.deb'
    $consolePackage = Join-Path $repoRoot 'build\a-nas-console.deb'
    foreach ($package in @($hostPackage, $consolePackage)) {
        if (-not (Test-Path -LiteralPath $package -PathType Leaf)) { throw "Missing ${package}: run make check first." }
    }
    $workerArchive = Join-Path $repoRoot 'build\ai-worker.tar'
    & git -c $gitSafeDirectory archive --format=tar -o $workerArchive HEAD ai/anas_ai
    if ($LASTEXITCODE -ne 0) { throw 'Cannot archive the AI Worker.' }

    $apiHash = (Get-FileHash -LiteralPath $apiBinary -Algorithm SHA256).Hash.ToLowerInvariant()
    $agentHash = (Get-FileHash -LiteralPath $agentBinary -Algorithm SHA256).Hash.ToLowerInvariant()

    # Content travels once per content: each file has a directory below the
    # NAS staging area (~/apps/a-nas) that its hash names or that holds one
    # file per content.
    $stagedContents = @($models | ForEach-Object {
            @{ Source = $_.Path; Directory = "models/$($_.Pinned.sha256)"; Name = $_.Pinned.file; Sha = $_.Pinned.sha256 }
        }) + @(
        @{ Source = $runtimePath; Directory = 'ai-runtimes'; Name = $runtimeManifest.file; Sha = $runtimeManifest.sha256 },
        @{ Source = $searchRuntimePath; Directory = 'search-runtimes'; Name = $searchRuntimeManifest.file; Sha = $searchRuntimeManifest.sha256 },
        @{ Source = $qdrantPath; Directory = "qdrant/$($qdrant.sha256)"; Name = $qdrant.file; Sha = $qdrant.sha256 },
        @{ Source = $llamaPath; Directory = "llama/$($llama.sha256)"; Name = $llama.file; Sha = $llama.sha256 }
    )

    # The release's own files, staged under these names for
    # remote-activate-release.sh.
    $uploads = @(
        @{ Source = $apiBinary; Name = 'anas-api.incoming' },
        @{ Source = $agentBinary; Name = 'anas-host-agent.incoming' },
        @{ Source = $hostPackage; Name = 'a-nas-host.deb.incoming' },
        @{ Source = $consolePackage; Name = 'a-nas-console.deb.incoming' },
        @{ Source = (Join-Path $repoRoot 'deploy\systemd\user\anas-api.service'); Name = 'anas-api.service.incoming' },
        @{ Source = (Join-Path $repoRoot 'deploy\systemd\user\anas-host-agent.service'); Name = 'anas-host-agent.service.incoming' },
        @{ Source = (Join-Path $repoRoot 'deploy\systemd\system\anas-api.service'); Name = 'anas-api-system.service.incoming' },
        @{ Source = (Join-Path $repoRoot 'deploy\systemd\system\anas-host-agent.service'); Name = 'anas-host-agent-system.service.incoming' },
        @{ Source = (Join-Path $repoRoot 'deploy\systemd\system\anas-photos.service'); Name = 'anas-photos-system.service.incoming' },
        @{ Source = (Join-Path $repoRoot 'deploy\systemd\system\anas-shares.service'); Name = 'anas-shares-system.service.incoming' },
        @{ Source = (Join-Path $repoRoot 'deploy\systemd\system\anas-family.service'); Name = 'anas-family-system.service.incoming' },
        @{ Source = (Join-Path $repoRoot 'deploy\systemd\system\anas.slice'); Name = 'anas-system.slice.incoming' },
        @{ Source = (Join-Path $repoRoot 'deploy\systemd\system\anas-ai.socket'); Name = 'anas-ai-system.socket.incoming' },
        @{ Source = (Join-Path $repoRoot 'deploy\systemd\system\anas-ai.service'); Name = 'anas-ai-system.service.incoming' },
        @{ Source = (Join-Path $repoRoot 'deploy\systemd\system\anas-ffmpeg.socket'); Name = 'anas-ffmpeg-system.socket.incoming' },
        @{ Source = (Join-Path $repoRoot 'deploy\systemd\system\anas-ffmpeg@.service'); Name = 'anas-ffmpeg-system@.service.incoming' },
        @{ Source = (Join-Path $repoRoot 'deploy\systemd\system\system-anasffmpeg.slice'); Name = 'anas-ffmpeg-system.slice.incoming' },
        @{ Source = (Join-Path $repoRoot 'deploy\systemd\system\anas-vectors@.service'); Name = 'anas-vectors-system@.service.incoming' },
        @{ Source = (Join-Path $repoRoot 'deploy\systemd\system\anas-vectors-proxy@.socket'); Name = 'anas-vectors-proxy-system@.socket.incoming' },
        @{ Source = (Join-Path $repoRoot 'deploy\systemd\system\anas-vectors-proxy@.service'); Name = 'anas-vectors-proxy-system@.service.incoming' },
        @{ Source = $qdrantManifestPath; Name = 'vectors-qdrant.json.incoming' },
        @{ Source = (Join-Path $repoRoot 'deploy\qdrant\config.yaml'); Name = 'vectors-config.yaml.incoming' },
        @{ Source = (Join-Path $repoRoot 'deploy\systemd\system\anas-vectors.slice'); Name = 'anas-vectors-system.slice.incoming' },
        @{ Source = (Join-Path $repoRoot 'deploy\qdrant\start.sh'); Name = 'vectors-start.sh.incoming' },
        @{ Source = (Join-Path $repoRoot 'deploy\qdrant\wait-ready.sh'); Name = 'vectors-wait-ready.sh.incoming' },
        @{ Source = $workerArchive; Name = 'ai-worker.tar.incoming' },
        @{ Source = $modelManifestPath; Name = 'ai-model.json.incoming' },
        @{ Source = $detectorManifestPath; Name = 'ai-detector.json.incoming' },
        @{ Source = $faceDetectorManifestPath; Name = 'ai-face-detector.json.incoming' },
        @{ Source = $faceEmbedderManifestPath; Name = 'ai-face-embedder.json.incoming' },
        @{ Source = $animalEmbedderManifestPath; Name = 'ai-animal-embedder.json.incoming' },
        @{ Source = $ocrModelManifestPath; Name = 'ai-ocr-model.json.incoming' },
        @{ Source = $ocrMmprojManifestPath; Name = 'ai-ocr-mmproj.json.incoming' },
        @{ Source = $runtimeManifestPath; Name = 'ai-runtime.json.incoming' },
        @{ Source = $llamaManifestPath; Name = 'ai-llama.json.incoming' },
        @{ Source = (Join-Path $repoRoot 'deploy\systemd\system\anas-search.service'); Name = 'anas-search-system.service.incoming' },
        @{ Source = (Join-Path $repoRoot 'deploy\systemd\system\anas-search-dns.service'); Name = 'anas-search-dns-system.service.incoming' },
        @{ Source = (Join-Path $repoRoot 'deploy\systemd\system\anas-search-dns.path'); Name = 'anas-search-dns-system.path.incoming' },
        @{ Source = (Join-Path $repoRoot 'deploy\sysusers.d\a-nas-search.conf'); Name = 'a-nas-search.sysusers.incoming' },
        @{ Source = (Join-Path $repoRoot 'deploy\search\settings.yml'); Name = 'search-settings.yml.incoming' },
        @{ Source = (Join-Path $repoRoot 'deploy\search\searxng.json'); Name = 'search-searxng.json.incoming' },
        @{ Source = (Join-Path $repoRoot 'deploy\search\allow-dns.sh'); Name = 'search-allow-dns.sh.incoming' },
        @{ Source = (Join-Path $repoRoot 'deploy\search\anas_search.py'); Name = 'search-anas_search.py.incoming' },
        @{ Source = $searchRuntimeManifestPath; Name = 'search-runtime.json.incoming' },
        @{ Source = (Join-Path $repoRoot 'scripts\install-v1.0.1-system-services.sh'); Name = 'install-v1.0.1-system-services.sh.incoming' },
        @{ Source = (Join-Path $repoRoot 'scripts\provision-v1.0.1-rc.sh'); Name = 'provision-v1.0.1-rc.sh.incoming' },
        @{ Source = (Join-Path $repoRoot 'scripts\install-screensavers.sh'); Name = 'install-screensavers.sh.incoming' },
        @{ Source = (Join-Path $repoRoot 'scripts\install-ffmpeg-units.sh'); Name = 'install-ffmpeg-units.sh.incoming' },
        @{ Source = (Join-Path $repoRoot 'scripts\run-kiosk.sh'); Name = 'kiosk-launcher.incoming' },
        @{ Source = (Join-Path $repoRoot 'deploy\systemd\system\anas-kiosk@.service'); Name = 'anas-kiosk@.service.incoming' },
        @{ Source = (Join-Path $repoRoot 'deploy\pam\a-nas-kiosk'); Name = 'a-nas-kiosk.pam.incoming' },
        @{ Source = (Join-Path $repoRoot 'deploy\sysusers.d\a-nas-kiosk.conf'); Name = 'a-nas-kiosk.sysusers.incoming' },
        @{ Source = (Join-Path $repoRoot 'deploy\config\experimental-nas-kiosk.env'); Name = 'kiosk.env.incoming' },
        @{ Source = (Join-Path $repoRoot 'deploy\chromium\policies\managed\a-nas.json'); Name = 'a-nas-chromium-policy.json.incoming' },
        @{ Source = (Join-Path $repoRoot 'deploy\caddy\Caddyfile'); Name = 'Caddyfile.incoming' },
        @{ Source = (Join-Path $repoRoot 'deploy\caddy\Caddyfile.internet'); Name = 'Caddyfile.internet.incoming' },
        @{ Source = (Join-Path $repoRoot 'deploy\internet\anas-internet.nft'); Name = 'anas-internet.nft.incoming' },
        @{ Source = (Join-Path $repoRoot 'deploy\systemd\system\anas-internet-firewall.service'); Name = 'anas-internet-firewall-system.service.incoming' },
        @{ Source = (Join-Path $repoRoot 'scripts\remote-activate-release.sh'); Name = 'activate.incoming' }
    )
    function Get-Sha256([string]$Path) {
        (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
    }
    # Writes text as the NAS reads it: UTF-8 without a byte order mark, LF.
    function Write-LinuxText([string]$Path, [string[]]$Lines) {
        [System.IO.File]::WriteAllText($Path, (($Lines -join "`n") + "`n"), (New-Object System.Text.UTF8Encoding($false)))
    }
    # Writes what the SSH path below uploads, for stage.sh to stage on the NAS
    # (scripts/stage-release-bundle.sh): release/<Name>, the content
    # under content/<Directory>/<Name>, the extra files and root scripts, and
    # bundle.txt, which lists them with the arguments remote-activate-release.sh
    # takes. SHA256SUMS covers every file of the bundle; content files are
    # checked against the hashes in bundle.txt.
    function Write-ReleaseBundle {
        New-Item -ItemType Directory -Path $BundleDirectory -Force | Out-Null
        $root = [System.IO.Path]::GetFullPath($BundleDirectory)
        $bundleRoot = Join-Path $root "a-nas-$version"
        $contentRoot = Join-Path $root 'content'
        if (Test-Path -LiteralPath $bundleRoot) { throw "The bundle already exists: $bundleRoot" }
        foreach ($part in 'release', 'extra', 'root') {
            New-Item -ItemType Directory -Path (Join-Path $bundleRoot $part) | Out-Null
        }
        $list = @(
            "version $version",
            "product-version $productVersion",
            "mode $Mode",
            "api-sha256 $apiHash",
            "host-agent-sha256 $agentHash"
        )
        foreach ($content in $stagedContents) {
            $directory = Join-Path $contentRoot $content.Directory
            $destination = Join-Path $directory $content.Name
            if ((Test-Path -LiteralPath $destination -PathType Leaf) -and (Get-Sha256 $destination) -eq $content.Sha) {
                Write-Host "The bundle's content already holds $($content.Name)."
            } else {
                New-Item -ItemType Directory -Path $directory -Force | Out-Null
                Copy-Item -LiteralPath $content.Source -Destination "$destination.incoming" -Force
                if ((Get-Sha256 "$destination.incoming") -ne $content.Sha) { throw "The copy does not match its manifest: $($content.Source)" }
                Move-Item -LiteralPath "$destination.incoming" -Destination $destination -Force
            }
            $list += "content $($content.Directory) $($content.Name) $($content.Sha)"
        }
        foreach ($upload in $uploads) {
            Copy-Item -LiteralPath $upload.Source -Destination (Join-Path $bundleRoot "release\$($upload.Name)")
            $list += "release $($upload.Name)"
        }
        $names = @{}
        foreach ($part in @(@{ Kind = 'extra'; Files = $ExtraFile }, @{ Kind = 'root'; Files = $RootScript })) {
            foreach ($file in $part.Files) {
                $name = Split-Path -Leaf $file
                if ($name -notmatch '^[A-Za-z0-9][A-Za-z0-9._@-]*$') { throw "Unsupported file name: $name" }
                if ($names.ContainsKey("$($part.Kind)/$name")) { throw "Two files are named $name" }
                $names["$($part.Kind)/$name"] = $true
                Copy-Item -LiteralPath $file -Destination (Join-Path $bundleRoot "$($part.Kind)\$name")
                $list += "$($part.Kind) $name"
            }
        }
        Copy-Item -LiteralPath (Join-Path $repoRoot 'scripts\stage-release-bundle.sh') -Destination (Join-Path $bundleRoot 'stage.sh')
        Write-LinuxText (Join-Path $bundleRoot 'bundle.txt') $list
        $sums = @(Get-ChildItem -LiteralPath $bundleRoot -Recurse -File | Sort-Object FullName | ForEach-Object {
                "$(Get-Sha256 $_.FullName)  $($_.FullName.Substring($bundleRoot.Length + 1).Replace('\', '/'))"
            })
        $sumsPath = Join-Path $bundleRoot 'SHA256SUMS'
        Write-LinuxText $sumsPath $sums
        Write-Host "A-NAS $productVersion ($version) is in the bundle $bundleRoot, its content in $contentRoot."
        Write-Host "SHA256SUMS SHA-256: $(Get-Sha256 $sumsPath)"
    }

    if ($bundle) {
        Write-ReleaseBundle
    } else {
        $release = "apps/a-nas/releases/$version"
        $sshOptions = @(
            '-o', 'BatchMode=yes',
            '-o', 'IdentitiesOnly=yes',
            '-o', 'StrictHostKeyChecking=yes',
            '-i', $IdentityFile
        )
        & ssh.exe @sshOptions $target "install -d -m 0750 ~/$release"
        if ($LASTEXITCODE -ne 0) { throw 'Cannot create the remote release directory.' }

        # Uploads content the NAS staging area does not hold yet and checks it there.
        foreach ($content in $stagedContents) {
            $directory = "apps/a-nas/$($content.Directory)"
            $remote = "$directory/$($content.Name)"
            & ssh.exe @sshOptions $target "printf '%s  %s\n' $($content.Sha) ~/$remote | sha256sum --check --quiet - >/dev/null 2>&1"
            if ($LASTEXITCODE -eq 0) {
                Write-Host "The NAS already holds $($content.Name)."
                continue
            }
            & ssh.exe @sshOptions $target "install -d -m 0750 ~/$directory"
            if ($LASTEXITCODE -ne 0) { throw "Cannot create ~/$directory on the NAS." }
            & scp.exe @sshOptions $content.Source "${target}:$remote.incoming"
            if ($LASTEXITCODE -ne 0) { throw "Upload failed: $($content.Source)" }
            & ssh.exe @sshOptions $target "printf '%s  %s\n' $($content.Sha) ~/$remote.incoming | sha256sum --check --quiet - && mv -f ~/$remote.incoming ~/$remote"
            if ($LASTEXITCODE -ne 0) { throw "The uploaded file does not match its manifest: $($content.Name)" }
        }
        foreach ($upload in $uploads) {
            & scp.exe @sshOptions $upload.Source "${target}:$release/$($upload.Name)"
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
    }
    Write-Host "API SHA-256:        $apiHash"
    Write-Host "Host Agent SHA-256: $agentHash"
    # Root checks these too before apt runs the packages' scripts (ADR 0031).
    Write-Host "a-nas-host SHA-256:    $((Get-FileHash -LiteralPath $hostPackage -Algorithm SHA256).Hash.ToLowerInvariant())"
    Write-Host "a-nas-console SHA-256: $((Get-FileHash -LiteralPath $consolePackage -Algorithm SHA256).Hash.ToLowerInvariant())"
} finally {
    Pop-Location
}
