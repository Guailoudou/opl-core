[CmdletBinding()]
param(
    [ValidateSet('all', 'windows-386', 'windows-amd64', 'windows-arm64', 'linux-386', 'linux-amd64', 'linux-arm64', 'linux-mips', 'linux-mips64', 'darwin-amd64', 'darwin-arm64')]
    [string[]]$Target = @('all')
)

$ErrorActionPreference = 'Stop'
$coreRoot = $PSScriptRoot
$goVersion = (& go version).Trim()
if ($goVersion -notmatch '^go version go1\.20\.14 ') {
    throw "OPL Core builds require Go 1.20.14; found: $goVersion"
}

$matrix = @(
    @{ OS = 'windows'; Arch = '386';   Label = 'windows-386';   Ext = '.exe'; Wintun = 'x86' }
    @{ OS = 'windows'; Arch = 'amd64'; Label = 'windows-amd64'; Ext = '.exe'; Wintun = 'amd64' }
    @{ OS = 'windows'; Arch = 'arm64'; Label = 'windows-arm64'; Ext = '.exe'; Wintun = 'arm64' }
    @{ OS = 'linux';   Arch = '386';   Label = 'linux-386';     Ext = '' }
    @{ OS = 'linux';   Arch = 'amd64'; Label = 'linux-amd64';   Ext = '' }
    @{ OS = 'linux';   Arch = 'arm64'; Label = 'linux-arm64';   Ext = '' }
    @{ OS = 'linux';   Arch = 'mips';  Label = 'linux-mips';    Ext = '' }
    @{ OS = 'linux';   Arch = 'mips64';Label = 'linux-mips64';  Ext = '' }
    @{ OS = 'darwin';  Arch = 'amd64'; Label = 'darwin-amd64';  Ext = '' }
    @{ OS = 'darwin';  Arch = 'arm64'; Label = 'darwin-arm64';  Ext = '' }
)
$selected = if ($Target -contains 'all') { $matrix } else { $matrix | Where-Object Label -in $Target }
$components = Get-Content -LiteralPath (Join-Path $coreRoot 'release-components.json') -Raw | ConvertFrom-Json
$wintunCache = Join-Path $coreRoot ".cache\wintun-$($components.wintun.version)"

if ($selected.OS -contains 'windows') {
    $wintunZip = Join-Path $wintunCache 'wintun.zip'
    $wintunRoot = Join-Path $wintunCache 'unpack\wintun'
    New-Item -ItemType Directory -Force -Path $wintunCache | Out-Null
    if (!(Test-Path -LiteralPath $wintunRoot)) {
        if (!(Test-Path -LiteralPath $wintunZip)) {
            Invoke-WebRequest -Uri $components.wintun.source -OutFile $wintunZip
        }
        Expand-Archive -LiteralPath $wintunZip -DestinationPath (Join-Path $wintunCache 'unpack') -Force
    }
}

$oldEnvironment = @{
    GOOS = $env:GOOS
    GOARCH = $env:GOARCH
    CGO_ENABLED = $env:CGO_ENABLED
    GOCACHE = $env:GOCACHE
}

Push-Location $coreRoot
try {
    foreach ($item in $selected) {
        $output = Join-Path $coreRoot "dist\opl-core-$($item.Label)"
        New-Item -ItemType Directory -Force -Path $output | Out-Null
        $env:GOOS = $item.OS
        $env:GOARCH = $item.Arch
        $env:CGO_ENABLED = '0'
        $env:GOCACHE = Join-Path $coreRoot '.gocache'

        & go build -mod=readonly -trimpath -ldflags '-s -w' -o (Join-Path $output "opl-core$($item.Ext)") ./cmd/opl-core
        if ($LASTEXITCODE -ne 0) { throw "Build failed: $($item.Label)" }

        Copy-Item -LiteralPath (Join-Path $coreRoot 'web') -Destination $output -Recurse -Force
        Copy-Item -LiteralPath (Join-Path $coreRoot 'README.md'), (Join-Path $coreRoot 'release-components.json'), (Join-Path $coreRoot 'internal\openp2pengine\NOTICE.md') -Destination $output -Force

        if ($item.OS -eq 'windows') {
            $wintun = Join-Path $wintunCache "unpack\wintun\bin\$($item.Wintun)\wintun.dll"
            $expected = $components.wintun.sha256.PSObject.Properties[$item.Arch].Value
            if (!(Test-Path -LiteralPath $wintun) -or (Get-FileHash -Algorithm SHA256 -LiteralPath $wintun).Hash.ToLowerInvariant() -ne $expected) {
                throw "Wintun SHA-256 mismatch: $($item.Label)"
            }
            Copy-Item -LiteralPath $wintun -Destination $output -Force
        }
        Write-Host "Built: $output"
    }
} finally {
    Pop-Location
    $env:GOOS = $oldEnvironment.GOOS
    $env:GOARCH = $oldEnvironment.GOARCH
    $env:CGO_ENABLED = $oldEnvironment.CGO_ENABLED
    $env:GOCACHE = $oldEnvironment.GOCACHE
}
