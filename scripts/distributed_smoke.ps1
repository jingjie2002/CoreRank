param(
    [string]$BaseUrl = "http://127.0.0.1:8081",
    [string]$PrometheusUrl = "http://127.0.0.1:9090",
    [string]$GrafanaUrl = "http://127.0.0.1:3000",
    [string]$MatchMode = "duel",
    [string]$LeaderboardPrefix = "compose",
    [string]$RoomServerID = "compose-room-1"
)

$ErrorActionPreference = "Stop"

function Wait-Http {
    param([string]$Url)

    for ($i = 0; $i -lt 60; $i++) {
        try {
            Invoke-WebRequest -Uri $Url -UseBasicParsing -TimeoutSec 3 | Out-Null
            return
        } catch {
            Start-Sleep -Milliseconds 500
        }
    }

    throw "HTTP endpoint did not become ready: $Url"
}

function Invoke-Json {
    param(
        [string]$Method,
        [string]$Url,
        $Body = $null
    )

    $params = @{
        Method = $Method
        Uri = $Url
        TimeoutSec = 10
    }

    if ($null -ne $Body) {
        $params.ContentType = "application/json"
        $params.Body = ($Body | ConvertTo-Json -Depth 8 -Compress)
    }

    return Invoke-RestMethod @params
}

function Expect-HttpError {
    param(
        [string]$Method,
        [string]$Url,
        [int]$ExpectedStatus,
        $Body = $null,
        [string]$BodyContains = ""
    )

    $params = @{
        Method = $Method
        Uri = $Url
        TimeoutSec = 10
        UseBasicParsing = $true
    }

    if ($null -ne $Body) {
        $params.ContentType = "application/json"
        $params.Body = ($Body | ConvertTo-Json -Depth 8 -Compress)
    }

    try {
        $response = Invoke-WebRequest @params
        throw "Expected HTTP $ExpectedStatus from $Method $Url, got $($response.StatusCode)"
    } catch {
        $response = $_.Exception.Response
        if ($null -eq $response) {
            throw
        }

        $statusCode = [int]$response.StatusCode
        $bodyText = [string]$_.ErrorDetails.Message
        $contentProperty = $response.PSObject.Properties["Content"]
        if ([string]::IsNullOrWhiteSpace($bodyText) -and $null -ne $contentProperty -and $null -ne $contentProperty.Value) {
            $bodyText = [string]$contentProperty.Value
        }
        if ([string]::IsNullOrWhiteSpace($bodyText)) {
            try {
                $stream = $response.GetResponseStream()
                if ($null -ne $stream) {
                    $reader = [System.IO.StreamReader]::new($stream)
                    $bodyText = $reader.ReadToEnd()
                    $reader.Dispose()
                }
            } catch {
                $bodyText = ""
            }
        }

        if ($statusCode -ne $ExpectedStatus) {
            throw "Expected HTTP $ExpectedStatus from $Method $Url, got $statusCode body=$bodyText"
        }
        if (-not [string]::IsNullOrWhiteSpace($BodyContains) -and -not $bodyText.Contains($BodyContains)) {
            throw "Expected HTTP $ExpectedStatus body to contain '$BodyContains', got body=$bodyText"
        }

        return [pscustomobject]@{
            StatusCode = $statusCode
            Body = $bodyText
        }
    }
}

function Get-JsonValue {
    param(
        $Object,
        [string]$Primary,
        [string]$Fallback = ""
    )

    if ($null -eq $Object) {
        return $null
    }

    $primaryProperty = $Object.PSObject.Properties[$Primary]
    if ($null -ne $primaryProperty) {
        return $primaryProperty.Value
    }

    if (-not [string]::IsNullOrWhiteSpace($Fallback)) {
        $fallbackProperty = $Object.PSObject.Properties[$Fallback]
        if ($null -ne $fallbackProperty) {
            return $fallbackProperty.Value
        }
    }

    return $null
}

