$ErrorActionPreference = 'Stop'
$coreRoot = $PSScriptRoot
$goVersion = (& go version).Trim()
if ($goVersion -notmatch '^go version go1\.20\.14 windows/(?:386|amd64)$') {
    throw "OPL Core release builds require Go 1.20.14; found: $goVersion"
}

$dist = Join-Path $coreRoot 'dist\opl-core-windows-386'
$cache = Join-Path $coreRoot '.cache\wintun-0.14.1'
New-Item -ItemType Directory -Force -Path $dist, $cache | Out-Null
$wintun = Join-Path $cache 'wintun.dll'
$expected = 'd694fa46ab4cfebcb2632d094c7aa97278eef2f8052438621766d863ae98a931'
if (!(Test-Path -LiteralPath $wintun) -or (Get-FileHash -Algorithm SHA256 -LiteralPath $wintun).Hash.ToLowerInvariant() -ne $expected) {
    $zip = Join-Path $cache 'wintun.zip'
    $unpack = Join-Path $cache 'unpack'
    Invoke-WebRequest -Uri 'https://www.wintun.net/builds/wintun-0.14.1.zip' -OutFile $zip
    Expand-Archive -LiteralPath $zip -DestinationPath $unpack -Force
    Copy-Item -LiteralPath (Join-Path $unpack 'wintun\bin\x86\wintun.dll') -Destination $wintun -Force
}
if ((Get-FileHash -Algorithm SHA256 -LiteralPath $wintun).Hash.ToLowerInvariant() -ne $expected) {
    throw 'Wintun x86 SHA-256 mismatch'
}

$env:GOOS = 'windows'
$env:GOARCH = '386'
$env:CGO_ENABLED = '0'
$env:GOCACHE = Join-Path $coreRoot '.gocache'
Push-Location $coreRoot
try {
    & go build -mod=readonly -trimpath -ldflags '-s -w' -o (Join-Path $dist 'opl-core.exe') ./cmd/opl-core
    if ($LASTEXITCODE -ne 0) { throw "go build failed with exit code $LASTEXITCODE" }
} finally {
    Pop-Location
}
Copy-Item -LiteralPath $wintun -Destination (Join-Path $dist 'wintun.dll') -Force
Copy-Item -LiteralPath (Join-Path $coreRoot 'web') -Destination $dist -Recurse -Force
Copy-Item -LiteralPath (Join-Path $coreRoot 'README.md') -Destination $dist -Force
Copy-Item -LiteralPath (Join-Path $coreRoot 'release-components.json') -Destination $dist -Force
Write-Host "Built: $dist"
