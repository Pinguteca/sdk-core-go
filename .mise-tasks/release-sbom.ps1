#!/usr/bin/env pwsh
#MISE description="Generate a CycloneDX SBOM for one module (PACKAGE_DIR + SLUG env, defaults to root)"
#MISE alias="rsbom"

$ErrorActionPreference = 'Stop'

$packageDir = if ($env:PACKAGE_DIR) { $env:PACKAGE_DIR } else { '.' }
$slug = if ($env:SLUG) { $env:SLUG } else { 'root' }

$outDir = Join-Path -Path 'out' -ChildPath 'sbom'
if (-not (Test-Path -LiteralPath $outDir)) {
    New-Item -ItemType Directory -Path $outDir -Force | Out-Null
}

$outFile = Join-Path -Path $outDir -ChildPath "sdk-core-go-$slug.cdx.json"
$source = "dir:$packageDir"

& syft scan $source -o "cyclonedx-json=$outFile"
if ($LASTEXITCODE -ne 0) {
    throw "syft scan failed with exit code $LASTEXITCODE"
}
