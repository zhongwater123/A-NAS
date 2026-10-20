<#
.SYNOPSIS
  在本机起一个 Qdrant（Windows 单文件二进制，不需要 Docker）。

.DESCRIPTION
  第一次运行会从 GitHub Releases 下载 qdrant-x86_64-pc-windows-msvc.zip 到 ai/finetune/qdrant/bin，
  数据目录是 ai/finetune/qdrant/storage（两者都在 .gitignore 里）。之后直接复用已下载的 exe。
  启动后：REST http://127.0.0.1:6333 ，控制台 http://127.0.0.1:6333/dashboard 。

  有 Docker 的机器也可以不用本脚本：
    docker run -d --name qdrant -p 6333:6333 -v "$PWD/ai/finetune/qdrant/storage:/qdrant/storage" qdrant/qdrant

.PARAMETER Version
  Qdrant 版本号（如 1.15.1）。默认 latest，通过 GitHub API 解析。
.PARAMETER Port
  REST 端口，默认 6333（gRPC 固定为 Port+1）。
.PARAMETER Background
  后台启动（Start-Process），脚本立即返回；不加则前台运行，Ctrl+C 停止。
.PARAMETER Stop
  停掉本脚本启动的 qdrant.exe。

.EXAMPLE
  .iinetune\qdrant-up.ps1                 # 前台起，看日志
  .iinetune\qdrant-up.ps1 -Background     # 后台起
  .iinetune\qdrant-up.ps1 -Stop
#>
[CmdletBinding()]
param(
    [string]$Version = "latest",
    [int]$Port = 6333,
    [switch]$Background,
    [switch]$Stop
)

$ErrorActionPreference = "Stop"
$root = Join-Path $PSScriptRoot "qdrant"
$binDir = Join-Path $root "bin"
$storage = Join-Path $root "storage"
$exe = Join-Path $binDir "qdrant.exe"

if ($Stop) {
    $procs = Get-Process qdrant -ErrorAction SilentlyContinue | Where-Object { $_.Path -eq $exe }
    if (-not $procs) { Write-Host "没有由本脚本启动的 qdrant 在跑。"; return }
    $procs | Stop-Process
    Write-Host "已停止 qdrant（$($procs.Count) 个进程）。"
    return
}

# 已经有一个在该端口上就不重复起
try {
    $probe = Invoke-RestMethod -Uri "http://127.0.0.1:$Port/" -TimeoutSec 2
    Write-Host "端口 $Port 上已有 Qdrant 在跑（版本 $($probe.version)），不再启动。"
    return
} catch { }

New-Item -ItemType Directory -Force $binDir, $storage | Out-Null

if (-not (Test-Path $exe)) {
    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
    $headers = @{ "User-Agent" = "anas-agent-qdrant-up" }
    if ($Version -eq "latest") {
        Write-Host "解析最新版本号……"
        $rel = Invoke-RestMethod -Uri "https://api.github.com/repos/qdrant/qdrant/releases/latest" -Headers $headers
        $tag = $rel.tag_name
    } else {
        $tag = if ($Version.StartsWith("v")) { $Version } else { "v$Version" }
        $rel = Invoke-RestMethod -Uri "https://api.github.com/repos/qdrant/qdrant/releases/tags/$tag" -Headers $headers
    }
    $asset = $rel.assets | Where-Object { $_.name -like "qdrant-x86_64-pc-windows-msvc*.zip" } | Select-Object -First 1
    if (-not $asset) {
        $names = ($rel.assets | ForEach-Object { $_.name }) -join "`n  "
        throw "版本 $tag 没有找到 Windows 资产 qdrant-x86_64-pc-windows-msvc*.zip。该版本的资产有：`n  $names"
    }
    $zip = Join-Path $binDir $asset.name
    Write-Host ("下载 {0} ({1:N0} MB)……" -f $asset.name, ($asset.size / 1MB))
    Invoke-WebRequest -Uri $asset.browser_download_url -OutFile $zip -Headers $headers
    Expand-Archive -Path $zip -DestinationPath $binDir -Force
    Remove-Item $zip
    $found = Get-ChildItem $binDir -Recurse -Filter qdrant.exe | Select-Object -First 1
    if (-not $found) { throw "压缩包里没有 qdrant.exe，请检查 $binDir" }
    if ($found.FullName -ne $exe) { Move-Item $found.FullName $exe -Force }
    Set-Content -Path (Join-Path $binDir "VERSION") -Value $tag -Encoding ascii
    Write-Host "已安装 Qdrant $tag 到 $exe"
}

# 通过环境变量配置（Qdrant 支持 QDRANT__节__键 这种写法）
$env:QDRANT__STORAGE__STORAGE_PATH = $storage
$env:QDRANT__STORAGE__SNAPSHOTS_PATH = Join-Path $root "snapshots"
$env:QDRANT__SERVICE__HTTP_PORT = "$Port"
$env:QDRANT__SERVICE__GRPC_PORT = "$($Port + 1)"
$env:QDRANT__SERVICE__HOST = "127.0.0.1"
$env:QDRANT__TELEMETRY_DISABLED = "true"

$ver = if (Test-Path (Join-Path $binDir "VERSION")) { (Get-Content (Join-Path $binDir "VERSION")).Trim() } else { "?" }
Write-Host "启动 Qdrant $ver  REST http://127.0.0.1:$Port  数据 $storage"
Write-Host "调用方这样接：export ANAS_QDRANT_URL=http://127.0.0.1:$Port"

if ($Background) {
    $p = Start-Process -FilePath $exe -WorkingDirectory $root -PassThru -WindowStyle Hidden
    $ok = $false
    for ($i = 0; $i -lt 30 -and -not $ok; $i++) {
        Start-Sleep -Milliseconds 500
        try { Invoke-RestMethod -Uri "http://127.0.0.1:$Port/" -TimeoutSec 2 | Out-Null; $ok = $true } catch { }
    }
    if ($ok) { Write-Host "Qdrant 已就绪（PID $($p.Id)）。停止：.iinetune\qdrant-up.ps1 -Stop" }
    else { Write-Warning "15 秒内没探测到服务，看看进程是否还在：Get-Process qdrant" }
} else {
    & $exe
}
