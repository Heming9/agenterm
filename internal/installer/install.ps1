# Agenterm installer
# Project: https://github.com/Heming9/agenterm
# SPDX-License-Identifier: Apache-2.0
# License: https://www.apache.org/licenses/LICENSE-2.0
param([AllowEmptyString()][string]$Token, [Parameter(Mandatory=$true)][string]$Base)
$ErrorActionPreference = 'Stop'
$Version = '@RELEASE_TAG@'
$language = if ($env:AGENTERM_LANG -and $env:AGENTERM_LANG -ne 'auto') { $env:AGENTERM_LANG } else { [System.Globalization.CultureInfo]::CurrentUICulture.Name }
$chinese = $language -match '^(zh-CN|zh-SG|zh-Hans)(-|$)' -or $language -eq 'zh'
@TRANSLATIONS@
$useColor = -not [Console]::IsOutputRedirected -and $env:TERM -ne 'dumb' -and -not $env:NO_COLOR
$trueColor = $env:WT_SESSION -or $env:COLORTERM -in @('truecolor', '24bit') -or $env:TERM_PROGRAM -in @('Apple_Terminal', 'vscode')
$primarySGR = '@COLOR_PRIMARY_SGR@'
function Write-Styled([string]$Message, [string]$Color = '') {
  if ($useColor -and $trueColor -and $Color -eq '@COLOR_PRIMARY@') {
    $esc = [char]27
    Write-Host "${esc}[${primarySGR}m${Message}${esc}[0m"
  } elseif ($useColor -and $Color) {
    Write-Host $Message -ForegroundColor $Color
  } else {
    Write-Host $Message
  }
}
function Say([string]$Key) {
  $color = switch ($Key) {
    'installer.2.4.preparing.client' { '@COLOR_PRIMARY@'; break }
    'installer.cached.client.verified.skipping.download' { '@COLOR_SUCCESS@'; break }
    'installer.client.verified' { '@COLOR_SUCCESS@'; break }
    default { '' }
  }
  Write-Styled (Text $Key) $color
}
function Show-Banner {
  # Only use the block-character banner in known modern terminal hosts.
  $useUnicodeBanner = -not [Console]::IsOutputRedirected -and $env:TERM -ne 'dumb' -and
    ($env:WT_SESSION -or $env:TERM_PROGRAM -eq 'vscode')
  if ($useUnicodeBanner) {
    Write-Styled "`n  @LOGO_TOP@  @WORDMARK_TOP@" '@COLOR_PRIMARY@'
    Write-Styled '  @LOGO_MIDDLE@   @WORDMARK_BOTTOM@' '@COLOR_PRIMARY@'
    Write-Styled "  @LOGO_BOTTOM@  Agenterm   [ $Version ]`n" '@COLOR_PRIMARY@'
  } else {
    Write-Host ''
    Write-Styled "  >_<  Agenterm   [ $Version ]" '@COLOR_PRIMARY@'
    Write-Host ''
  }
  Write-Host (Text 'installer.project.s' @('https://github.com/Heming9/agenterm'))
  Say 'installer.license'
}
Show-Banner
$architecture = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant()
switch ($architecture) { 'x64' { $arch = 'amd64' } 'arm64' { $arch = 'arm64' } default { throw (Text 'installer.unsupported.architecture') } }
Say 'installer.2.4.preparing.client'
$work = Join-Path ([IO.Path]::GetTempPath()) ('agenterm-assistance.' + [System.Guid]::NewGuid())
New-Item -ItemType Directory -Path $work | Out-Null
try {
  $binary = "agenterm-client-cli-windows-$arch.exe"
  $program = Join-Path $work $binary
  Invoke-WebRequest -UseBasicParsing -TimeoutSec 15 "$base/SHA256SUMS" -OutFile (Join-Path $work 'SHA256SUMS')
  $manifest = Get-Content (Join-Path $work 'SHA256SUMS')
  function Get-ExpectedHash([string]$Name) {
    $line = $manifest | Where-Object { $_ -match ('^[a-fA-F0-9]{64}  ' + [regex]::Escape($Name) + '$') } | Select-Object -First 1
    if (-not $line) { throw (Text 'installer.missing.checksum.for.s' @($Name)) }
    return $line.Substring(0, 64)
  }
  $expectedBinary = Get-ExpectedHash $binary
  # A previous persistent authorization may have installed a reusable copy.
  $valid = $false
  if ($env:APPDATA) {
    $cached = Join-Path $env:APPDATA ("Agenterm\cli\agenterm-cli-" + $expectedBinary.ToLowerInvariant() + ".exe")
    if (Test-Path -LiteralPath $cached -PathType Leaf) {
      $item = Get-Item -LiteralPath $cached -Force
      if (-not ($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -and
          (Get-FileHash -Algorithm SHA256 -LiteralPath $cached).Hash -eq $expectedBinary) {
        Copy-Item -LiteralPath $cached -Destination $program
        $valid = (Get-FileHash -Algorithm SHA256 -LiteralPath $program).Hash -eq $expectedBinary
      }
    }
  }
  if ($valid) {
    Say 'installer.cached.client.verified.skipping.download'
  } else {
    if (Test-Path -LiteralPath $program) { Remove-Item -Force -LiteralPath $program }
    Say 'installer.downloading.client'
    $format = 'raw'
    $tar = Get-Command tar.exe -ErrorAction SilentlyContinue
    if ($tar) {
      $probeFile = Join-Path $work 'probe.tar.xz'
      try {
        [IO.File]::WriteAllBytes($probeFile, [Convert]::FromBase64String('/Td6WFoAAATm1rRGAgAhAQwAAACPmEGc4AP/AAtdAABv/f//o7d/RjIAAAAMJ2kgl2N4wwABJ4AIAAAALnmurbHEZ/sCAAAAAARZWg=='))
        & $tar.Source -tf $probeFile 2>$null | Out-Null
        if ($LASTEXITCODE -eq 0) { $format = 'tar.xz' }
      } catch { $format = 'raw' } finally { Remove-Item -LiteralPath $probeFile -ErrorAction SilentlyContinue }
    }
    if ($format -eq 'raw' -and (Get-Command Expand-Archive -ErrorAction SilentlyContinue)) { $format = 'zip' }
    $asset = $binary
    if ($format -ne 'raw') { $asset = "agenterm-client-cli-windows-$arch.$format" }
    $file = Join-Path $work $asset
    $expectedAsset = $expectedBinary
    if ($format -ne 'raw') {
      $hashFile = Join-Path $work 'archive.sha256'
      Invoke-WebRequest -UseBasicParsing -TimeoutSec 30 "$base/$asset.sha256" -OutFile $hashFile
      $expectedAsset = (Get-Content -Raw -LiteralPath $hashFile).Trim()
      if ($expectedAsset -notmatch '^[a-fA-F0-9]{64}$') { throw (Text 'installer.missing.archive.checksum') }
    }
    $ProgressPreference = 'Continue'
    Invoke-WebRequest -UseBasicParsing -TimeoutSec 180 "$base/$asset" -OutFile $file
    Say 'installer.download.complete.verifying.and.extracting'
    if ((Get-FileHash -Algorithm SHA256 -LiteralPath $file).Hash -ne $expectedAsset) { throw (Text 'installer.archive.checksum.mismatch') }
    if ($format -eq 'tar.xz') {
      & $tar.Source -xf $file -C $work $binary
      if ($LASTEXITCODE -ne 0) { throw (Text 'installer.invalid.client.archive') }
    } elseif ($format -eq 'zip') { Expand-Archive -LiteralPath $file -DestinationPath $work }
    if ((Get-FileHash -Algorithm SHA256 -LiteralPath $program).Hash -ne $expectedBinary) { throw (Text 'installer.client.checksum.mismatch') }
    Say 'installer.client.verified'
  }
  $previousSkip = $env:AGENTERM_SKIP_LINK_REGISTRATION
  try {
    $env:AGENTERM_SKIP_LINK_REGISTRATION = '1'
    $clientVersion = & $program -version
    if ($LASTEXITCODE -ne 0 -or $clientVersion -isnot [string] -or $clientVersion -notmatch '^\d+\.\d+\.\d+$' -or [version]$clientVersion -lt [version]'0.20.16') {
      throw (Text 'installer.client.authorization.version')
    }
  } finally {
    $env:AGENTERM_SKIP_LINK_REGISTRATION = $previousSkip
  }
  $previousLanguage = $env:AGENTERM_LANG
  try {
    $env:AGENTERM_LANG = if ($chinese) { 'zh-CN' } else { 'en' }
    if ($Token) {
      & $program -token $Token
    } else {
      & $program -token=
    }
    $clientExitCode = $LASTEXITCODE
  } finally {
    $env:AGENTERM_LANG = $previousLanguage
  }
} finally {
  Remove-Item -Recurse -Force -LiteralPath $work
}
if ($clientExitCode -ne 0) { exit $clientExitCode }
