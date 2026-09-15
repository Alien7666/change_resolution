<#
    Reproducible build for the ResolutionTray display tray tool.

    Runs, in order, and stops loudly at the first failure:
      1. go generate  ./cmd/resolution-tray   (embeds the Common Controls 6 / DPI manifest)
      2. go test      ./...                   (all unit + safe integration tests)
      3. go vet       ./...
      4. go build     ./cmd/resolution-tray   (GUI subsystem, CGO disabled, windows/amd64)

    Output: dist/ResolutionTray.exe (dist/ is gitignored; local artifact only).
#>

$ErrorActionPreference = 'Stop'

function Resolve-GoExecutable {
    $onPath = Get-Command 'go.exe' -ErrorAction SilentlyContinue
    if ($onPath) {
        return $onPath.Source
    }
    $fallback = 'C:\Program Files\Go\bin\go.exe'
    if (Test-Path -LiteralPath $fallback) {
        return $fallback
    }
    throw "go.exe was not found on PATH or at '$fallback'. Install Go 1.27.x and retry."
}

$go = Resolve-GoExecutable

function Invoke-Go {
    param([Parameter(Mandatory)][string[]]$Arguments)
    & $go @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "go $($Arguments -join ' ') failed with exit code $LASTEXITCODE"
    }
}

# Production builds never rely on CGo, and always target the one supported platform.
$env:CGO_ENABLED = '0'
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'

Invoke-Go @('generate', './cmd/resolution-tray')
Invoke-Go @('test', './...')
Invoke-Go @('vet', './...')

New-Item -ItemType Directory -Force -Path 'dist' | Out-Null

Invoke-Go @('build', '-trimpath', '-ldflags', '-H windowsgui -s -w', '-o', 'dist/ResolutionTray.exe', './cmd/resolution-tray')

Write-Host "Build succeeded: dist/ResolutionTray.exe"
