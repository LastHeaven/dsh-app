# 构建 dsh-web.exe（Windows x64，单文件，零依赖）
#
# 需要：
#   - Go 1.24+
#   - windres（msys64/ucrt64/bin/windres.exe），只用来把 manifest 和图标链进 exe
#
# 产物：publish\dsh-web.exe（约 3 MB）

[CmdletBinding()]
param(
    [string]$OutDir = "publish",
    [switch]$SkipTest,
    # 发版用：把版本号写进 app.version.rc 再编译，例如 -SetVersion 1.2.3
    [string]$SetVersion = ""
)

$ErrorActionPreference = 'Stop'
Set-Location -LiteralPath $PSScriptRoot

function Assert-Command($name, $hint) {
    if (-not (Get-Command $name -ErrorAction SilentlyContinue)) {
        throw "找不到 $name。$hint"
    }
}
Assert-Command go "请安装 Go 1.24+ 并加入 PATH。"

# 版本号注入：改写 app.version.rc（不是给 windres 传 -D —— 字符串型 -D 的引号
# 在各 shell 里转义规则不一致，PowerShell 下实测会 syntax error）。
if ($SetVersion) {
    if ($SetVersion -notmatch '^\d+\.\d+\.\d+$') {
        throw "版本号格式应为 x.y.z，收到：$SetVersion"
    }
    $four = "$SetVersion.0"
    $parts = $SetVersion.Split('.')
    $commas = "$($parts[0]),$($parts[1]),$($parts[2]),0"

    Write-Host "==> 写入版本号 $SetVersion" -ForegroundColor Cyan
    $rcPath = Join-Path $PSScriptRoot 'app.version.rc'
    $rc = [System.IO.File]::ReadAllText($rcPath, [System.Text.Encoding]::UTF8)
    $rc = [regex]::Replace($rc, '(?m)^#define VER_FILEVERSION\s+.*$',
        "#define VER_FILEVERSION             $commas")
    $rc = [regex]::Replace($rc, '(?m)^#define VER_FILEVERSION_STR\s+.*$',
        "#define VER_FILEVERSION_STR         `"$four`"")
    $rc = [regex]::Replace($rc, '(?m)^#define VER_PRODUCTVERSION\s+.*$',
        "#define VER_PRODUCTVERSION          $commas")
    $rc = [regex]::Replace($rc, '(?m)^#define VER_PRODUCTVERSION_STR\s+.*$',
        "#define VER_PRODUCTVERSION_STR      `"$SetVersion`"")
    [System.IO.File]::WriteAllText($rcPath, $rc, (New-Object System.Text.UTF8Encoding($false)))
}

# windres 不在 PATH 里，按已知位置找。
# CI 会先探测/安装，然后把路径放进 $env:windres_path，这里优先用它。
$windres = $env:windres_path
if (-not $windres) { $windres = (Get-Command windres -ErrorAction SilentlyContinue).Source }
if (-not $windres) {
    foreach ($c in @(
            "C:\msys64\ucrt64\bin\windres.exe",
            "C:\msys64\mingw64\bin\windres.exe")) {
        if (Test-Path -LiteralPath $c) { $windres = $c; break }
    }
}
if (-not $windres -or -not (Test-Path -LiteralPath $windres)) {
    throw "找不到 windres.exe（msys64/ucrt64/bin）。没有它无法嵌入 manifest，exe 会缺少管理员权限声明。"
}

Write-Host "==> gofmt" -ForegroundColor Cyan
$fmt = gofmt -l .
if ($fmt) {
    Write-Host "格式化以下文件：$fmt"
    gofmt -w .
}

Write-Host "==> 生成资源 (app.syso)" -ForegroundColor Cyan
& $windres -i app.rc -o app.syso -O coff
if ($LASTEXITCODE -ne 0) { throw "windres 失败（退出码 $LASTEXITCODE）" }
if (-not (Test-Path -LiteralPath app.syso)) { throw "windres 没有产出 app.syso" }

if (-not $SkipTest) {
    Write-Host "==> go vet" -ForegroundColor Cyan
    go vet ./...
    if ($LASTEXITCODE -ne 0) { throw "go vet 未通过" }
}

Write-Host "==> 编译" -ForegroundColor Cyan
if (-not (Test-Path -LiteralPath $OutDir)) {
    New-Item -ItemType Directory -Path $OutDir | Out-Null
}

# -s -w     去符号表和调试信息（体积大头）
# -H windowsgui  不弹控制台窗口（对应原来的 WinExe）
# -trimpath 去掉构建机绝对路径，产物可复现
$env:CGO_ENABLED = '0'
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
go build -trimpath -buildvcs=false `
    -ldflags "-s -w -H windowsgui" `
    -o (Join-Path $OutDir "dsh-web.exe") .
if ($LASTEXITCODE -ne 0) { throw "go build 失败" }

$exe = Join-Path $OutDir "dsh-web.exe"
$sizeMB = [math]::Round((Get-Item -LiteralPath $exe).Length / 1MB, 2)
Write-Host ""
Write-Host "完成：$exe  ($sizeMB MB)" -ForegroundColor Green

# 校验：manifest 真的链进去了吗（要求 requireAdministrator 生效）
$bytes = [IO.File]::ReadAllBytes($exe)
$text = [Text.Encoding]::ASCII.GetString($bytes)
if ($text.Contains("requireAdministrator")) {
    Write-Host "  [ok] manifest 已嵌入（requireAdministrator）" -ForegroundColor Green
} else {
    Write-Warning "  [!!] manifest 未嵌入 —— UAC 提权不会生效"
}

# 校验：版本信息资源
$ver = (Get-Item -LiteralPath $exe).VersionInfo.ProductVersion
if ($ver) {
    Write-Host "  [ok] 版本信息：$ver" -ForegroundColor Green
} else {
    Write-Warning "  [!!] 缺少版本信息资源（app.version.rc）"
}
