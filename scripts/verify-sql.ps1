#Requires -Version 5.1
<#
.SYNOPSIS
    Execute the platform SQL scripts against a throwaway database to prove they still run.

.DESCRIPTION
    These scripts never reach the Go compiler, so a mangled identifier (the 2026-11 accident: every
    `s` / `c` replaced by `o`, e.g. `platform.tenant_oapabilitieo`) survives `go build`, code review
    and the locale checks, and only surfaces when somebody runs `psql -f` on a fresh environment.

    This script creates a temporary database, runs the scripts in dependency order, and drops the
    database again. Any failing statement aborts the run with a non-zero exit code.

    Not every script under nexus/platform/sql can run on an empty database: some are incremental
    migrations that ALTER a table the repository has no DDL for (legacy tables carried over from the
    original dump). Those are listed in $skipped with the reason. The fix is to commit the missing
    table DDL, not to keep skipping: platform.quota_configs used to be the one entry here until its
    CREATE TABLE moved into platform.sql, which is what let quota_storage.sql become a real check.

    After the platform scripts, the tenant template (nexus/platform/provision/template/schema.sql +
    seed.sql) is applied into a throwaway schema with search_path pointing at it - the same way
    provision.CreateTenant runs it. That file is a pg_dump-ordered script (create every table, then
    add every PK / FK); an inline "REFERENCES" whose target has no PRIMARY KEY yet makes PostgreSQL
    raise 42830 and tenant creation fail on a fresh database, which no Go test would ever catch.

.PARAMETER Keep
    Do not drop the temporary database (for inspecting a failure by hand).

.EXAMPLE
    pwsh scripts/verify-sql.ps1
    pwsh scripts/verify-sql.ps1 -Port 5433 -User flame -Password secret
#>
param(
    [string]$DbHost = 'localhost',
    [int]$Port = 5432,
    [string]$User = 'postgres',
    [string]$Password = 'pg',
    [string]$AdminDb = 'postgres',
    [switch]$Keep
)

$ErrorActionPreference = 'Stop'

$repoRoot = Split-Path -Parent $PSScriptRoot
$sqlDir = Join-Path $repoRoot 'nexus/platform/sql'
if (-not (Test-Path $sqlDir)) {
    throw "SQL directory not found: $sqlDir"
}

$dbName = 'flame_sqlcheck_' + (Get-Random -Minimum 10000 -Maximum 99999)
$env:PGPASSWORD = $Password

# Dependency order matters: the incremental migrations below reference tables created by the
# scripts that come first. Alphabetical order does not work (asset_* need shared_assets).
$steps = @(
    @{ File = 'platform.sql';                Note = 'core platform tables (incl. platform.quota_configs)' }
    @{ File = 'quota_storage.sql';           Note = 'session-quota columns on quota_configs' }
    @{ File = 'tenant_capabilities.sql';     Note = 'per-tenant capability grants' }
    @{ File = 'capability_gates.sql';        Note = 'global kill switch' }
    @{ File = 'capability_code_rename.sql';  Note = 'needs capability_gates' }
    @{ File = 'shared_assets.sql';           Note = 'market tables' }
    @{ File = 'asset_listing_rename.sql';    Note = 'needs shared_assets' }
    @{ File = 'asset_build_rename.sql';      Note = 'needs shared_assets' }
    @{ File = 'asset_pack_digest.sql';       Note = 'needs shared_asset_installs' }
    @{ File = 'listing_policy.sql';          Note = 'listing review policy' }
    @{ File = 'peer_market.sql';             Note = 'peer servers + market tokens' }
    @{ File = 'plugin_gates.sql';            Note = 'plugin gates' }
    @{ File = 'tenant_plugins.sql';          Note = 'tenant plugin rows' }
    @{ File = 'customtool_capability_exec.sql'; Note = 'backfills tenant schemas (no-op on an empty DB)' }
)

# Kept for the next legacy-table case: SQL that ALTERs a table whose DDL is not in this repository cannot run here.
$skipped = @()

