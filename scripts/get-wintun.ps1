$ErrorActionPreference = "Stop"
$root = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
$destDir = Join-Path $root "internal\tun"
$zip = Join-Path $env:TEMP "wintun-0.14.1.zip"
$unpack = Join-Path $env:TEMP "wintun-0.14.1"
Invoke-WebRequest -Uri "https://www.wintun.net/builds/wintun-0.14.1.zip" -OutFile $zip
if (Test-Path $unpack) { Remove-Item -Recurse -Force $unpack }
Expand-Archive -Path $zip -DestinationPath $unpack -Force
$dll = Get-ChildItem -Path $unpack -Recurse -Filter "wintun.dll" |
    Where-Object { $_.FullName -match "amd64" } |
    Select-Object -First 1
if (-not $dll) { throw "wintun.dll для amd64 не найден в архиве" }
$expected = "e5da8447dc2c320edc0fc52fa01885c103de8c118481f683643cacc3220dafce"
$actual = (Get-FileHash -Algorithm SHA256 $dll.FullName).Hash.ToLower()
if ($actual -ne $expected) { throw "wintun.dll не совпал по SHA-256: $actual" }
$sig = Get-AuthenticodeSignature $dll.FullName
if ($sig.Status -ne "Valid" -or $sig.SignerCertificate.Subject -notmatch "WireGuard LLC") { throw "у wintun.dll нет действительной подписи WireGuard LLC" }
Copy-Item $dll.FullName (Join-Path $destDir "wintun.dll") -Force
Write-Output "wintun.dll скопирован в internal\tun"