function Wait-GameServer {
    param(
        [string]$ServerID,
        [string]$Mode
    )

    $modeQuery = [uri]::EscapeDataString($Mode)
    $serversUrl = "{0}/api/servers?match_mode={1}" -f $BaseUrl, $modeQuery
    for ($i = 0; $i -lt 60; $i++) {
        $servers = Invoke-Json "GET" $serversUrl
        $matches = @($servers | Where-Object { (Get-JsonValue $_ "server_id" "ServerID") -eq $ServerID })
        if ($matches.Count -gt 0) {
            $server = $matches[0]
            $status = Get-JsonValue $server "status" "Status"
            $addr = Get-JsonValue $server "addr" "Addr"
            if ($status -eq "active" -and -not [string]::IsNullOrWhiteSpace($addr)) {
                return $server
            }
        }
        Start-Sleep -Milliseconds 500
    }

    throw "Roomserver was not registered as active: $ServerID"
}

function Split-HostPort {
    param([string]$Addr)

    $trimmed = $Addr.Trim()
    $lastColon = $trimmed.LastIndexOf(":")
    if ($lastColon -le 0 -or $lastColon -ge $trimmed.Length - 1) {
        throw "Invalid roomserver addr: $Addr"
    }

    $hostname = $trimmed.Substring(0, $lastColon).Trim("[", "]")
    $port = [int]$trimmed.Substring($lastColon + 1)
    if ([string]::IsNullOrWhiteSpace($hostname) -or $hostname -eq "0.0.0.0" -or $hostname -eq "::") {
        $hostname = "127.0.0.1"
    }

    return [pscustomobject]@{
        Host = $hostname
        Port = $port
    }
}

function New-RoomTcpClient {
    param([string]$Addr)

    $endpoint = Split-HostPort $Addr
    for ($i = 0; $i -lt 30; $i++) {
        $client = [System.Net.Sockets.TcpClient]::new()
        try {
            $async = $client.BeginConnect($endpoint.Host, $endpoint.Port, $null, $null)
            if (-not $async.AsyncWaitHandle.WaitOne(3000)) {
                $client.Close()
                throw "timeout"
            }
            $client.EndConnect($async)
            $stream = $client.GetStream()
            $stream.ReadTimeout = 3000
            $stream.WriteTimeout = 3000
            $encoding = [System.Text.UTF8Encoding]::new($false)
            return [pscustomobject]@{
                TcpClient = $client
                Reader = [System.IO.StreamReader]::new($stream, $encoding)
                Writer = [System.IO.StreamWriter]::new($stream, $encoding)
            }
        } catch {
            $client.Close()
            Start-Sleep -Milliseconds 500
        }
    }

    throw "Unable to connect roomserver TCP endpoint: $Addr"
}

function Send-RoomRequest {
    param(
        $Client,
        $Body
    )

    $json = $Body | ConvertTo-Json -Depth 8 -Compress
    $Client.Writer.WriteLine($json)
    $Client.Writer.Flush()
}

function Read-RoomResponse {
    param($Client)

    $line = $Client.Reader.ReadLine()
    if ([string]::IsNullOrWhiteSpace($line)) {
        throw "Roomserver TCP connection returned an empty response"
    }
    return $line | ConvertFrom-Json
}

function Expect-RoomResponse {
    param(
        $Client,
        [string]$Type,
        [string]$RoomID = "",
        [string]$PlayerID = ""
    )

    $response = Read-RoomResponse $Client
    if ($response.type -ne $Type) {
        throw "Unexpected room response type. want=$Type got=$($response | ConvertTo-Json -Compress)"
    }
    if (-not [string]::IsNullOrWhiteSpace($RoomID) -and $response.room_id -ne $RoomID) {
        throw "Unexpected room response room_id. want=$RoomID got=$($response | ConvertTo-Json -Compress)"
    }
    if (-not [string]::IsNullOrWhiteSpace($PlayerID) -and $response.player_id -ne $PlayerID) {
        throw "Unexpected room response player_id. want=$PlayerID got=$($response | ConvertTo-Json -Compress)"
    }
    return $response
}

