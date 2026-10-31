param([Parameter(Mandatory=$true)][string]$Invitation, [Parameter(Mandatory=$true)][string]$Base)
$ErrorActionPreference = 'Stop'
$Version = '@RELEASE_TAG@'
$origin = $Base -replace '/downloads/[^/]+$', ''
Write-Host "`n连接进度"
Write-Host '[1/5] 等待连接授权'
Write-Host '⚠️ 远程控制授权'
Write-Host "`n服务器：$origin"
Write-Host '连接后，对方将能够以你当前账户的权限控制本机，包括执行命令、访问和修改文件。请确保你信任对方，并了解此次连接的用途。'
Write-Host "`n是否允许连接？[y/N] " -NoNewline
$answer = [Console]::ReadLine()
if ($null -eq $answer -or $answer.Trim() -ine 'y') {
  Write-Host '已取消连接。'
  return
}
$architecture = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant()
switch ($architecture) { 'x64' { $arch = 'amd64' } 'arm64' { $arch = 'arm64' } default { throw 'Unsupported architecture' } }
Write-Host '[2/5] 检查连通性与版本…'
$probe = [System.Net.WebSockets.ClientWebSocket]::new()
$timeout = [System.Threading.CancellationTokenSource]::new(10000)
try {
  $probe.Options.SetRequestHeader('Authorization', "Bearer $Invitation")
  $probe.Options.SetRequestHeader('X-Agenterm-Version', $Version)
  $endpoint = ($origin -replace '^http', 'ws') + '/client?probe=1'
  $null = $probe.ConnectAsync([Uri]$endpoint, $timeout.Token).GetAwaiter().GetResult()
} catch {
  if ($_.Exception.ToString() -match '426') {
    throw "版本不兼容：安装器 $Version 与 Server 不兼容。请将 Server 和 Client 更新到兼容版本。"
  }
  throw "WebSocket connectivity check failed. Check the server address, network, proxy, and invitation validity. $($_.Exception.Message)"
} finally {
  $probe.Dispose()
  $timeout.Dispose()
}
Write-Host '      服务可达，邀请和版本校验通过。'
Write-Host '[3/5] 准备客户端…'
if (-not $env:LOCALAPPDATA) { throw 'LOCALAPPDATA is not set' }
$cache = Join-Path $env:LOCALAPPDATA "Agenterm\cache\$Version\windows-$arch"
New-Item -ItemType Directory -Path $cache -Force | Out-Null
$work = Join-Path $cache ('.tmp-' + [System.Guid]::NewGuid())
New-Item -ItemType Directory -Path $work | Out-Null
try {
  $asset = "agenterm-cli-windows-$arch.zip"
  $binary = "agenterm-cli-windows-$arch.exe"
  $program = Join-Path $cache $binary
  $file = Join-Path $work $asset
  Invoke-WebRequest -UseBasicParsing -TimeoutSec 15 "$base/SHA256SUMS" -OutFile (Join-Path $work 'SHA256SUMS')
  $manifest = Get-Content (Join-Path $work 'SHA256SUMS')
  function Get-ExpectedHash([string]$Name) {
    $line = $manifest | Where-Object { $_ -match ('^[a-fA-F0-9]{64}  ' + [regex]::Escape($Name) + '$') } | Select-Object -First 1
    if (-not $line) { throw "Missing checksum for $Name" }
    return $line.Substring(0, 64)
  }
  $expectedBinary = Get-ExpectedHash $binary
  $valid = $false
  if (Test-Path -LiteralPath $program -PathType Leaf) {
    $item = Get-Item -LiteralPath $program -Force
    if (-not ($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint)) {
      $valid = (Get-FileHash -Algorithm SHA256 -LiteralPath $program).Hash -eq $expectedBinary
    }
  }
  if ($valid) {
    Write-Host '      本地客户端校验通过，跳过下载。'
  } else {
    Write-Host '      正在下载客户端…'
    $expectedAsset = Get-ExpectedHash $asset
    $ProgressPreference = 'Continue'
    Invoke-WebRequest -UseBasicParsing -TimeoutSec 180 "$base/$asset" -OutFile $file
    Write-Host '      下载完成，正在校验并解压…'
    if ((Get-FileHash -Algorithm SHA256 -LiteralPath $file).Hash -ne $expectedAsset) { throw 'Archive checksum mismatch' }
    Expand-Archive -LiteralPath $file -DestinationPath $work
    $staged = Join-Path $work $binary
    if ((Get-FileHash -Algorithm SHA256 -LiteralPath $staged).Hash -ne $expectedBinary) { throw 'Client checksum mismatch' }
    if (Test-Path -LiteralPath $program -PathType Container) { throw 'Client cache path is a directory' }
    try {
      Move-Item -LiteralPath $staged -Destination $program -Force -ErrorAction Stop
    } catch {
      # Another running instance can keep the cached executable locked on Windows.
      # The independently verified staged client remains usable for this session.
      $program = $staged
    }
    Write-Host '      客户端校验通过。'
  }
  & $program -invite $Invitation -consent-confirmed
} finally { Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue }
