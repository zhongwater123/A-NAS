[CmdletBinding(SupportsShouldProcess = $true)]
param(
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[A-Za-z0-9][A-Za-z0-9.-]*$')]
    [string]$NasHost,

    [ValidateRange(1024, 65535)]
    [int]$LocalPort = 18080,

    [string]$IdentityFile = (Join-Path $env:USERPROFILE '.ssh\a-nas-dev_ed25519')
)

$ErrorActionPreference = 'Stop'
if (-not (Test-Path -LiteralPath $IdentityFile -PathType Leaf)) {
    throw "SSH identity does not exist: $IdentityFile"
}
$target = "anas-dev@$NasHost"
$forward = "127.0.0.1:${LocalPort}:127.0.0.1:8080"
if (-not $PSCmdlet.ShouldProcess($target, "open local forwarding $forward")) {
    Write-Host "Would open http://127.0.0.1:$LocalPort/ through $target"
    return
}

Write-Host "A-NAS desktop: http://127.0.0.1:$LocalPort/"
Write-Host 'Keep this window open; press Ctrl+C to close the tunnel.'
& ssh.exe `
    -N -T `
    -o BatchMode=yes `
    -o IdentitiesOnly=yes `
    -o StrictHostKeyChecking=yes `
    -o ExitOnForwardFailure=yes `
    -o ServerAliveInterval=30 `
    -o ServerAliveCountMax=3 `
    -i $IdentityFile `
    -L $forward `
    $target
if ($LASTEXITCODE -ne 0) {
    throw "SSH tunnel exited with code $LASTEXITCODE."
}