Wait-Http "$BaseUrl/healthz"
Wait-Http "$PrometheusUrl/-/ready"
Wait-Http "$GrafanaUrl/api/health"
Wait-Http "http://127.0.0.1:19080/metrics"
Wait-Http "http://127.0.0.1:19081/metrics"
Wait-Http "http://127.0.0.1:19082/metrics"

$suffix = Get-Date -Format "HHmmss"
$numericSuffix = [int]$suffix
$player1 = "compose-p1-$suffix"
$player2 = "compose-p2-$suffix"
$cancelPlayer = "compose-cancel-$suffix"
$duplicatePlayer = "compose-dupe-$suffix"
$leaderboard = "${LeaderboardPrefix}:$suffix"
$leaderboardQuery = [uri]::EscapeDataString($leaderboard)

$roomServer = Wait-GameServer $RoomServerID $MatchMode
$registeredServerID = Get-JsonValue $roomServer "server_id" "ServerID"
$registeredServerAddr = Get-JsonValue $roomServer "addr" "Addr"

$cancelTicket = Invoke-Json "POST" "$BaseUrl/api/match/tickets" @{
    player_id = $cancelPlayer
    mmr_score = 700000 + $numericSuffix
    match_mode = $MatchMode
    max_wait_ms = 60000
}
$cancelTicketID = Get-JsonValue $cancelTicket "TicketID" "ticket_id"
$cancelTicketStatus = Get-JsonValue $cancelTicket "Status" "status"
if ([string]::IsNullOrWhiteSpace($cancelTicketID) -or $cancelTicketStatus -ne "queued") {
    throw "Cancel scenario ticket was not queued: $($cancelTicket | ConvertTo-Json -Compress)"
}

$cancelledTicket = Invoke-Json "DELETE" "$BaseUrl/api/match/tickets/$cancelTicketID"
$cancelledTicketStatus = Get-JsonValue $cancelledTicket "Status" "status"
if ($cancelledTicketStatus -ne "cancelled") {
    throw "Cancel scenario did not return cancelled ticket: $($cancelledTicket | ConvertTo-Json -Compress)"
}
$cancelRepeatError = Expect-HttpError "DELETE" "$BaseUrl/api/match/tickets/$cancelTicketID" 409

$duplicateTicket = Invoke-Json "POST" "$BaseUrl/api/match/tickets" @{
    player_id = $duplicatePlayer
    mmr_score = 800000 + $numericSuffix
    match_mode = $MatchMode
    max_wait_ms = 60000
}
$duplicateTicketID = Get-JsonValue $duplicateTicket "TicketID" "ticket_id"
$duplicateTicketStatus = Get-JsonValue $duplicateTicket "Status" "status"
if ([string]::IsNullOrWhiteSpace($duplicateTicketID) -or $duplicateTicketStatus -ne "queued") {
    throw "Duplicate scenario first ticket was not queued: $($duplicateTicket | ConvertTo-Json -Compress)"
}
$duplicateRepeatError = Expect-HttpError "POST" "$BaseUrl/api/match/tickets" 409 @{
    player_id = $duplicatePlayer
    mmr_score = 800000 + $numericSuffix
    match_mode = $MatchMode
    max_wait_ms = 60000
}

$duplicateCancelled = Invoke-Json "DELETE" "$BaseUrl/api/match/tickets/$duplicateTicketID"
$duplicateCancelledStatus = Get-JsonValue $duplicateCancelled "Status" "status"
if ($duplicateCancelledStatus -ne "cancelled") {
    throw "Duplicate scenario cleanup did not cancel ticket: $($duplicateCancelled | ConvertTo-Json -Compress)"
}

$ticket1 = Invoke-Json "POST" "$BaseUrl/api/match/tickets" @{
    player_id = $player1
    mmr_score = 1300
    match_mode = $MatchMode
    max_wait_ms = 60000
}
$ticket2 = Invoke-Json "POST" "$BaseUrl/api/match/tickets" @{
    player_id = $player2
    mmr_score = 1310
    match_mode = $MatchMode
    max_wait_ms = 60000
}

