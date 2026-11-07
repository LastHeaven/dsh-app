# 生成图标：
#   - 托盘三态图标（透明底鲸鱼）：assets\running.ico / stopped.ico / busy.ico
#   - exe 主图标（白底圆角卡片 + 黑鲸鱼）：app.ico
#
# 用法：
#   powershell -NoProfile -ExecutionPolicy Bypass -File icon-src\build-icons.ps1
#
# 依赖：Chrome（用于把 SVG 渲染成 PNG）、Python + Pillow（用于打包 ICO）
#
# 之所以不是纯 Python：本机实测 Python 直接调 Chrome --headless 截图不稳定
# （进程返回 0 但不产出文件），PowerShell 的 Start-Process 一直可靠。
#
# 注意：托盘图标必须**透明底**（叠在任务栏上），
#       exe 图标必须**白底圆角卡片**（叠在资源管理器的深浅背景上）。
#       两者不能混用，改了颜色/底色前先想清楚是哪一套。

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
    running = '#0B0B0F'   # 运行中：黑鱼（透明底）
    stopped = '#9AA0AE'   # 未运行：灰鱼（透明底）
    busy    = '#D99B00'   # 启动中 / 异常：琥珀（透明底）
    app     = '#0B0B0F'   # exe 自身图标：黑鱼 + 白底圆角卡片
}

# exe 图标的白底卡片：与 D:\agents\dsh\icons\dsh-icon.svg 同款观感
# （白色圆角方块 + 极淡描边 + 轻微投影），这样在资源管理器/任务栏的
# 深浅背景上都看得清。托盘图标不加这个背景。
$appCornerRadius = 228.0 / 1024.0 * 1024.0   # 以 1024 画布为基准
$appCardInset = 0.0

# ---- 3) 渲染成 1024 PNG ----------------------------------------------------
foreach ($name in $states.Keys) {
    $ink = $states[$name]

    if ($name -eq 'app') {
        # 白底圆角卡片 + 缩小的鲸鱼（卡片内留边距，鲸鱼不贴边）
        # 0.80 是照着 D:\agents\dsh\icons\preview.png 的观感调的：
        # 再小就显得空，再大鲸鱼会顶到圆角。
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
& python $py $work $assets
if ($LASTEXITCODE -ne 0) { throw "pack_ico.py 失败（退出码 $LASTEXITCODE）" }

Write-Host '图标已生成到 assets\'
