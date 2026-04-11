# GoAria Build & Package Script

# 1. 从 build/config.yml 获取版本号
$configPath = Join-Path $PSScriptRoot "build/config.yml"
$version = (Select-String -Path $configPath -Pattern '^\s+version: "([^"]+)"' | Select-Object -First 1).Matches[0].Groups[1].Value

Write-Host "Detected Version: $version" -ForegroundColor Cyan

$baseName = "goaria"
$binDir = Join-Path $PSScriptRoot "bin"

if (-not (Test-Path $binDir)) { New-Item -ItemType Directory -Path $binDir }

# 函数：打包 zip
function Build-And-Package($arch) {
    Write-Host "------------------------------------" -ForegroundColor Yellow
    Write-Host "Building for windows/$arch..." -ForegroundColor Yellow
    
    $exeSource = ""
    if ($arch -eq "amd64") {
        # 使用标准构建命令
        wails3 build
        $exeSource = Join-Path $binDir "goaria-v3.exe"
    } else {
        # 使用用户定义的 arm64 任务
        wails3 task windows:build:arm64
        $exeSource = Join-Path $binDir "goaria-v3-arm64.exe"
    }

    if (-not (Test-Path $exeSource)) {
        Write-Error "Build failed or output not found: $exeSource"
        exit 1
    }

    # 准备目标程序名（去掉 v3）
    $targetExeName = "$baseName.exe"
    if ($arch -eq "arm64") { $targetExeName = "$baseName-arm64.exe" }
    $exePath = Join-Path $binDir $targetExeName

    # 如果源和目标不同，更名（为了打包出的 exe 也不带 v3）
    if ($exeSource -ne $exePath) {
        if (Test-Path $exePath) { Remove-Item $exePath -Force }
        Move-Item $exeSource $exePath
    }

    $zipName = "$baseName-v$version-windows-$arch.zip"
    $zipPath = Join-Path $binDir $zipName

    if (Test-Path $zipPath) { Remove-Item $zipPath -Force }

    Write-Host "Packaging $zipName (containing $targetExeName)..." -ForegroundColor Green
    Compress-Archive -Path $exePath -DestinationPath $zipPath -Force
}

# 2. 构建并打包 amd64
Build-And-Package "amd64"

# 3. 构建并打包 arm64
Build-And-Package "arm64"

Write-Host "------------------------------------" -ForegroundColor Cyan
Write-Host "All builds completed successfully!" -ForegroundColor Cyan
Get-ChildItem -Path $binDir -Filter "*.zip" | Select-Object Name, Length