$matchedTicket = $ticket2
if ([string]::IsNullOrWhiteSpace($matchedTicket.MatchID)) {
    for ($i = 0; $i -lt 30; $i++) {
        Start-Sleep -Milliseconds 300
        $matchedTicket = Invoke-Json "GET" "$BaseUrl/api/match/tickets/$($ticket2.TicketID)"
        if (-not [string]::IsNullOrWhiteSpace($matchedTicket.MatchID)) {
            break
        }
    }
}
if ([string]::IsNullOrWhiteSpace($matchedTicket.MatchID)) {
    throw "Ticket did not match: $($matchedTicket | ConvertTo-Json -Compress)"
}
if ([string]::IsNullOrWhiteSpace($matchedTicket.RoomID)) {
    throw "Matched ticket has no room id: $($matchedTicket | ConvertTo-Json -Compress)"
}

$ticketRead = Invoke-Json "GET" "$BaseUrl/api/match/tickets/$($ticket1.TicketID)"
$result = Invoke-Json "GET" "$BaseUrl/api/match/results/$($matchedTicket.MatchID)"
if ($result.Status -ne "matched") {
    throw "Unexpected match status: $($result | ConvertTo-Json -Compress)"
}
if (@($result.PlayerIDs).Count -ne 2) {
    throw "Unexpected player ids: $($result | ConvertTo-Json -Compress)"
}
$resultServerID = Get-JsonValue $result "ServerID" "server_id"
$resultServerAddr = Get-JsonValue $result "ServerAddr" "server_addr"
if ([string]::IsNullOrWhiteSpace($resultServerID) -or [string]::IsNullOrWhiteSpace($resultServerAddr)) {
    throw "Match result has no roomserver assignment: $($result | ConvertTo-Json -Compress)"
}
if ($resultServerID -ne $RoomServerID) {
    throw "Match result used unexpected roomserver. want=$RoomServerID got=$($result | ConvertTo-Json -Compress)"
}
if ($resultServerAddr -ne $registeredServerAddr) {
    throw "Match result used unexpected roomserver addr. registered=$registeredServerAddr got=$($result | ConvertTo-Json -Compress)"
}

$roomClient = $null
try {
    $roomClient = New-RoomTcpClient $resultServerAddr
    Send-RoomRequest $roomClient @{ type = "join"; room_id = $matchedTicket.RoomID; player_id = $player1 }
    Expect-RoomResponse $roomClient "joined" $matchedTicket.RoomID $player1 | Out-Null

    Send-RoomRequest $roomClient @{ type = "join"; room_id = $matchedTicket.RoomID; player_id = $player2 }
    Expect-RoomResponse $roomClient "joined" $matchedTicket.RoomID $player2 | Out-Null

    Send-RoomRequest $roomClient @{ type = "ready"; room_id = $matchedTicket.RoomID; player_id = $player1 }
    Expect-RoomResponse $roomClient "ready" $matchedTicket.RoomID $player1 | Out-Null

    Send-RoomRequest $roomClient @{ type = "ready"; room_id = $matchedTicket.RoomID; player_id = $player2 }
    Expect-RoomResponse $roomClient "ready" $matchedTicket.RoomID $player2 | Out-Null
    Expect-RoomResponse $roomClient "room_started" $matchedTicket.RoomID | Out-Null

    Send-RoomRequest $roomClient @{ type = "leave"; room_id = $matchedTicket.RoomID; player_id = $player1 }
    Expect-RoomResponse $roomClient "left" $matchedTicket.RoomID $player1 | Out-Null

    Send-RoomRequest $roomClient @{ type = "leave"; room_id = $matchedTicket.RoomID; player_id = $player2 }
    Expect-RoomResponse $roomClient "left" $matchedTicket.RoomID $player2 | Out-Null
} finally {
    if ($null -ne $roomClient) {
        $roomClient.Reader.Dispose()
        $roomClient.Writer.Dispose()
        $roomClient.TcpClient.Close()
    }
}

$settle = Invoke-Json "POST" "$BaseUrl/api/matches/$($matchedTicket.MatchID)/settle" @{
    leaderboard_type = $leaderboard
    scores = @(
        @{ player_id = $player1; score = 1600 },
        @{ player_id = $player2; score = 1500 }
    )
}
if (@($settle.updated_players).Count -ne 2) {
    throw "Settlement did not update two players: $($settle | ConvertTo-Json -Compress)"
}

$topUrl = "{0}/api/rank/top?n=2&leaderboard_type={1}" -f $BaseUrl, $leaderboardQuery
$top = Invoke-Json "GET" $topUrl
if (@($top).Count -ne 2) {
    throw "Top rank did not return two players: $($top | ConvertTo-Json -Compress)"
}
if ($top[0].PlayerID -ne $player1 -or $top[0].Rank -ne 1) {
    throw "Unexpected top rank order: $($top | ConvertTo-Json -Compress)"
}

$playerIDQuery = [uri]::EscapeDataString($player1)
$playerUrl = "{0}/api/rank/player/{1}?leaderboard_type={2}" -f $BaseUrl, $playerIDQuery, $leaderboardQuery
$player = Invoke-Json "GET" $playerUrl
if ($player.PlayerID -ne $player1 -or $player.Rank -ne 1) {
    throw "Unexpected player rank: $($player | ConvertTo-Json -Compress)"
}

Start-Sleep -Seconds 6
$targets = Invoke-Json "GET" "$PrometheusUrl/api/v1/targets?state=active"
$targetSummary = @()
foreach ($job in @("corerank-gateway", "corerank-rank-service", "corerank-match-service")) {
    $hits = @($targets.data.activeTargets | Where-Object { $_.labels.job -eq $job })
    if ($hits.Count -lt 1) {
        throw "Prometheus target missing: $job"
    }
    $targetSummary += [pscustomobject]@{
        job = $job
        health = $hits[0].health
        scrapeUrl = $hits[0].scrapeUrl
    }
}
if (@($targetSummary | Where-Object { $_.health -ne "up" }).Count -gt 0) {
    throw "Prometheus target not up: $($targetSummary | ConvertTo-Json -Compress)"
}

$grafanaHealth = Invoke-Json "GET" "$GrafanaUrl/api/health"
$grafanaSearch = Invoke-Json "GET" "$GrafanaUrl/api/search?query=CoreRank"
if (@($grafanaSearch | Where-Object { $_.uid -eq "corerank-overview" }).Count -lt 1) {
    throw "Grafana dashboard corerank-overview was not found"
}

[pscustomobject]@{
    compose_stack = "ok"
    gateway_healthz = "ok"
    gateway_metrics = "ok"
    rank_metrics = "ok"
    match_metrics = "ok"
    prometheus_ready = "ok"
    grafana_database = $grafanaHealth.database
    grafana_dashboard = "corerank-overview"
    roomserver_id = $registeredServerID
    roomserver_addr = $registeredServerAddr
    cancel_ticket_id = $cancelTicketID
    cancel_ticket_status = $cancelledTicketStatus
    cancel_repeat_status_code = $cancelRepeatError.StatusCode
    duplicate_ticket_id = $duplicateTicketID
    duplicate_ticket_status = $duplicateCancelledStatus
    duplicate_repeat_status_code = $duplicateRepeatError.StatusCode
    ticket1_id = $ticket1.TicketID
    ticket1_status = $ticketRead.Status
    ticket2_id = $ticket2.TicketID
    ticket2_status = $matchedTicket.Status
    match_id = $matchedTicket.MatchID
    room_id = $matchedTicket.RoomID
    match_server_id = $resultServerID
    match_server_addr = $resultServerAddr
    room_tcp = "join-ready-started-left"
    match_status = $result.Status
    leaderboard_type = $leaderboard
    top_first_player = $top[0].PlayerID
    top_first_rank = $top[0].Rank
    player_rank = $player.Rank
    prometheus_targets = $targetSummary
} | ConvertTo-Json -Depth 8
