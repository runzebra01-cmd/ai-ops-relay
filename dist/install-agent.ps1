# 内网 Windows 机器安装脚本
#
#   以管理员身份打开 PowerShell，然后：
#   .\install-agent.ps1 -Relay https://relay.example.com -HostId win-01 -Token <令牌>
#
# 可选：-Proxy http://proxy.corp:3128   走企业代理
#       -Ca C:\path\ca.pem             指定 Relay 证书（默认取脚本同目录的 ca.pem）

param(
    [Parameter(Mandatory = $true)][string]$Relay,
    [Parameter(Mandatory = $true)][string]$HostId,
    [Parameter(Mandatory = $true)][string]$Token,
    [string]$Ca = "",
    [string]$Proxy = ""
)

$ErrorActionPreference = "Stop"
$InstallDir = "C:\ProgramData\RelayAgent"
$TaskName = "RelayAgent"

function Test-Admin {
    $p = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
    return $p.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

if (-not (Test-Admin)) {
    throw "请以管理员身份运行 PowerShell（右键 -> 以管理员身份运行）"
}

# ---------- 1. 选择架构 ----------
$archRaw = $env:PROCESSOR_ARCHITECTURE
switch ($archRaw) {
    "AMD64" { $arch = "amd64" }
    "ARM64" { $arch = "arm64" }
    default { throw "不支持的架构: $archRaw（目前支持 AMD64 / ARM64）" }
}
$srcExe = Join-Path $PSScriptRoot "relay-agent-windows-$arch.exe"
if (-not (Test-Path $srcExe)) { throw "找不到 $srcExe" }

Write-Host "== 1. 安装二进制 ($arch) =="
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
Copy-Item $srcExe (Join-Path $InstallDir "relay-agent.exe") -Force

# ---------- 2. 证书 ----------
Write-Host "== 2. 安装证书 =="
if ([string]::IsNullOrEmpty($Ca)) {
    $Ca = Join-Path $PSScriptRoot "ca.pem"
}
$caDst = Join-Path $InstallDir "ca.pem"
if (Test-Path $Ca) {
    Copy-Item $Ca $caDst -Force
} elseif (Test-Path $caDst) {
    Write-Host "   使用已有的 $caDst"
} else {
    Write-Host "   警告: 未找到 ca.pem，自签证书会校验失败"
    $caDst = ""
}

# ---------- 3. 连通性检查 ----------
Write-Host "== 3. 连通性检查 =="
$bin = Join-Path $InstallDir "relay-agent.exe"
$checkArgs = @("-check", "-relay", $Relay, "-host", $HostId, "-token", $Token)
if ($caDst) { $checkArgs += @("-ca", $caDst) }
& $bin @checkArgs
if ($LASTEXITCODE -ne 0) {
    Write-Host ""
    Write-Host "连接失败，按顺序排查：" -ForegroundColor Yellow
    Write-Host "  1. 本机能否访问 Relay:  curl.exe -k $Relay/healthz"
    Write-Host "  2. 是否必须走代理:      加 -Proxy http://proxy:port 重跑"
    Write-Host "  3. 令牌是否抄错"
    Write-Host "  4. 出口防火墙是否放行 443"
    throw "安装中止"
}

# ---------- 4. 启动脚本 + 计划任务 ----------
Write-Host "== 4. 注册开机自启 =="
$cmdFile = Join-Path $InstallDir "start.cmd"
$caArg = if ($caDst) { " -ca `"$caDst`"" } else { "" }
$proxyLine = if ($Proxy) { "set HTTPS_PROXY=$Proxy`r`nset HTTP_PROXY=$Proxy`r`n" } else { "" }
$content = @"
@echo off
$proxyLine"$bin" -relay $Relay -host $HostId -token $Token$caArg >> "$InstallDir\agent.log" 2>&1
"@
Set-Content -Path $cmdFile -Value $content -Encoding ASCII

# 先清掉旧任务，避免重复注册
schtasks /Delete /TN $TaskName /F 2>$null | Out-Null
schtasks /Create /TN $TaskName /TR "`"$cmdFile`"" /SC ONSTART /RU SYSTEM /RL HIGHEST /F | Out-Null
schtasks /Run /TN $TaskName | Out-Null
Start-Sleep -Seconds 3

# ---------- 5. 验证 ----------
Write-Host "== 5. 验证 =="
$log = Join-Path $InstallDir "agent.log"
if (Test-Path $log) {
    $tail = Get-Content $log -Tail 8
    $tail | ForEach-Object { Write-Host "   $_" }
    if ($tail -match "connected to") {
        Write-Host ""
        Write-Host "OK: relay-agent 已启动并注册为 $HostId" -ForegroundColor Green
        Write-Host "日志: $log"
        Write-Host "停止: schtasks /End /TN $TaskName"
        Write-Host "删除: schtasks /Delete /TN $TaskName /F"
        exit 0
    }
}
Write-Host "任务已注册但暂未看到连接成功，请稍后查看 $log" -ForegroundColor Yellow
exit 1
