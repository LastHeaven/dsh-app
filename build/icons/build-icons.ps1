# 生成图标。
#
# 输出两套，**不能混用**：
#   - 托盘三态图标（透明底鲸鱼）→ internal\tray\icons\*.ico
#     go:embed 就是从这个目录读的，所以生成物要直接落在这里（已提交到仓库）。
#   - exe 主图标（白底圆角卡片 + 黑鲸鱼）→ build\app.ico
#     给 windres 链进 exe，贴在资源管理器/任务栏上。
#
# 用法（必须用 powershell，脚本是 UTF-8 带 BOM，PS 5.1 靠 BOM 认编码）：
#   powershell -NoProfile -ExecutionPolicy Bypass -File build\icons\build-icons.ps1
#
# 依赖：Chrome（把 SVG 渲染成 PNG）、Python + Pillow（打包 ICO）
#
# 之所以不是纯 Python：本机实测 Python 直接调 Chrome --headless 截图不稳定
# （进程返回 0 但不产出文件），PowerShell 的 Start-Process 一直可靠。
#
# 托盘图标必须**透明底**（叠在任务栏上），
# exe 图标必须**白底圆角卡片**（叠在资源管理器的深浅背景上）。

$ErrorActionPreference = 'Stop'

$repo = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)   # 仓库根目录
$trayDir = Join-Path $repo 'internal\tray\icons'                # 托盘图标（go:embed 目标）
$appDir = Join-Path $repo 'build'                               # exe 图标（windres 目标）
$work = Join-Path $PSScriptRoot '.work'
$whaleSvg = Join-Path $PSScriptRoot 'svg\whale.svg'

if (-not (Test-Path $whaleSvg)) {
    throw "找不到鲸鱼路径源文件：$whaleSvg"
}
New-Item -ItemType Directory -Path $trayDir, $appDir, $work -Force | Out-Null

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
    running = '#0B0B0F'   # 运行中：黑鱼（透明底）
    stopped = '#9AA0AE'   # 未运行：灰鱼（透明底）
    busy    = '#D99B00'   # 启动中 / 异常：琥珀（透明底）
    app     = '#0B0B0F'   # exe 自身图标：黑鱼 + 白底圆角卡片
}

# ---- 3) 渲染成 1024 PNG ----------------------------------------------------
foreach ($name in $states.Keys) {
    $ink = $states[$name]

    if ($name -eq 'app') {
        # 白底圆角卡片 + 缩小的鲸鱼（卡片内留边距，鲸鱼不贴边）
        # 0.80 是照着图标预览图的观感调的：再小就显得空，再大鲸鱼会顶到圆角。
        $cardScale = 0.80
        $fishScale = $scale * $cardScale
        $fishTx = (1024.0 - 23.16 * $fishScale) / 2.0
        $fishTy = (1024.0 - 17.04 * $fishScale) / 2.0
        $svg = @"
<svg xmlns="http://www.w3.org/2000/svg" width="1024" height="1024" viewBox="0 0 1024 1024">
  <defs>
    <linearGradient id="bg" x1="0" y1="0" x2="0.35" y2="1">
      <stop offset="0" stop-color="#FFFFFF"/>
      <stop offset="0.62" stop-color="#FFFFFF"/>
      <stop offset="1" stop-color="#F1F2F6"/>
    </linearGradient>
    <clipPath id="card">
      <rect width="1024" height="1024" rx="228" ry="228"/>
    </clipPath>
  </defs>
  <g clip-path="url(#card)">
    <rect width="1024" height="1024" fill="url(#bg)"/>
    <rect x="4" y="4" width="1016" height="1016" rx="225" ry="225"
          fill="none" stroke="#0B0B0F" stroke-opacity="0.10" stroke-width="8"/>
  </g>
  <g transform="translate({0:F3} {1:F3}) scale({2:F5})">
    <path fill="{3}" d="{4}"/>
  </g>
</svg>
"@ -f $fishTx, $fishTy, $fishScale, $ink, $whalePath
    }
    else {
        # 托盘图标：透明底，鲸鱼铺满（按原比例居中）
        $svg = @"
<svg xmlns="http://www.w3.org/2000/svg" width="1024" height="1024" viewBox="0 0 1024 1024">
  <g transform="translate({0:F3} {1:F3}) scale({2:F5})">
    <path fill="{3}" d="{4}"/>
  </g>
</svg>
"@ -f $tx, $ty, $scale, $ink, $whalePath
    }

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
& python $py $work $trayDir $appDir
if ($LASTEXITCODE -ne 0) { throw "pack_ico.py 失败（退出码 $LASTEXITCODE）" }

Write-Host "托盘图标 -> $trayDir"
Write-Host "exe 图标 -> $appDir\app.ico"
