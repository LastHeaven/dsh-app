# 构建 dsh-web.exe（Windows x64，单文件，零依赖）
#
# 需要：
#   - Go 1.24+
#   - windres（msys64/ucrt64/bin/windres.exe），只用来把 manifest 和图标链进 exe
#
# 产物：publish\dsh-web.exe（约 3 MB）
#
# 本文件必须保存为 **UTF-8 带 BOM**，否则 PowerShell 5.1 按 GBK 解析、
# 中文注释会把引号配对搞乱，直接 syntax error（这个坑踩过好几次）。

[CmdletBinding()]
param(
    [string]$OutDir = "publish",
    [switch]$SkipTest,
    # 发版用：把版本号写进 build\app.version.rc 再编译，例如 -SetVersion 1.2.3
    [string]$SetVersion = ""
)

$ErrorActionPreference = 'Stop'
Set-Location -LiteralPath $PSScriptRoot

$resDir = Join-Path $PSScriptRoot 'build'
$versionRC = Join-Path $resDir 'app.version.rc'

function Assert-Command($name, $hint) {
    if (-not (Get-Command $name -ErrorAction SilentlyContinue)) {
        throw "找不到 $name。$hint"
    }
}
Assert-Command go "请安装 Go 1.24+ 并加入 PATH。"

# BOM 自检：仓库里所有 .ps1 必须是 UTF-8 带 BOM。
#
# 这个坑反复出现：编辑工具/代码生成把 BOM 抹掉后，PowerShell 5.1 会按 GBK
# 解析中文注释，引号配对错乱，报一堆莫名其妙的 syntax error。
# 与其等下一次踩到，不如在构建开头就拦住。
function Assert-Utf8Bom([string[]]$paths) {
    foreach ($rel in $paths) {
        $full = Join-Path $PSScriptRoot $rel
        if (-not (Test-Path -LiteralPath $full)) { continue }
        $b = [System.IO.File]::ReadAllBytes($full)
        if ($b.Length -lt 3 -or $b[0] -ne 0xEF -or $b[1] -ne 0xBB -or $b[2] -ne 0xBF) {
            throw "$rel 缺少 UTF-8 BOM（PowerShell 5.1 会按 GBK 解析并报语法错误）。" +
                  "修复：`$t=[IO.File]::ReadAllText('$rel',[Text.Encoding]::UTF8)；" +
                  "[IO.File]::WriteAllText('$rel',`$t,(New-Object Text.UTF8Encoding(`$true))"
        }
    }
}
Assert-Utf8Bom @('build.ps1', 'build\icons\build-icons.ps1')

# 版本号注入：改写 build\app.version.rc（不是给 windres 传 -D —— 字符串型 -D
# 的引号在各 shell 里转义规则不一致，PowerShell 下实测会 syntax error）。
if ($SetVersion) {
    if ($SetVersion -notmatch '^\d+\.\d+\.\d+$') {
        throw "版本号格式应为 x.y.z，收到：$SetVersion"
    }
    $four = "$SetVersion.0"
    $parts = $SetVersion.Split('.')
    $commas = "$($parts[0]),$($parts[1]),$($parts[2]),0"

    Write-Host "==> 写入版本号 $SetVersion" -ForegroundColor Cyan
    $rc = [System.IO.File]::ReadAllText($versionRC, [System.Text.Encoding]::UTF8)
    $rc = [regex]::Replace($rc, '(?m)^#define VER_FILEVERSION\s+.*$',
        "#define VER_FILEVERSION             $commas")
    $rc = [regex]::Replace($rc, '(?m)^#define VER_FILEVERSION_STR\s+.*$',
        "#define VER_FILEVERSION_STR         `"$four`"")
    $rc = [regex]::Replace($rc, '(?m)^#define VER_PRODUCTVERSION\s+.*$',
        "#define VER_PRODUCTVERSION          $commas")
    $rc = [regex]::Replace($rc, '(?m)^#define VER_PRODUCTVERSION_STR\s+.*$',
        "#define VER_PRODUCTVERSION_STR      `"$SetVersion`"")
    [System.IO.File]::WriteAllText($versionRC, $rc, (New-Object System.Text.UTF8Encoding($false)))
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

# go:embed 只认包目录内的文件，所以托盘图标的**唯一存放位置**就是
# internal\tray\icons（已提交到仓库）。它们由 build\icons\build-icons.ps1 生成，
# 生成物直接落在那儿，构建时不需要再复制。
Write-Host "==> 检查托盘图标" -ForegroundColor Cyan
foreach ($ico in @('running.ico', 'stopped.ico', 'busy.ico')) {
    $p = Join-Path $PSScriptRoot "internal\tray\icons\$ico"
    if (-not (Test-Path -LiteralPath $p)) {
        throw "缺少托盘图标 $p。请先运行 build\icons\build-icons.ps1 生成。"
    }
}

Write-Host "==> 生成资源 (app.syso)" -ForegroundColor Cyan
# app.syso 必须落在被编译包目录里 Go 才认，所以生成到 cmd\dsh-web 下。
$syso = Join-Path $PSScriptRoot 'cmd\dsh-web\app.syso'
& $windres -I (Join-Path $resDir '') -i (Join-Path $resDir 'app.rc') -o $syso -O coff
if ($LASTEXITCODE -ne 0) { throw "windres 失败（退出码 $LASTEXITCODE）" }
if (-not (Test-Path -LiteralPath $syso)) { throw "windres 没有产出 app.syso" }

if (-not $SkipTest) {
    Write-Host "==> go vet" -ForegroundColor Cyan
    go vet ./...
    if ($LASTEXITCODE -ne 0) { throw "go vet 未通过" }

    Write-Host "==> go test" -ForegroundColor Cyan
    go test ./...
    if ($LASTEXITCODE -ne 0) { throw "go test 未通过" }
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
    -o (Join-Path $OutDir "dsh-web.exe") ./cmd/dsh-web
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
    Write-Warning "  [!!] 缺少版本信息资源（build\app.version.rc）"
}
