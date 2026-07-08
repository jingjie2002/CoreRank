param(
    [int]$DurationMinutes = 10,
    [int]$IntervalSeconds = 30,
    [double]$MinFreeMemoryGB = 4,
    [double]$MinFreeDiskGB = 20,
    [string]$BaseUrl = "http://127.0.0.1:8081",
    [string]$PrometheusUrl = "http://127.0.0.1:9090",
    [string]$GrafanaUrl = "http://127.0.0.1:3000",
    [switch]$RunSmokeAtStart,
    [switch]$RunSmokeAtEnd,
    [switch]$AllowResourceWarnings,
    [string]$OutputPath = ""
)

$ErrorActionPreference = "Stop"

function Invoke-Preflight {
    $script = Join-Path $PSScriptRoot "stability_preflight.ps1"
    $raw = & powershell -NoProfile -ExecutionPolicy Bypass -File $script `
        -CheckEndpoints `
        -MinFreeMemoryGB $MinFreeMemoryGB `
        -MinFreeDiskGB $MinFreeDiskGB

    return ($raw | Out-String | ConvertFrom-Json)
}

function Test-HttpEndpoint {
    param([string]$Url)

    $startedAt = Get-Date
    try {
        $response = Invoke-WebRequest -Uri $Url -UseBasicParsing -TimeoutSec 5
        return [pscustomobject]@{
            url = $Url
            ok = $true
            status_code = [int]$response.StatusCode
            latency_ms = [math]::Round(((Get-Date) - $startedAt).TotalMilliseconds, 2)
            error = ""
        }
    } catch {
        return [pscustomobject]@{
            url = $Url
            ok = $false
            status_code = 0
            latency_ms = [math]::Round(((Get-Date) - $startedAt).TotalMilliseconds, 2)
            error = $_.Exception.Message
        }
    }
}

function Get-PrometheusTargets {
    param([string]$Url)

    try {
        $targets = Invoke-RestMethod -Uri "$Url/api/v1/targets?state=active" -TimeoutSec 5
        $summary = @()
        foreach ($job in @("corerank-gateway", "corerank-rank-service", "corerank-match-service")) {
            $hits = @($targets.data.activeTargets | Where-Object { $_.labels.job -eq $job })
            $summary += [pscustomobject]@{
                job = $job
                present = ($hits.Count -gt 0)
                health = if ($hits.Count -gt 0) { $hits[0].health } else { "" }
            }
        }
        return $summary
    } catch {
        return @([pscustomobject]@{
            job = "prometheus-query"
            present = $false
            health = $_.Exception.Message
        })
    }
}

function Invoke-Smoke {
    $script = Join-Path $PSScriptRoot "distributed_smoke.ps1"
    $raw = & powershell -NoProfile -ExecutionPolicy Bypass -File $script `
        -BaseUrl $BaseUrl `
        -PrometheusUrl $PrometheusUrl `
        -GrafanaUrl $GrafanaUrl

    return ($raw | Out-String | ConvertFrom-Json)
}

function Has-ResourceWarnings {
    param($Warnings)

    foreach ($warning in @($Warnings)) {
        if ($warning -like "available_memory_below_*" -or $warning -like "disk_free_below_*") {
            return $true
        }
    }
    return $false
}

if ($DurationMinutes -lt 1) {
    throw "DurationMinutes must be greater than or equal to 1"
}
if ($IntervalSeconds -lt 5) {
    throw "IntervalSeconds must be greater than or equal to 5"
}

$initialPreflight = Invoke-Preflight
$resourceBlocked = Has-ResourceWarnings $initialPreflight.warnings
$samples = @()
$smokeStart = $null
$smokeEnd = $null

if ($resourceBlocked -and -not $AllowResourceWarnings) {
    $result = [pscustomobject]@{
        status = "blocked"
        reason = "resource_warning"
        started_at = (Get-Date).ToString("s")
        duration_minutes = $DurationMinutes
        interval_seconds = $IntervalSeconds
        initial_preflight = $initialPreflight
        samples = $samples
    }
    $json = $result | ConvertTo-Json -Depth 12
    if (-not [string]::IsNullOrWhiteSpace($OutputPath)) {
        $json | Set-Content -Encoding UTF8 -LiteralPath $OutputPath
    }
    $json
    exit 2
}

if ($RunSmokeAtStart) {
    $smokeStart = Invoke-Smoke
}

$deadline = (Get-Date).AddMinutes($DurationMinutes)
while ((Get-Date) -lt $deadline) {
    $preflight = Invoke-Preflight
    $endpoints = @(
        Test-HttpEndpoint "$BaseUrl/healthz"
        Test-HttpEndpoint "$PrometheusUrl/-/ready"
        Test-HttpEndpoint "$GrafanaUrl/api/health"
    )
    $targets = Get-PrometheusTargets $PrometheusUrl

    $sample = [pscustomobject]@{
        timestamp = (Get-Date).ToString("s")
        memory = $preflight.memory
        disks = $preflight.disks
        warnings = $preflight.warnings
        endpoints = $endpoints
        prometheus_targets = $targets
    }
    $samples += $sample

    if ((Has-ResourceWarnings $preflight.warnings) -and -not $AllowResourceWarnings) {
        $result = [pscustomobject]@{
            status = "aborted"
            reason = "resource_warning_during_run"
            started_at = $samples[0].timestamp
            ended_at = (Get-Date).ToString("s")
            duration_minutes = $DurationMinutes
            interval_seconds = $IntervalSeconds
            initial_preflight = $initialPreflight
            smoke_start = $smokeStart
            smoke_end = $smokeEnd
            samples = $samples
        }
        $json = $result | ConvertTo-Json -Depth 12
        if (-not [string]::IsNullOrWhiteSpace($OutputPath)) {
            $json | Set-Content -Encoding UTF8 -LiteralPath $OutputPath
        }
        $json
        exit 3
    }

    Start-Sleep -Seconds $IntervalSeconds
}

if ($RunSmokeAtEnd) {
    $smokeEnd = Invoke-Smoke
}

$failedEndpointSamples = @($samples | Where-Object {
    @($_.endpoints | Where-Object { -not $_.ok }).Count -gt 0
})
$unhealthyTargetSamples = @($samples | Where-Object {
    @($_.prometheus_targets | Where-Object { -not $_.present -or $_.health -ne "up" }).Count -gt 0
})

$status = "passed"
if ($failedEndpointSamples.Count -gt 0 -or $unhealthyTargetSamples.Count -gt 0) {
    $status = "failed"
}

$final = [pscustomobject]@{
    status = $status
    started_at = if ($samples.Count -gt 0) { $samples[0].timestamp } else { (Get-Date).ToString("s") }
    ended_at = (Get-Date).ToString("s")
    duration_minutes = $DurationMinutes
    interval_seconds = $IntervalSeconds
    initial_preflight = $initialPreflight
    smoke_start = $smokeStart
    smoke_end = $smokeEnd
    samples = $samples
    failed_endpoint_samples = $failedEndpointSamples.Count
    unhealthy_target_samples = $unhealthyTargetSamples.Count
}

$finalJson = $final | ConvertTo-Json -Depth 12
if (-not [string]::IsNullOrWhiteSpace($OutputPath)) {
    $finalJson | Set-Content -Encoding UTF8 -LiteralPath $OutputPath
}
$finalJson

if ($status -ne "passed") {
    exit 4
}
