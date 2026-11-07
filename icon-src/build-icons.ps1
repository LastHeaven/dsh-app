# 生成托盘三态图标（透明底鲸鱼）
#
# 用法：
#   pwsh -File icon-src\build-icons.ps1
#
# 依赖：Chrome（用于把 SVG 渲染成 PNG）、Python + Pillow（用于打包 ICO）
# 产物：assets\running.ico / stopped.ico / busy.ico，以及 app.ico（exe 主图标）
#
# 之所以不是纯 Python：本机实测 Python 直接调 Chrome --headless 截图不稳定
# （进程返回 0 但不产出文件），PowerShell 的 Start-Process 一直可靠。

$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $PSScriptRoot      # 仓库根目录
$assets = Join-Path $root 'assets'
$work = Join-Path $PSScriptRoot '.work'
$whaleSvg = Join-Path $PSScriptRoot 'svg\whale.svg'

if (-not (Test-Path $whaleSvg)) {
    throw "找不到鲸鱼路径源文件：$whaleSvg"
}
New-Item -ItemType Directory -Path $assets, $work -Force | Out-Null

# ---- 1) 找 Chrome -----------------------------------------------------------
$chrome = @(
    'C:\Program Files\Google\Chrome\Application\chrome.exe',
    'C:\Program Files (x86)\Google\Chrome\Application\chrome.exe',
    "$env:LOCALAPPDATA\Google\Chrome\Application\chrome.exe"
) | Where-Object { Test-Path $_ } | Select-Object -First 1

if (-not $chrome) { throw '未找到 Chrome，无法渲染 SVG。' }

# ---- 2) 从 whale.svg 里取出 path 数据 --------------------------------------
$raw = Get-Content -Raw $whaleSvg
$m = [regex]::Match($raw, 'd="([^"]*)"')
if (-not $m.Success) { throw 'whale.svg 里没有找到 <path d="...">' }
$whalePath = $m.Groups[1].Value

# 鲸鱼原始比例 23.16 x 17.04，在 1024 画布里留边距居中
$scale = 900.0 / 23.16
$tx = (1024.0 - 900.0) / 2.0
$ty = (1024.0 - 17.04 * $scale) / 2.0

$states = [ordered]@{
    running = '#0B0B0F'   # 运行中：黑鱼
    stopped = '#9AA0AE'   # 未运行：灰鱼
    busy    = '#D99B00'   # 启动中 / 异常：琥珀
    app     = '#0B0B0F'   # exe 自身图标
}

# ---- 3) 渲染成 1024 PNG ----------------------------------------------------
foreach ($name in $states.Keys) {
    $ink = $states[$name]
    $svg = @"
<svg xmlns="http://www.w3.org/2000/svg" width="1024" height="1024" viewBox="0 0 1024 1024">
  <g transform="translate({0:F3} {1:F3}) scale({2:F5})">
    <path fill="{3}" d="{4}"/>
  </g>
</svg>
"@ -f $tx, $ty, $scale, $ink, $whalePath

    $html = "<!doctype html><html><head><meta charset='utf-8'>" +
            "<style>html,body{margin:0;padding:0;background:transparent}" +
            "svg{display:block}</style></head><body>" + $svg + "</body></html>"

    $page = Join-Path $work "$name.html"
    $shot = Join-Path $work "$name.png"
    Set-Content -Path $page -Value $html -Encoding UTF8
    if (Test-Path $shot) { Remove-Item $shot -Force }

    $proc = Start-Process -FilePath $chrome -ArgumentList @(
        '--headless=new', '--disable-gpu', '--hide-scrollbars', '--no-first-run',
        '--no-default-browser-check', '--force-device-scale-factor=1',
        "--user-data-dir=$(Join-Path $work "prof_$name")",
        '--window-size=1200,1200', '--default-background-color=00000000',
        "--screenshot=$shot", "file:///$($page -replace '\\','/')"
    ) -PassThru -Wait

    if (-not (Test-Path $shot)) { throw "渲染失败：$name" }
    Write-Host "  渲染 $name -> $shot"
}

# ---- 4) 打包成多尺寸 ICO（由 Python/Pillow 完成） ---------------------------
$py = Join-Path $PSScriptRoot 'pack_ico.py'
& python $py $work $assets
if ($LASTEXITCODE -ne 0) { throw "pack_ico.py 失败（退出码 $LASTEXITCODE）" }

Write-Host '图标已生成到 assets\'
