# Kept for one version under its old name: scripts/deploy-media-pool.ps1
# -Kind screensavers does the same.
[CmdletBinding(SupportsShouldProcess = $true, ConfirmImpact = 'Medium')]
param(
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[A-Za-z0-9][A-Za-z0-9.-]*$')]
    [string]$NasHost,

    [Parameter(Mandatory = $true)]
    [string[]]$Video,

    [string]$IdentityFile
)

$ErrorActionPreference = 'Stop'
& (Join-Path $PSScriptRoot 'deploy-media-pool.ps1') -Kind screensavers @PSBoundParameters
