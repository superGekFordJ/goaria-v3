<#
.SYNOPSIS
    GoAria v3 开发环境设置脚本
    Setup script for GoAria v3 development environment

.DESCRIPTION
    此脚本用于自动准备开发环境：
    1. 检查并安装 Wails 3 CLI 工具
    2. 检查 pnpm 是否已安装
    3. 下载并部署 aria2c 二进制文件到 internal/process\bundled\windows 目录

.NOTES
    File Name      : setup.ps1
    Prerequisite   : Go 1.25+, Node.js 18+
#>

$ErrorActionPreference = "Stop"

function Write-Color([string]$text, [ConsoleColor]$color) {
    Write-Host $text -ForegroundColor $color
}

function Test-Command([string]$command) {
    return (Get-Command $command -ErrorAction SilentlyContinue) -ne $null
}

function Get-FileSHA256([string]$path) {
    return (Get-FileHash -Path $path -Algorithm SHA256).Hash.ToLowerInvariant()
}

function Test-Aria2Hash([string]$path, [string]$expectedHash, [string]$label) {
    $actualHash = Get-FileSHA256 $path
    if ($actualHash -ne $expectedHash.ToLowerInvariant()) {
        throw "$label SHA256 mismatch. Expected $expectedHash but got $actualHash"
    }
}

Write-Color "`n🚀 GoAria v3 Development Setup Initiated..." Cyan
Write-Color "============================================" Cyan

# -------------------------------------------------------------------------
# 1. Check pnpm
# -------------------------------------------------------------------------
Write-Host "Checking pnpm..." -NoNewline
if (Test-Command "pnpm") {
    Write-Color " [OK]" Green
} else {
    Write-Color " [MISSING]" Red
    Write-Color "❌ pnpm is not installed." Yellow
    Write-Color "   Please install it by running: corepack prepare pnpm@latest --activate" Gray
    Write-Color "   Or visit: https://pnpm.io/installation" Gray
    # We don't exit here, just warn, as they might use npm (though not recommended)
}

# -------------------------------------------------------------------------
# 2. Check & Install Wails 3
# -------------------------------------------------------------------------
Write-Host "Checking Wails 3 CLI..." -NoNewline
if (Test-Command "wails3") {
    Write-Color " [OK]" Green
} else {
    Write-Color " [MISSING]" Yellow
    Write-Color "⚠️  Installing Wails 3 CLI (this might take a while)..." Cyan
    try {
        go install github.com/wailsapp/wails/v3/cmd/wails3@latest
        if ($LASTEXITCODE -eq 0) {
            Write-Color "✅ Wails 3 installed successfully." Green
        } else {
            throw "Go install failed"
        }
    } catch {
        Write-Color "❌ Failed to install Wails 3. Please check your Go environment." Red
        exit 1
    }
}

# -------------------------------------------------------------------------
# 3. Download & Install Aria2c
# -------------------------------------------------------------------------
$targetDir = Join-Path $PSScriptRoot "internal\process\bundled\windows"
$aria2Exe = Join-Path $targetDir "aria2c.exe"
$aria2Version = "1.37.0"
$expectedHashes = @{
    "win-64bit" = @{
        Zip = "67d015301eef0b612191212d564c5bb0a14b5b9c4796b76454276a4d28d9b288"
        Exe = "be2099c214f63a3cb4954b09a0becd6e2e34660b886d4c898d260febfe9d70c2"
    }
    "win-32bit" = @{
        Zip = "35f6514cc5dd7e98a87b3c4c2d25a0754b9b063dbe59bc0f22d483464f61e5b6"
        Exe = "b9cd71b275af11b63c33457b0f43f2f2675937070c563e195f223efd7fa4c74b"
    }
}

# Determine Architecture
$arch = "win-64bit"
if ($env:PROCESSOR_ARCHITECTURE -eq "x86") { $arch = "win-32bit" }

$downloadUrl = "https://github.com/aria2/aria2/releases/download/release-$aria2Version/aria2-$aria2Version-$arch-build1.zip"
$expectedArchiveRoot = "aria2-$aria2Version-$arch-build1"
$expectedArchiveExe = Join-Path $expectedArchiveRoot "aria2c.exe"
$expectedHash = $expectedHashes[$arch]

Write-Host "Checking aria2c binary..." -NoNewline

if (Test-Path $aria2Exe) {
    try {
        Test-Aria2Hash -Path $aria2Exe -ExpectedHash $expectedHash.Exe -Label "Existing aria2c.exe"
        Write-Color " [OK]" Green
        Write-Color "   Found and verified at: $aria2Exe" Gray
    } catch {
        Write-Color " [INVALID]" Yellow
        Write-Color "   Existing bundled aria2c.exe failed integrity verification and will be replaced." Yellow
        Remove-Item $aria2Exe -Force
    }
}

if (-not (Test-Path $aria2Exe)) {
    Write-Color " [MISSING]" Yellow
    Write-Color "⬇️  Downloading aria2c v$aria2Version ($arch)..." Cyan
    Write-Color "   Source: $downloadUrl" Gray
    Write-Color "   ⚠️  Note: Downloading from GitHub. Check your network connection if it stalls." Yellow

    $tempZip = Join-Path $env:TEMP "aria2_setup_$((Get-Date).Ticks).zip"
    $tempExtract = Join-Path $env:TEMP "aria2_setup_$((Get-Date).Ticks)"

    try {
        # Download
        Invoke-WebRequest -Uri $downloadUrl -OutFile $tempZip -UseBasicParsing
        Test-Aria2Hash -Path $tempZip -ExpectedHash $expectedHash.Zip -Label "Downloaded aria2 archive"

        # Extract
        Write-Color "📦 Extracting..." Cyan
        Expand-Archive -Path $tempZip -DestinationPath $tempExtract -Force

        # Use the exact expected archive path
        $foundExe = Join-Path $tempExtract $expectedArchiveExe

        if (Test-Path $foundExe) {
            Test-Aria2Hash -Path $foundExe -ExpectedHash $expectedHash.Exe -Label "Extracted aria2c.exe"
            # Ensure target directory exists
            if (-not (Test-Path $targetDir)) { New-Item -ItemType Directory -Path $targetDir | Out-Null }

            # Move
            Move-Item -Path $foundExe -Destination $aria2Exe -Force
            Write-Color "✅ aria2c.exe installed to internal/process\bundled\windows/" Green
        } else {
            throw "aria2c.exe not found at expected archive path '$expectedArchiveExe'."
        }
    } catch {
        Write-Color "❌ Failed to download or install aria2c." Red
        Write-Color "   Error: $_" Red
        Write-Color "   Please manually download the release from GitHub and place aria2c.exe in internal/process\bundled\windows/" Yellow
        exit 1
    } finally {
        # Cleanup
        if (Test-Path $tempZip) { Remove-Item $tempZip -Force -ErrorAction SilentlyContinue }
        if (Test-Path $tempExtract) { Remove-Item $tempExtract -Recurse -Force -ErrorAction SilentlyContinue }
    }
}

Write-Color "`n🎉 Setup Complete!" Green
Write-Color "============================================" Cyan
Write-Color "To start development:" Gray
Write-Color "  > wails3 dev" White
Write-Color "`nℹ️  If you are not a developer and just want to use the app," Gray
Write-Color "   please download the pre-built installer from the Releases page." Gray
