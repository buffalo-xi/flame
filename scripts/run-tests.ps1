#Requires -Version 5.1
<#
.SYNOPSIS
    Run the test suites of the four Go modules behind one verdict.

.DESCRIPTION
    tools / nexus / sandboxd / sandboxg are separate modules wired with `replace` and there is no
    go.work, so `go test ./...` has to be run once per directory. Running them by hand makes it easy
    to forget one, and the sandboxd E2E tests need a working Docker daemon: without it they fail with
    `docker ps: exit status 1`, which looks exactly like a regression.

    This script runs each module, skips the sandboxd E2E packages when Docker is unreachable (and
    says so), and exits non-zero only when a suite really failed.

    -count=1 is passed on purpose: a cached "ok" from an earlier tree must not be able to hide a
    failure from the current one.

    The copy rules are checked at the end through `check-locale.ps1 -Gate` and fold into the same
    verdict: a Go comment or a display field that drifts back to Chinese is a source rule violation
    (AGENTS.md), and it is cheap enough to run next to the tests. It always scans all four modules,
    even when -Module narrows the test run.

.PARAMETER Module
    Restrict the run to some modules. Default: all four.

.PARAMETER RequireDocker
    Fail instead of skipping when the Docker daemon is unreachable (use this in CI, where a silent
    skip would quietly drop coverage).

.PARAMETER SkipCopyCheck
    Skip the copy-rule step (for a run that only cares about the tests, e.g. while bisecting).

.EXAMPLE
    pwsh scripts/run-tests.ps1

.EXAMPLE
    pwsh scripts/run-tests.ps1 -Module tools, nexus

.EXAMPLE
    pwsh scripts/run-tests.ps1 -RequireDocker

.EXAMPLE
    pwsh scripts/run-tests.ps1 -SkipCopyCheck
#>
param(
    [ValidateSet('tools', 'nexus', 'sandboxd', 'sandboxg')]
    [string[]]$Module = @('tools', 'nexus', 'sandboxd', 'sandboxg'),

    [switch]$RequireDocker,

    [switch]$SkipCopyCheck
)

$ErrorActionPreference = 'Stop'

$repoRoot = Split-Path -Parent $PSScriptRoot

function Test-Docker {
    # docker reports its failure on stderr and PowerShell turns native stderr into error records,
    # so the exit code is the only reliable signal here.
    $ErrorActionPreference = 'Continue'
    & docker ps --format '{{.ID}}' 2>&1 | Out-Null
    return ($LASTEXITCODE -eq 0)
}

function Get-PackageList {
    param([string]$Dir)

    $ErrorActionPreference = 'Continue'
    Push-Location $Dir
    try {
        $lines = & go list -f '{{.ImportPath}}|{{.Dir}}' ./... 2>&1
        $code = $LASTEXITCODE
    }
    finally {
        Pop-Location
    }
    if ($code -ne 0) {
        throw ("go list failed in {0}:{1}{2}" -f $Dir, [Environment]::NewLine, ($lines -join [Environment]::NewLine))
    }

    $packages = @()
    foreach ($line in $lines) {
        $parts = $line -split '\|', 2
        if ($parts.Count -eq 2) {
            $packages += @{ Import = $parts[0]; Dir = $parts[1] }
        }
    }
    return $packages
}

function Test-HasE2E {
    param([string]$Dir)

    $files = @(Get-ChildItem -Path $Dir -Filter '*_e2e_test.go' -File -ErrorAction SilentlyContinue)
    return ($files.Count -gt 0)
}

$dockerOk = Test-Docker
if (-not $dockerOk) {
    if ($RequireDocker) {
        throw 'docker is not available (docker ps failed) and -RequireDocker was given'
    }
    Write-Host 'docker is not available: sandboxd E2E packages will be skipped' -ForegroundColor Yellow
}

$failed = @()
$skipped = @()

foreach ($name in $Module) {
    $dir = Join-Path $repoRoot $name
    if (-not (Test-Path $dir)) {
        Write-Host ("  MISS  {0}  (directory not found)" -f $name) -ForegroundColor Red
        $failed += $name
        continue
    }

    $targets = @('./...')
    if ($name -eq 'sandboxd' -and -not $dockerOk) {
        $targets = @()
        foreach ($pkg in (Get-PackageList -Dir $dir)) {
            if (Test-HasE2E -Dir $pkg.Dir) {
                $skipped += $pkg.Import
            }
            else {
                $targets += $pkg.Import
            }
        }
        if ($targets.Count -eq 0) {
            Write-Host ("  skip  {0}  (every package needs docker)" -f $name) -ForegroundColor Yellow
            continue
        }
    }

    Push-Location $dir
    try {
        $output = & go test -count=1 @targets 2>&1
        $code = $LASTEXITCODE
    }
    finally {
        Pop-Location
    }

    if ($code -eq 0) {
        Write-Host ("  ok    {0}" -f $name) -ForegroundColor Green
        continue
    }

    Write-Host ("  FAIL  {0}" -f $name) -ForegroundColor Red
    $failed += $name
    # Full output is noise; the failing test names and the last words of each failure are not.
    $output | Where-Object { $_ -match '^(FAIL|--- FAIL|panic:|\s+\S+\.go:\d+:)' } |
        Select-Object -First 40 |
        ForEach-Object { Write-Host ("        {0}" -f $_) -ForegroundColor DarkGray }
}

foreach ($pkg in $skipped) {
    Write-Host ("  skip  {0}  (needs docker)" -f $pkg) -ForegroundColor Yellow
}

# Copy rules last: they do not need a database or Docker, only the sources. Run in a child process
# because check-locale.ps1 exits, and its exit code must not end this script's own verdict.
$copyOk = $true
if (-not $SkipCopyCheck) {
    $localeScript = Join-Path $PSScriptRoot 'check-locale.ps1'
    $copyOutput = & powershell -NoProfile -File $localeScript -Gate 2>&1
    if ($LASTEXITCODE -ne 0) {
        $copyOk = $false
        Write-Host '  FAIL  copy rules' -ForegroundColor Red
        $copyOutput | Where-Object { $_ -match 'GATE FAILED|^  - ' } |
            Select-Object -First 20 |
            ForEach-Object { Write-Host ("        {0}" -f $_) -ForegroundColor DarkGray }
    }
    else {
        Write-Host '  ok    copy rules' -ForegroundColor Green
    }
}

Write-Host ''
if ($failed.Count -gt 0 -or -not $copyOk) {
    if ($failed.Count -gt 0) {
        Write-Host ("{0} module(s) failed: {1}" -f $failed.Count, ($failed -join ', ')) -ForegroundColor Red
    }
    if (-not $copyOk) {
        Write-Host 'the copy rules failed; run scripts/check-locale.ps1 -Gate for the full list' -ForegroundColor Red
    }
    exit 1
}

Write-Host ("all {0} module(s) passed, copy rules clean" -f $Module.Count) -ForegroundColor Green
