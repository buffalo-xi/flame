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
       desktop/src/locales/index.js formatError). A handler without a code always leaks its hardcoded msg
       into the UI, and so does an ErrCode whose code is missing from a bundle. The ErrJSON helper was
       removed from tools/xhttp in 2026-11; section 3 below stays as a tripwire in case it comes back.

    3. The other direction: user-facing copy that never reaches a locale bundle. Section 7 scans the
       desktop sources with comments stripped, because "the file contains N lines of Chinese" counts
       design notes as if they were copy and makes the remaining L4 work look an order of magnitude
       bigger than it is. What is left is either copy that needs a key, or a business value that must
       not be translated (see the i18n plan, pitfall 2) - both are worth a human look, for opposite
       reasons.

    4. Finding codes and their wording. The canvas prints flow.check.code.<code> for every finding
       shared/flowcheck returns; a code added there without a bundle entry silently degrades to the
       generic "unknown" line, and a bundle entry no rule emits is a stale sentence. Section 8 compares
       the two in both directions.

    5. Node types, their doc page and their palette label. Every type in knownTypes is reachable from the
       palette and from the create tools, so it needs a page under references/nodes (its own, or one of
       the two shared pages) and a flow.nodeType.<type> label. Section 9 checks presence only - whether
       the page is *right* is the node audit, not something a regex can answer.

    This is a baseline, not a gate. Regex cannot follow variables or helper wrappers, so the numbers are
    a lower bound and the lists are meant to be read by a human, not asserted on by CI.

    -Gate narrows that to the checks that are unambiguous and finishes with an exit code: Chinese comments
    anywhere, Chinese outside comments in a Go file that is not a test, un-bundled Chinese copy in the
    desktop sources, ErrJSON, a Chinese msg on an ErrCode line, a finding code without wording (or a
    wording without a code), and a node type without a doc page or palette label. Test fixtures are
    allowed - they are data, and the whole point of the split above is that they are not a todo list. The
    remaining exceptions are the short allowlists in the script, each with its reason.

.PARAMETER Module
    Limit the scan to some Go modules. Default: all four.

.PARAMETER Detailed
    Print every hit instead of only the per-file counts and the top offenders.

.PARAMETER Gate
    Exit non-zero when a rule that can be decided by regex is violated. Intended for CI and for a
    pre-commit run; running without it only prints the report.

.EXAMPLE
    powershell -File scripts/check-locale.ps1

.EXAMPLE
    powershell -File scripts/check-locale.ps1 -Module nexus -Detailed
