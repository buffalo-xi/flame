#Requires -Version 5.1
<#
.SYNOPSIS
    Baseline audit for the two copy rules: English sources, and user-facing text resolved by the frontend.

.DESCRIPTION
    The two rules fail in different ways, so they are reported separately:

    1. Sources are English. Chinese inside a Go file is either a comment / diagnostic that still needs
       translating, or a business fixture (allowed: see AGENTS.md - test data such as "张三" is data,
       not prose). The report splits the two so the todo list is not padded with fixtures.

    2. Every user-visible error carries a machine-readable code. The frontend resolves `error` through
       locales/<locale>/error.json and falls back to `msg` only when no entry exists (see
       desktop/src/locales/index.js formatError). A handler that calls ErrJSON - no code at all - always
       leaks its hardcoded msg into the UI, and so does an ErrCode whose code is missing from a bundle.

    This is a baseline, not a gate. Regex cannot follow variables or helper wrappers, so the numbers are
    a lower bound and the lists are meant to be read by a human, not asserted on by CI.

.PARAMETER Module
    Limit the scan to some Go modules. Default: all four.

.PARAMETER Detailed
    Print every hit instead of only the per-file counts and the top offenders.

.EXAMPLE
    powershell -File scripts/check-locale.ps1

.EXAMPLE
    powershell -File scripts/check-locale.ps1 -Module nexus -Detailed
#>
param(
    [ValidateSet('tools', 'nexus', 'sandboxd', 'sandboxg')]
    [string[]]$Module = @('tools', 'nexus', 'sandboxd', 'sandboxg'),

    [switch]$Detailed
)

$ErrorActionPreference = 'Stop'

$repoRoot = Split-Path -Parent $PSScriptRoot
$han = '[\u4e00-\u9fff]'

function Get-GoFiles {
    param([string]$Dir)

    $files = @()
    foreach ($sub in @('', 'internal', 'cmd')) {
        $path = Join-Path $Dir $sub
        if (Test-Path $path) {
            $files += @(Get-ChildItem -Path $path -Recurse -Filter *.go -File -ErrorAction SilentlyContinue)
        }
    }
    # The three sub-roots overlap (Dir itself is recursive), so de-duplicate by full name.
    return ($files | Sort-Object FullName -Unique)
}

function Flatten-Locale {
    param($Object, [string]$Prefix = '')

    foreach ($prop in $Object.PSObject.Properties) {
        if ($Prefix) { $key = "$Prefix.$($prop.Name)" } else { $key = $prop.Name }
        if ($prop.Value -is [System.Management.Automation.PSCustomObject]) {
            Flatten-Locale -Object $prop.Value -Prefix $key
        }
        else {
            $key
        }
    }
}

$byModule = @{}
$commentLines = @{}     # full name -> count of // lines containing Chinese
$otherLines = @{}       # full name -> count of non-comment lines containing Chinese
$msgChinese = @{}       # full name -> count of ErrCode lines containing Chinese
$codes = @{}            # error code -> first file that emits it
$errJSON = @{}          # full name -> count
$errCodeTotal = 0
$displayHits = @()

foreach ($name in $Module) {
    $dir = Join-Path $repoRoot $name
    if (-not (Test-Path $dir)) {
        Write-Host ("module not found: {0}" -f $name) -ForegroundColor Red
        continue
    }

    $files = Get-GoFiles -Dir $dir
    $byModule[$name] = $files.Count

    foreach ($file in $files) {
        # UTF8 is not optional: Windows PowerShell 5.1 reads as ANSI by default, which turns the
        # Chinese into mojibake and, in the bundles, breaks the JSON quotes outright.
        $raw = Get-Content -Path $file.FullName -Raw -Encoding UTF8
        if (-not $raw) { continue }
        $rel = $file.FullName.Substring($repoRoot.Length + 1)

        if ($raw -match $han) {
            $c = 0
            $o = 0
            foreach ($line in ($raw -split "`r?`n")) {
                if ($line -notmatch $han) { continue }
                if ($line -match '^\s*//') { $c++ } else { $o++ }
            }
            if ($c -gt 0) { $commentLines[$rel] = $c }
            if ($o -gt 0) { $otherLines[$rel] = $o }
        }

        $jsonCount = ([regex]::Matches($raw, 'xhttp\.ErrJSON\(')).Count
        if ($jsonCount -gt 0) { $errJSON[$rel] = $jsonCount }

        $errCodeTotal += ([regex]::Matches($raw, 'xhttp\.ErrCode')).Count

        foreach ($line in ($raw -split "`r?`n")) {
            if ($line -match 'xhttp\.ErrCode[a-zA-Z]*\(' -and $line -match $han) {
                if ($msgChinese.ContainsKey($rel)) { $msgChinese[$rel]++ } else { $msgChinese[$rel] = 1 }
            }
        }

        # Codes travel as the second argument: ErrCode(c, "agent.not_found", ...).
        foreach ($m in [regex]::Matches($raw, 'xhttp\.ErrCode(?:Data|Quota)?\(\s*c\s*,\s*"([A-Za-z0-9_.\-]+)"')) {
            $code = $m.Groups[1].Value
            if (-not $codes.ContainsKey($code)) { $codes[$code] = $rel }
        }

        # Backend text handed to the UI as a display field rather than through the error envelope.
        $lineNo = 0
        foreach ($line in ($raw -split "`r?`n")) {
            $lineNo++
            if ($line -match '^\s*//') { continue }
            if ($line -match '(Label|Detail|reason|Reason)\s*[:=]\s*"[^"]*' + $han) {
                $displayHits += ("{0}:{1}  {2}" -f $rel, $lineNo, $line.Trim())
            }
        }
    }
}

