param(
    [double]$MinFreeMemoryGB = 4,
    [double]$MinFreeDiskGB = 20,
    [int[]]$Ports = @(6379, 7001, 8081, 9090, 3000, 18081, 18082, 19080, 19081, 19082),
    [switch]$CheckEndpoints,
    [string[]]$EndpointUrls = @(
        "http://127.0.0.1:8081/healthz",
        "http://127.0.0.1:9090/-/ready",
        "http://127.0.0.1:3000/api/health"
    ),
    [int]$EndpointTimeoutSec = 2,
    [switch]$Strict
)

$ErrorActionPreference = "Stop"

$ProjectRoot = if (-not [string]::IsNullOrWhiteSpace($PSScriptRoot)) {
    Resolve-Path (Join-Path $PSScriptRoot "..")
} else {
    Get-Location
}

function Convert-BytesToGB {
    param([double]$Bytes)
    return [math]::Round($Bytes / 1GB, 2)
}

function Get-MemorySnapshot {
    try {
        [void][Reflection.Assembly]::LoadWithPartialName("Microsoft.VisualBasic")
        $info = [Microsoft.VisualBasic.Devices.ComputerInfo]::new()
        return [pscustomobject]@{
            total_gb = Convert-BytesToGB $info.TotalPhysicalMemory
            available_gb = Convert-BytesToGB $info.AvailablePhysicalMemory
            available_percent = [math]::Round(($info.AvailablePhysicalMemory / $info.TotalPhysicalMemory) * 100, 2)
        }
    } catch {
        return $null
    }
}

function Get-UptimeSnapshot {
    try {
        if (Get-Command Get-Uptime -ErrorAction SilentlyContinue) {
            $uptime = Get-Uptime
        } else {
            $uptime = [timespan]::FromMilliseconds([math]::Abs([Environment]::TickCount))
        }
        return [pscustomobject]@{
            total_hours = [math]::Round($uptime.TotalHours, 2)
            text = $uptime.ToString()
        }
    } catch {
        return $null
    }
}

function Get-DiskSnapshot {
    param(
        [string]$RootPath,
        [string]$Role
    )

    try {
        $root = [System.IO.Path]::GetPathRoot($RootPath)
        $drive = [System.IO.DriveInfo]::GetDrives() | Where-Object { $_.Name -eq $root } | Select-Object -First 1
        if ($null -eq $drive) {
            return $null
        }
        return [pscustomobject]@{
            role = $Role
            drive = $drive.Name
            total_gb = Convert-BytesToGB $drive.TotalSize
            free_gb = Convert-BytesToGB $drive.AvailableFreeSpace
            free_percent = [math]::Round(($drive.AvailableFreeSpace / $drive.TotalSize) * 100, 2)
        }
    } catch {
        return $null
    }
}

function Get-PortSnapshot {
    param([int[]]$PortsToCheck)

    $results = @()
    foreach ($port in $PortsToCheck) {
        $listeners = @(Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue)
        $owners = @()
        foreach ($listener in $listeners) {
            $processName = ""
            try {
                $processName = (Get-Process -Id $listener.OwningProcess -ErrorAction Stop).ProcessName
            } catch {
                $processName = "unknown"
            }
            $owners += [pscustomobject]@{
                pid = $listener.OwningProcess
                process = $processName
                address = $listener.LocalAddress
            }
        }
        $results += [pscustomobject]@{
            port = $port
            listening = ($listeners.Count -gt 0)
            owners = $owners
        }
    }
    return $results
}

function Test-Endpoint {
    param([string]$Url)

    try {
        $response = Invoke-WebRequest -Uri $Url -UseBasicParsing -TimeoutSec $EndpointTimeoutSec
        return [pscustomobject]@{
            url = $Url
            reachable = $true
            status_code = [int]$response.StatusCode
            error = ""
        }
    } catch {
        return [pscustomobject]@{
            url = $Url
            reachable = $false
            status_code = 0
            error = $_.Exception.Message
        }
    }
}

$warnings = [System.Collections.Generic.List[string]]::new()
$memory = Get-MemorySnapshot
$diskRoots = @(
    [pscustomobject]@{ Role = "project"; Path = $ProjectRoot.Path },
    [pscustomobject]@{ Role = "system"; Path = "$env:SystemDrive\" }
)
$disks = @()
foreach ($diskRoot in $diskRoots) {
    $snapshot = Get-DiskSnapshot $diskRoot.Path $diskRoot.Role
    if ($null -ne $snapshot -and @($disks | Where-Object { $_.drive -eq $snapshot.drive }).Count -eq 0) {
        $disks += $snapshot
    }
}
$uptime = Get-UptimeSnapshot
$portSnapshots = Get-PortSnapshot $Ports

if ($null -eq $memory) {
    $warnings.Add("memory_snapshot_unavailable")
} elseif ($memory.available_gb -lt $MinFreeMemoryGB) {
    $warnings.Add("available_memory_below_${MinFreeMemoryGB}gb")
}

if ($disks.Count -eq 0) {
    $warnings.Add("disk_snapshot_unavailable")
} else {
    foreach ($disk in $disks) {
        if ($disk.free_gb -lt $MinFreeDiskGB) {
            $warnings.Add("disk_free_below_${MinFreeDiskGB}gb_$($disk.drive.TrimEnd('\'))")
        }
    }
}

if ($null -ne $uptime -and $uptime.total_hours -lt 0.25) {
    $warnings.Add("system_recently_rebooted")
}

$dockerProcesses = @(Get-Process -ErrorAction SilentlyContinue | Where-Object {
    $_.ProcessName -like "*docker*" -or
    $_.ProcessName -like "com.docker*" -or
    $_.ProcessName -eq "vmmem" -or
    $_.ProcessName -eq "wsl"
} | Sort-Object WorkingSet64 -Descending | Select-Object -First 12 ProcessName, Id, CPU, WorkingSet64)

$topProcesses = @(Get-Process -ErrorAction SilentlyContinue |
    Sort-Object WorkingSet64 -Descending |
    Select-Object -First 10 @{Name = "process"; Expression = { $_.ProcessName } },
        @{Name = "pid"; Expression = { $_.Id } },
        @{Name = "cpu_seconds"; Expression = { [math]::Round([double]($_.CPU), 2) } },
        @{Name = "working_set_mb"; Expression = { [math]::Round($_.WorkingSet64 / 1MB, 2) } })

$endpoints = @()
if ($CheckEndpoints) {
    foreach ($url in $EndpointUrls) {
        $endpoints += Test-Endpoint $url
    }
}

$result = [pscustomobject]@{
    preflight = "complete"
    timestamp = (Get-Date).ToString("s")
    cwd = (Get-Location).Path
    project_root = $ProjectRoot.Path
    thresholds = [pscustomobject]@{
        min_free_memory_gb = $MinFreeMemoryGB
        min_free_disk_gb = $MinFreeDiskGB
    }
    uptime = $uptime
    memory = $memory
    disks = $disks
    listening_ports = $portSnapshots
    docker_processes = $dockerProcesses
    top_processes_by_memory = $topProcesses
    endpoints = $endpoints
    warnings = $warnings.ToArray()
}

$result | ConvertTo-Json -Depth 8

if ($Strict -and $warnings.Count -gt 0) {
    exit 2
}