function Invoke-Psql {
    param(
        [string]$Database,
        [string[]]$Arguments
    )
    # psql writes NOTICE / WARNING to stderr, and PowerShell 5.1 turns native stderr into an error
    # record that $ErrorActionPreference='Stop' would escalate. Only the exit code decides here.
    $ErrorActionPreference = 'Continue'
    $output = & psql -h $DbHost -p $Port -U $User -d $Database -v ON_ERROR_STOP=1 @Arguments 2>&1
    $code = $LASTEXITCODE
    if ($code -ne 0) {
        Write-Host "  psql output:" -ForegroundColor DarkGray
        $output | Where-Object { $_ -match '\S' } | ForEach-Object { Write-Host "    $_" -ForegroundColor DarkGray }
    }
    return $code
}

Write-Host "verify-sql: $DbHost`:$Port as $User, temp database $dbName" -ForegroundColor Cyan

try {
    if ((Invoke-Psql -Database $AdminDb -Arguments @('-c', "DROP DATABASE IF EXISTS $dbName")) -ne 0) {
        throw "cannot connect to $DbHost`:$Port (check -Host/-Port/-User/-Password)"
    }
    if ((Invoke-Psql -Database $AdminDb -Arguments @('-c', "CREATE DATABASE $dbName")) -ne 0) {
        throw "CREATE DATABASE failed"
    }
    if ((Invoke-Psql -Database $dbName -Arguments @('-c', 'CREATE SCHEMA platform')) -ne 0) {
        throw "CREATE SCHEMA platform failed"
    }

    $failed = @()
    foreach ($step in $steps) {
        $path = Join-Path $sqlDir $step.File
        if (-not (Test-Path $path)) {
            Write-Host ("  FAIL  {0}  ({1})" -f $step.File, 'file not found') -ForegroundColor Red
            $failed += $step.File
            continue
        }
        if ((Invoke-Psql -Database $dbName -Arguments @('-f', $path)) -ne 0) {
            Write-Host ("  FAIL  {0}  ({1})" -f $step.File, $step.Note) -ForegroundColor Red
            $failed += $step.File
        }
        else {
            Write-Host ("  ok    {0}" -f $step.File) -ForegroundColor Green
        }
    }

    foreach ($step in $skipped) {
        Write-Host ("  skip  {0}  ({1})" -f $step.File, $step.Note) -ForegroundColor Yellow
    }

    # ---- Tenant template: the script that actually creates a tenant's tables ----
    # provision.CreateTenant runs it with a transaction-scoped search_path, so PGOPTIONS does the same here. Both files
    # must run in order: schema.sql builds every table and then adds the constraints, seed.sql writes the preinstalled
    # agents / skills on top.
    $tplDir = Join-Path $repoRoot 'nexus/platform/provision/template'
    if ((Invoke-Psql -Database $dbName -Arguments @('-c', 'CREATE SCHEMA t0')) -ne 0) {
        Write-Host '  FAIL  CREATE SCHEMA t0 (tenant template)' -ForegroundColor Red
        $failed += 'template:CREATE SCHEMA'
    }
    else {
        $env:PGOPTIONS = '-c search_path=t0'
        try {
            foreach ($file in @('schema.sql', 'seed.sql')) {
                $path = Join-Path $tplDir $file
                $label = "template/$file"
                if (-not (Test-Path $path)) {
                    Write-Host ("  FAIL  {0}  ({1})" -f $label, 'file not found') -ForegroundColor Red
                    $failed += $label
                    continue
                }
                if ((Invoke-Psql -Database $dbName -Arguments @('-f', $path)) -ne 0) {
                    Write-Host ("  FAIL  {0}  ({1})" -f $label, 'tenant template') -ForegroundColor Red
                    $failed += $label
                }
                else {
                    Write-Host ("  ok    {0}" -f $label) -ForegroundColor Green
                }
            }
        }
        finally {
            $env:PGOPTIONS = ''
        }
    }

    if ($failed.Count -gt 0) {
        Write-Host ""
        Write-Host ("{0} script(s) failed: {1}" -f $failed.Count, ($failed -join ', ')) -ForegroundColor Red
        exit 1
    }

    Write-Host ""
    Write-Host ("all {0} platform script(s) and the tenant template executed against an empty database" -f $steps.Count) -ForegroundColor Green
}
finally {
    if ($Keep) {
        Write-Host "keeping database $dbName (-Keep)" -ForegroundColor Yellow
    }
    else {
        Invoke-Psql -Database $AdminDb -Arguments @('-c', "DROP DATABASE IF EXISTS $dbName") | Out-Null
        Write-Host "dropped $dbName" -ForegroundColor DarkGray
    }
}
