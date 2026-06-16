# Windows build script — equivalent to `make build`.
# Requires Go and zip (via Git for Windows or 7-Zip).
param(
    [switch]$Clean,
    [switch]$ApiOnly,
    [switch]$TriggerOnly
)

$env:GOOS       = "linux"
$env:GOARCH     = "amd64"
$env:CGO_ENABLED = "0"

function Build-Lambda {
    param($Name, $Src)
    $outDir = "bin\$Name"
    New-Item -ItemType Directory -Force -Path $outDir | Out-Null
    Write-Host "Building $Name..."
    & go build -tags lambda.norpc -o "$outDir\bootstrap" $Src
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

    # Use 7-Zip if available, else fall back to Compress-Archive
    $zipPath = "$outDir\$Name.zip"
    if (Get-Command 7z -ErrorAction SilentlyContinue) {
        & 7z a -tzip $zipPath "$outDir\bootstrap" | Out-Null
    } else {
        Compress-Archive -Path "$outDir\bootstrap" -DestinationPath $zipPath -Force
    }
    Write-Host "  → $zipPath"
}

if ($Clean) {
    Remove-Item -Recurse -Force bin -ErrorAction SilentlyContinue
    Write-Host "Cleaned bin/"
    exit 0
}

if (-not $TriggerOnly) { Build-Lambda "api"     "./lambdas/api" }
if (-not $ApiOnly)     { Build-Lambda "trigger" "./lambdas/trigger" }

Write-Host "`nBuild complete."
