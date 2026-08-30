# Builds DoDaemon as a single-file, dependency-free native Windows GUI exe,
# named "DoDaemon v<Version>.exe". The web UI (HTML/CSS/JS) is embedded via
# go:embed and the native GUI (internal/nativeui) is linked in via
# -H=windowsgui, so this one file is the entire deployable artifact — no
# console window ever, no separate web server, no Node.js, no DLLs beyond
# what's already on any Windows machine (see docs/PLAN.md §9.1). There is
# no CLI: running the exe, however it's launched, opens the GUI.
#
# Run `go run ./cmd/icongen` first if internal/icons/icons.go changed, so
# assets/icon.ico is current, then from cmd/dodaemon:
#   go-winres simply --icon ../../assets/icon.ico --manifest gui `
#     --file-description "DoDaemon - 통합 FTP/TFTP/Syslog 서버" `
#     --product-name "DoDaemon" --copyright "DoDaemon" `
#     --original-filename "DoDaemon.exe" `
#     --file-version "<Version>.0" --product-version "<Version>.0" --arch amd64
# to regenerate rsrc_windows_amd64.syso (go-winres: go install github.com/tc-hib/go-winres@latest)
# before rebuilding, so the exe's icon/version resource matches.
#
# Usage: powershell -File scripts/build.ps1 [-Version "2.3.1"]

param(
    [string]$Version = "2.3.1"
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

New-Item -ItemType Directory -Force -Path "dist" | Out-Null
Get-ChildItem "dist" -Filter "DoDaemon v*.exe" | Remove-Item -Force

$env:CGO_ENABLED = "0"
$env:GOOS = "windows"
$env:GOARCH = "amd64"

$outFile = "dist/DoDaemon v$Version.exe"
Write-Host "Building $outFile ..."
go build -trimpath -ldflags "-s -w -H=windowsgui -X main.version=$Version" -o $outFile ./cmd/dodaemon

$size = (Get-Item $outFile).Length / 1MB
Write-Host ("Done: {0} ({1:N1} MB)" -f $outFile, $size)
Write-Host "This single file requires nothing else installed — copy it anywhere and double-click it."
