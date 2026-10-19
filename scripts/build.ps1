$ErrorActionPreference = "Stop"
$root = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
$go = "C:\Program Files\Go\bin\go.exe"
Set-Location $root
if (-not (Test-Path (Join-Path $root "internal/tun/wintun.dll"))) { & "$PSScriptRoot\get-wintun.ps1" }
& $go test ./...
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
New-Item -ItemType Directory -Force -Path (Join-Path $root "admin") | Out-Null
$adminFlags = "-s -w -H windowsgui -X main.defaultMode=admin"
if ($env:LOKALKA_DEFAULT_SERVER) { $adminFlags += " -X main.defaultServer=$($env:LOKALKA_DEFAULT_SERVER)" }
& $go build -trimpath -ldflags $adminFlags -o (Join-Path $root "admin/Lokalka.exe") ./cmd/lokalka
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
$env:GOOS = "linux"
$env:GOARCH = "amd64"
$env:CGO_ENABLED = "0"
& $go build -trimpath -ldflags "-s -w" -o (Join-Path $root "admin/lokalka-linux-amd64") ./cmd/lokalka
$code = $LASTEXITCODE
Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED
if ($code -ne 0) { exit $code }
