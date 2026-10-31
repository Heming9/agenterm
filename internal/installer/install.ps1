param([Parameter(Mandatory=$true)][string]$Invitation, [Parameter(Mandatory=$true)][string]$Base)
$ErrorActionPreference = 'Stop'
$Version = '@RELEASE_TAG@'
$language = if ($env:AGENTERM_LANG -and $env:AGENTERM_LANG -ne 'auto') { $env:AGENTERM_LANG } else { [System.Globalization.CultureInfo]::CurrentUICulture.Name }
$chinese = $language -match '^(zh-CN|zh-SG|zh-Hans)(-|$)' -or $language -eq 'zh'
@TRANSLATIONS@
function Say([string]$Key) { Write-Host (Text $Key) }
$origin = $Base -replace '/downloads/[^/]+$', ''
Say 'installer.connection.progress'
Say 'installer.1.5.awaiting.authorization'
Say 'installer.remote.control.authorization'
Write-Host (Text 'installer.server.s' @($origin))
Say 'installer.the.server.can.control.this.computer.with'
Write-Host (Text 'installer.allow.connection.y.n') -NoNewline
$answer = [Console]::ReadLine()
if ($null -eq $answer -or $answer.Trim() -ine 'y') {
  Say 'installer.connection.cancelled'
  return
}
$architecture = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant()
switch ($architecture) { 'x64' { $arch = 'amd64' } 'arm64' { $arch = 'arm64' } default { throw (Text 'installer.unsupported.architecture') } }
Say 'installer.2.5.checking.connectivity.and.version'
$probe = [System.Net.WebSockets.ClientWebSocket]::new()
$timeout = [System.Threading.CancellationTokenSource]::new(10000)
try {
  $probe.Options.SetRequestHeader('Authorization', "Bearer $Invitation")
  $probe.Options.SetRequestHeader('X-Agenterm-Version', $Version)
  $endpoint = ($origin -replace '^http', 'ws') + '/client?probe=1'
  $null = $probe.ConnectAsync([Uri]$endpoint, $timeout.Token).GetAwaiter().GetResult()
} catch {
  if ($_.Exception.ToString() -match '426') {
    throw (Text 'installer.incompatible.versions.installer.s.is.incompatible.with' @($Version))
  }
  throw (Text 'installer.websocket.connectivity.check.failed.check.the.server' @($_.Exception.Message))
} finally {
  $probe.Dispose()
  $timeout.Dispose()
}
Say 'installer.server.reachable.invitation.and.version.verified'
Say 'installer.3.5.preparing.client'
if (-not $env:LOCALAPPDATA) { throw (Text 'installer.localappdata.is.not.set') }
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
    if (-not $line) { throw (Text 'installer.missing.checksum.for.s' @($Name)) }
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
    Say 'installer.cached.client.verified.skipping.download'
  } else {
    Say 'installer.downloading.client'
    $expectedAsset = Get-ExpectedHash $asset
    $ProgressPreference = 'Continue'
    Invoke-WebRequest -UseBasicParsing -TimeoutSec 180 "$base/$asset" -OutFile $file
    Say 'installer.download.complete.verifying.and.extracting'
    if ((Get-FileHash -Algorithm SHA256 -LiteralPath $file).Hash -ne $expectedAsset) { throw (Text 'installer.archive.checksum.mismatch') }
    Expand-Archive -LiteralPath $file -DestinationPath $work
    $staged = Join-Path $work $binary
    if ((Get-FileHash -Algorithm SHA256 -LiteralPath $staged).Hash -ne $expectedBinary) { throw (Text 'installer.client.checksum.mismatch') }
    if (Test-Path -LiteralPath $program -PathType Container) { throw (Text 'installer.client.cache.path.is.a.directory') }
    try {
      Move-Item -LiteralPath $staged -Destination $program -Force -ErrorAction Stop
    } catch {
      # Another running instance can keep the cached executable locked on Windows.
      # The independently verified staged client remains usable for this session.
      $program = $staged
    }
    Say 'installer.client.verified'
  }
  & $program -invite $Invitation -consent-confirmed
} finally { Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue }
