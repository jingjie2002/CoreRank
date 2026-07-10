$ErrorActionPreference = "Stop"

$root = Split-Path -Parent $PSScriptRoot
Push-Location $root
try {
    $goPath = (& go env GOPATH).Trim()
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($goPath)) {
        throw "Unable to resolve GOPATH"
    }
    $env:PATH = (Join-Path $goPath "bin") + ";" + $env:PATH

    & protoc `
        --go_out=. --go_opt=paths=source_relative `
        --go-grpc_out=. --go-grpc_opt=paths=source_relative `
        api/proto/rank.proto
    if ($LASTEXITCODE -ne 0) {
        throw "protoc failed with exit code $LASTEXITCODE"
    }
} finally {
    Pop-Location
}