Write-Host ''
Write-Host 'locale audit' -ForegroundColor Cyan
Write-Host ("  modules: {0}" -f (($byModule.GetEnumerator() | Sort-Object Name | ForEach-Object { "$($_.Name)=$($_.Value) files" }) -join ', '))

function Show-Ranking {
    param([hashtable]$Table, [string]$Title, [string]$Unit)

    $sum = 0
    foreach ($v in $Table.Values) { $sum += $v }
    Write-Host ''
    Write-Host ("{0}: {1} {2} in {3} files" -f $Title, $sum, $Unit, $Table.Count) -ForegroundColor Yellow
    $top = $Table.GetEnumerator() | Sort-Object Value -Descending | Select-Object -First 10
    foreach ($e in $top) {
        Write-Host ("  {0,5}  {1}" -f $e.Value, $e.Key) -ForegroundColor DarkGray
    }
    if ($Detailed -and $Table.Count -gt 10) {
        $Table.GetEnumerator() | Sort-Object Value -Descending | Select-Object -Skip 10 |
            ForEach-Object { Write-Host ("  {0,5}  {1}" -f $_.Value, $_.Key) -ForegroundColor DarkGray }
    }
}

Show-Ranking -Table $commentLines -Title '1. Chinese comments' -Unit 'lines'
Show-Ranking -Table $otherLines -Title '2. Chinese outside comments (strings, diagnostics, fixtures)' -Unit 'lines'
Show-Ranking -Table $errJSON -Title '3. ErrJSON calls (no error code, msg always reaches the UI)' -Unit 'calls'
Show-Ranking -Table $msgChinese -Title '4. ErrCode calls whose line contains Chinese (msg is meant to be English)' -Unit 'calls'

Write-Host ''
Write-Host ("5. error codes: {0} distinct extracted from {1} ErrCode mentions" -f $codes.Count, $errCodeTotal) -ForegroundColor Yellow

$localeDir = Join-Path $repoRoot 'desktop/src/locales'
$bundles = @{}
foreach ($locale in @('en', 'zh-CN')) {
    $path = Join-Path $localeDir "$locale/error.json"
    if (-not (Test-Path $path)) {
        Write-Host ("  missing bundle: {0}" -f $path) -ForegroundColor Red
        continue
    }
    $flat = @{}
    foreach ($key in (Flatten-Locale -Object (Get-Content -Path $path -Raw -Encoding UTF8 | ConvertFrom-Json))) {
        # The bundle root is "error", the emitted codes are "agent.not_found".
        $flat[($key -replace '^error\.', '')] = $true
    }
    $bundles[$locale] = $flat
}

foreach ($locale in $bundles.Keys) {
    $flat = $bundles[$locale]
    $missing = @($codes.Keys | Where-Object { -not $flat.ContainsKey($_) } | Sort-Object)
    Write-Host ("  {0}: {1} of {2} codes have no entry" -f $locale, $missing.Count, $codes.Count) -ForegroundColor Yellow
    $limit = 20
    if ($Detailed) { $limit = $missing.Count }
    foreach ($code in ($missing | Select-Object -First $limit)) {
        Write-Host ("    {0}  (first used in {1})" -f $code, $codes[$code]) -ForegroundColor DarkGray
    }
    if ($missing.Count -gt $limit) {
        Write-Host ("    ... {0} more (-Detailed)" -f ($missing.Count - $limit)) -ForegroundColor DarkGray
    }
}

$allLocaleKeys = @{}
foreach ($locale in $bundles.Keys) {
    foreach ($key in $bundles[$locale].Keys) { $allLocaleKeys[$key] = $true }
}
$unused = @($allLocaleKeys.Keys | Where-Object { -not $codes.ContainsKey($_) } | Sort-Object)
Write-Host ("  entries never emitted: {0}" -f $unused.Count) -ForegroundColor Yellow
if ($Detailed) {
    foreach ($key in $unused) { Write-Host ("    {0}" -f $key) -ForegroundColor DarkGray }
}

Write-Host ''
Write-Host ("6. Chinese display fields returned to the UI: {0} hit(s)" -f $displayHits.Count) -ForegroundColor Yellow
$shown = $displayHits
if (-not $Detailed) { $shown = $displayHits | Select-Object -First 15 }
foreach ($hit in $shown) { Write-Host ("  {0}" -f $hit) -ForegroundColor DarkGray }
if ($displayHits.Count -gt $shown.Count) {
    Write-Host ("  ... {0} more (-Detailed)" -f ($displayHits.Count - $shown.Count)) -ForegroundColor DarkGray
}

Write-Host ''
Write-Host 'baseline only: regex cannot follow variables or helper wrappers, so treat the numbers as a lower bound' -ForegroundColor DarkGray