#>
param(
    [ValidateSet('tools', 'nexus', 'sandboxd', 'sandboxg', 'browserd', 'browserg')]
    [string[]]$Module = @('tools', 'nexus', 'sandboxd', 'sandboxg', 'browserd', 'browserg'),

    [switch]$Detailed,

    [switch]$Gate
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
                if ($line -match '^\s*//') {
                    # A comment often quotes the data it describes: the `系统:` line of a capability probe, the DOM text of a
                    # button, a byte offset inside "中". That is a fixture living in a comment, not prose to translate, so
                    # quoted spans are dropped before counting - at the cost of missing a real Chinese sentence in quotes.
                    $quoted = [regex]::Replace($line, '"[^"]*"|`[^`]*`', '')
                    if ($quoted -match $han) { $c++ }
                }
                else { $o++ }
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
        # Only exported-style fields (Label / Detail / Reason) and JSON keys count: a lowercase local such as
        # `reason = "未启用"` is prompt-building text for the model, not something a user ever sees.
        # Test files are skipped here: a JSON fixture with a Chinese label is data, not a display field.
        $lineNo = 0
        if ($rel -notlike '*_test.go') {
            foreach ($line in ($raw -split "`r?`n")) {
                $lineNo++
                if ($line -match '^\s*//') { continue }
                $displayHit = ($line -cmatch '(Label|Detail|Reason)\s*[:=]\s*"[^"]*' + $han) -or
                              ($line -match '"(label|detail|reason|display|message)"\s*:\s*"[^"]*' + $han)
                if ($displayHit) {
                    $displayHits += ("{0}:{1}  {2}" -f $rel, $lineNo, $line.Trim())
                }
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
Show-Ranking -Table $errJSON -Title '3. ErrJSON calls (helper removed; expected to stay at 0)' -Unit 'calls'
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

# "entry never emitted" only means something when every module was scanned: a partial scan
# sees a subset of the codes and would report the rest as dead.
if ($Module.Count -eq 6) {
    $allLocaleKeys = @{}
    foreach ($locale in $bundles.Keys) {
        foreach ($key in $bundles[$locale].Keys) { $allLocaleKeys[$key] = $true }
    }
    $unused = @($allLocaleKeys.Keys | Where-Object { -not $codes.ContainsKey($_) } | Sort-Object)
    Write-Host ("  entries never emitted: {0} (dynamic codes such as quota.* land here; verify before pruning)" -f $unused.Count) -ForegroundColor Yellow
    if ($Detailed) {
        foreach ($key in $unused) { Write-Host ("    {0}" -f $key) -ForegroundColor DarkGray }
    }
}
else {
    Write-Host '  entries never emitted: not computed (partial module scan)' -ForegroundColor DarkGray
}

Write-Host ''
Write-Host ("6. Chinese display fields returned to the UI: {0} hit(s)" -f $displayHits.Count) -ForegroundColor Yellow
$shown = $displayHits
if (-not $Detailed) { $shown = $displayHits | Select-Object -First 15 }
foreach ($hit in $shown) { Write-Host ("  {0}" -f $hit) -ForegroundColor DarkGray }
if ($displayHits.Count -gt $shown.Count) {
    Write-Host ("  ... {0} more (-Detailed)" -f ($displayHits.Count - $shown.Count)) -ForegroundColor DarkGray
}

# ---------------------------------------------------------------- 7. desktop copy
# Chinese left in a .vue / .js file is either copy that never reached a bundle (a defect) or a business
# value that must not be translated (a hazard). Comments are stripped first: the old way of counting
# "lines containing Chinese" mixed the two and made the remaining L4 work look far bigger than it is.
$desktopDir = Join-Path $repoRoot 'desktop/src'
$desktopCopy = @{}
$desktopComments = @{}
$desktopScanned = 0
if (Test-Path $desktopDir) {
    $files = @(Get-ChildItem -Path $desktopDir -Recurse -File -Include *.vue, *.js |
        Where-Object { $_.FullName -notlike '*\node_modules\*' })
    $desktopScanned = $files.Count
    foreach ($file in $files) {
        $raw = Get-Content -Path $file.FullName -Raw -Encoding UTF8
        if (-not $raw -or $raw -notmatch $han) { continue }
        # Block comments, HTML comments, then line comments. A `//` inside a URL truncates the rest of
        # that line, which can only hide a hit - another reason this stays a lower bound.
        $code = [regex]::Replace($raw, '(?s)/\*.*?\*/', '')
        $code = [regex]::Replace($code, '(?s)<!--.*?-->', '')
        $code = [regex]::Replace($code, '(?m)//.*$', '')
        $n = 0
        foreach ($line in ($code -split "`r?`n")) {
            if ($line -match $han) { $n++ }
        }
        if ($n -gt 0) {
            $desktopCopy[$file.FullName.Substring($repoRoot.Length + 1)] = $n
        }
        # Comments are reported separately and are not gated: the rule is "a comment you touch is English", not a
        # wholesale migration, because the backlog is two orders of magnitude larger than the Go one.
        $m = 0
        foreach ($line in ($raw -split "`r?`n")) {
            if ($line -notmatch $han) { continue }
            if ($line -match '^\s*(//|/\*|\*|<!--)') { $m++ }
        }
        if ($m -gt 0) {
            $desktopComments[$file.FullName.Substring($repoRoot.Length + 1)] = $m
        }
    }
    Show-Ranking -Table $desktopCopy -Title ("7. Chinese outside comments in desktop sources ({0} files scanned)" -f $desktopScanned) -Unit 'lines'
    Show-Ranking -Table $desktopComments -Title '7b. Chinese comments in desktop sources (not gated: convert the ones you touch)' -Unit 'lines'
}
else {
    Write-Host ''
    Write-Host '7. desktop sources: not found (skipped)' -ForegroundColor DarkGray
}

# ---------------------------------------------------------------- 8. finding codes vs bundles
# shared/flowcheck is the one rule set behind the agent tools, POST /flows/validate and the canvas badge.
# Every code it emits reaches the user as flow.check.code.<code>; without a bundle entry the badge falls
# back to "unknown", which is silent degradation rather than a failure. The reverse is also a defect: a
# wording no rule emits is a sentence describing a check that no longer exists.
$flowcheckFile = Join-Path $repoRoot 'nexus/shared/flowcheck/check.go'
$findingCodes = @{}
if (Test-Path $flowcheckFile) {
    $flowcheckRaw = Get-Content -Path $flowcheckFile -Raw -Encoding UTF8
    # Matches the const block and the standalone declarations (CodeOnErrorCodeUnused is one), and nothing that
    # merely starts with "Code" - the value has to be a lower-case code.
    foreach ($m in [regex]::Matches($flowcheckRaw, '\bCode\w+\s*=\s*"([a-z_]+)"')) {
        $findingCodes[$m.Groups[1].Value] = $true
    }
}
$findingCodeFallback = @(
    # the front end's own fallback key, not a Go constant
    'unknown'
)
$findingProblems = New-Object System.Collections.Generic.List[string]
$findingBundleCounts = @()
foreach ($locName in @('en', 'zh-CN')) {
    $bundlePath = Join-Path $repoRoot ("desktop/src/locales/{0}/flow.json" -f $locName)
    if (-not (Test-Path $bundlePath)) {
        $findingProblems.Add(("desktop/src/locales/{0}/flow.json is missing" -f $locName))
        continue
    }
    $bundle = Get-Content -Path $bundlePath -Raw -Encoding UTF8 | ConvertFrom-Json
    $keys = @($bundle.flow.check.code.PSObject.Properties.Name)
    $findingBundleCounts += ("{0}={1}" -f $locName, $keys.Count)
    foreach ($code in ($findingCodes.Keys | Sort-Object)) {
        if ($keys -notcontains $code) {
            $findingProblems.Add(("flow.check.code.{0} has no wording in {1}" -f $code, $locName))
        }
    }
    foreach ($key in $keys) {
        if ($findingCodes.ContainsKey($key)) { continue }
        if ($findingCodeFallback -contains $key) { continue }
        $findingProblems.Add(("flow.check.code.{0} in {1} matches no finding code" -f $key, $locName))
    }
}
Write-Host ''
Write-Host ("8. finding codes: {0} emitted, bundles {1}" -f $findingCodes.Count, ($findingBundleCounts -join ', ')) -ForegroundColor Yellow
if ($findingProblems.Count -gt 0) {
    foreach ($p in $findingProblems) { Write-Host ("  {0}" -f $p) -ForegroundColor DarkGray }
}
else {
    Write-Host '  every code has a wording and every wording has a code' -ForegroundColor DarkGray
}

# ---------------------------------------------------------------- 9. node types vs docs and labels
# Every type in flowcheck's knownTypes is reachable from the palette and from the create tools, so it needs a doc page
# (its own, or one of the two shared pages) and a palette label. No gate can tell whether that page is *right* - that is
# the node audit - but a missing page or a missing label is decidable, and it is how a new type ships undocumented.
$nodeDocsDir = Join-Path $repoRoot 'nexus/platform/provision/template/files/skills/flow-authoring/references/nodes'
$nodeDocAliases = @{
    # the four control signals share one page, and the canvas-only kinds share another
    'break'     = 'control-signals.md'
    'continue'  = 'control-signals.md'
    'return'    = 'control-signals.md'
    'exit'      = 'control-signals.md'
    'start'     = 'visual.md'
    'entry'     = 'visual.md'
    'comment'   = 'visual.md'
    'layout'    = 'visual.md'
    'knowledge' = 'visual.md'
}
# Canvas-only kinds drawn without a palette label, so the bundle has no key for them.
$nodeLabelSkip = @('start', 'entry', 'knowledge')

$nodeTypes = @()
$flowcheckPath = Join-Path $repoRoot 'nexus/shared/flowcheck/check.go'
if (Test-Path $flowcheckPath) {
    $flowcheckText = Get-Content -Path $flowcheckPath -Raw -Encoding UTF8
    $knownBlock = ($flowcheckText -split 'var knownTypes')[1]
    if ($knownBlock) {
        $knownBlock = $knownBlock.Split('}')[0]
        foreach ($m in [regex]::Matches($knownBlock, '"([a-z_]+)":\s*true')) {
            $nodeTypes += $m.Groups[1].Value
        }
    }
}
$nodeProblems = New-Object System.Collections.Generic.List[string]
# Load the palette label keys once for both bundles: the loop below walks every type.
$nodeLabels = @{}
foreach ($locName in @('en', 'zh-CN')) {
    $bundlePath = Join-Path $repoRoot ("desktop/src/locales/{0}/flow.json" -f $locName)
    if (-not (Test-Path $bundlePath)) { continue }
    $nodeBundle = Get-Content -Path $bundlePath -Raw -Encoding UTF8 | ConvertFrom-Json
    $nodeLabels[$locName] = @($nodeBundle.flow.nodeType.PSObject.Properties.Name)
}
foreach ($typ in ($nodeTypes | Sort-Object)) {
    $docName = $typ + '.md'
    if ($nodeDocAliases.ContainsKey($typ)) { $docName = $nodeDocAliases[$typ] }
    if (-not (Test-Path (Join-Path $nodeDocsDir $docName))) {
        $nodeProblems.Add(("node type {0} has no doc page (expected references/nodes/{1})" -f $typ, $docName))
    }
    if ($nodeLabelSkip -contains $typ) { continue }
    foreach ($locName in @('en', 'zh-CN')) {
        if (-not $nodeLabels.ContainsKey($locName)) { continue }
        if ($nodeLabels[$locName] -notcontains $typ) {
            $nodeProblems.Add(("node type {0} has no palette label in {1} (flow.nodeType.{0})" -f $typ, $locName))
        }
    }
}
Write-Host ''
Write-Host ("9. node types: {0} in knownTypes" -f $nodeTypes.Count) -ForegroundColor Yellow
if ($nodeProblems.Count -gt 0) {
    foreach ($p in $nodeProblems) { Write-Host ("  {0}" -f $p) -ForegroundColor DarkGray }
}
else {
    Write-Host '  every type has a doc page and a palette label' -ForegroundColor DarkGray
}

# ---------------------------------------------------------------- 10. gate
# Two short allowlists and nothing else: every entry is Chinese the product deliberately keeps, and adding
# one is a decision that belongs in review, not in a passing build.
$nonTestChineseAllowed = @(
    # zh-CN skeleton bodies: generated content for a Chinese-language tenant (see docs/i18n-plan.md, class 5)
    'nexus\shared\skeleton\skeleton.go'
)
$desktopChineseAllowed = @(
    # the language picker lists each language by its own name; translating it defeats the picker
    'desktop\src\locales\index.js',
    # legacy `msg` fallback, used only when a backend is old enough to have no `error` field
    'desktop\src\tenant\components\MainWindow.vue'
)

$gateProblems = New-Object System.Collections.Generic.List[string]
foreach ($f in $commentLines.Keys) {
    $gateProblems.Add(("Chinese comment: {0} ({1} line(s))" -f $f, $commentLines[$f]))
}
foreach ($f in $otherLines.Keys) {
    if ($f -like '*_test.go') { continue }          # fixtures are data, not prose
    if ($nonTestChineseAllowed -contains $f) { continue }
    $gateProblems.Add(("Chinese outside comments: {0} ({1} line(s))" -f $f, $otherLines[$f]))
}
foreach ($f in $desktopCopy.Keys) {
    if ($desktopChineseAllowed -contains $f) { continue }
    $gateProblems.Add(("Chinese copy in desktop sources: {0} ({1} line(s))" -f $f, $desktopCopy[$f]))
}
foreach ($f in $errJSON.Keys) {
    $gateProblems.Add(("ErrJSON is back (it is meant to be gone): {0}" -f $f))
}
foreach ($f in $msgChinese.Keys) {
    $gateProblems.Add(("an ErrCode msg contains Chinese: {0} ({1} call(s))" -f $f, $msgChinese[$f]))
}
foreach ($p in $findingProblems) {
    $gateProblems.Add($p)
}
foreach ($p in $nodeProblems) {
    $gateProblems.Add($p)
}

if ($Gate) {
    Write-Host ''
    if ($gateProblems.Count -gt 0) {
        Write-Host ("GATE FAILED: {0} problem(s) - see docs/i18n-plan.md for the rule behind each" -f $gateProblems.Count) -ForegroundColor Red
        foreach ($p in $gateProblems) { Write-Host ("  - " + $p) -ForegroundColor Red }
        exit 1
    }
    Write-Host 'GATE PASSED: no Chinese in comments, no Chinese in non-test Go sources beyond the allowlist, no un-bundled desktop copy, no ErrJSON, no Chinese msg, every finding code has a wording, every node type has a doc page and a label' -ForegroundColor Green
}

Write-Host ''
Write-Host 'baseline only: regex cannot follow variables or helper wrappers, so treat the numbers as a lower bound' -ForegroundColor DarkGray
