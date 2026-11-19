# Agenterm installer
# Project: https://github.com/Heming9/agenterm
# SPDX-License-Identifier: Apache-2.0
# License: https://www.apache.org/licenses/LICENSE-2.0
param([AllowEmptyString()][string]$Invitation, [Parameter(Mandatory=$true)][string]$Base, [string]$CredentialKind = "invitation", [string]$InstallMode = "cached")
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
    'installer.connection.progress' { '@COLOR_PRIMARY@'; break }
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
$origin = $Base -replace '/downloads/[^/]+$', ''
Show-Banner
Write-Host ''
Write-Styled (Text 'installer.remote.control.authorization') '@COLOR_WARNING@'
Write-Host (Text 'installer.server.s' @($origin))
Say 'installer.the.server.can.control.this.computer.with'
Write-Host ''
Say 'installer.consent.menu'
$choice = 0
$numberPrompt = 'installer.consent.number'
if ($InstallMode -eq 'temporary-assistance') {
  $choices = @((Text 'installer.consent.allow'), (Text 'installer.consent.no'))
  $numberPrompt = 'installer.consent.binary.number'
} else {
  $choices = @((Text 'installer.consent.temporary'), (Text 'installer.consent.persistent'), (Text 'installer.consent.no'))
}
$lastChoice = $choices.Count - 1
if (-not [Console]::IsInputRedirected -and -not [Console]::IsOutputRedirected) {
  $previousLines = 0
  while ($true) {
    $vertical = [Console]::WindowWidth -lt 120
    if ($previousLines -gt 0) {
      $top = [Math]::Max(0, [Console]::CursorTop - ($previousLines - 1))
      for ($line = 0; $line -lt $previousLines; $line++) {
        [Console]::SetCursorPosition(0, $top + $line)
        Write-Host (' ' * [Math]::Max(0, [Console]::WindowWidth - 1)) -NoNewline
      }
      [Console]::SetCursorPosition(0, $top)
    }
    Write-Host "`r" -NoNewline
    for ($i = 0; $i -lt $choices.Count; $i++) {
      if ($i -eq $choice) {
        if ($useColor) { Write-Host (" ▶ " + $choices[$i] + " ") -ForegroundColor Black -BackgroundColor White -NoNewline }
        else { Write-Host (" ▶ " + $choices[$i] + " ") -NoNewline }
      } else { Write-Host ("   " + $choices[$i] + " ") -NoNewline }
      if ($vertical -and $i -lt $lastChoice) { Write-Host '   ' } else { Write-Host '   ' -NoNewline }
    }
    $previousLines = if ($vertical) { $choices.Count } else { 1 }
    $key = [Console]::ReadKey($true)
    if ($key.Key -eq 'Enter') { break }
    if (($key.Key -eq 'LeftArrow' -or $key.Key -eq 'UpArrow') -and $choice -gt 0) { $choice-- }
    if (($key.Key -eq 'RightArrow' -or $key.Key -eq 'DownArrow') -and $choice -lt $lastChoice) { $choice++ }
  }
  Write-Host ''
} else {
  $numberedChoices = for ($i = 0; $i -lt $choices.Count; $i++) { "{0}. {1}" -f ($i + 1), $choices[$i] }
  Write-Host ($numberedChoices -join '    ')
  Write-Host (Text $numberPrompt) -NoNewline
  $answer = [Console]::ReadLine()
  if ($answer -eq '1') { $choice = 0 } elseif ($answer -eq '2') { $choice = 1 } else { $choice = $lastChoice }
}
if ($choice -eq $lastChoice) { Say 'installer.connection.cancelled'; return }
$clientOptions = @()
if ($choice -eq 0) { $clientOptions += '-connection-mode=temporary' } else { $clientOptions += '-connection-mode=persistent' }
Say 'installer.connection.progress'
$architecture = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant()
switch ($architecture) { 'x64' { $arch = 'amd64' } 'arm64' { $arch = 'arm64' } default { throw (Text 'installer.unsupported.architecture') } }
Say 'installer.2.4.preparing.client'
if ($InstallMode -eq 'temporary-assistance') {
  $work = Join-Path ([IO.Path]::GetTempPath()) ('agenterm-assistance.' + [System.Guid]::NewGuid())
  $destination = $work
} else {
  if (-not $env:LOCALAPPDATA) { throw (Text 'installer.localappdata.is.not.set') }
  $destination = Join-Path $env:LOCALAPPDATA "Agenterm\cache\$Version\windows-$arch"
  New-Item -ItemType Directory -Path $destination -Force | Out-Null
  $work = Join-Path $destination ('.tmp-' + [System.Guid]::NewGuid())
}
New-Item -ItemType Directory -Path $work | Out-Null
try {
  $binary = "agenterm-client-cli-windows-$arch.exe"
  $program = Join-Path $destination $binary
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
    $staged = Join-Path $work $binary
    if ((Get-FileHash -Algorithm SHA256 -LiteralPath $staged).Hash -ne $expectedBinary) { throw (Text 'installer.client.checksum.mismatch') }
    if (Test-Path -LiteralPath $program -PathType Container) { throw (Text 'installer.client.cache.path.is.a.directory') }
    if ($work -ne $destination) {
      try {
        Move-Item -LiteralPath $staged -Destination $program -Force -ErrorAction Stop
      } catch {
        # Another running instance can keep the cached executable locked on Windows.
        # The independently verified staged client remains usable for this session.
        $program = $staged
      }
    }
    Say 'installer.client.verified'
  }
  # Keep the client in the language used for the authorization prompt, even
  # when Windows display language and regional format are different.
  $previousLanguage = $env:AGENTERM_LANG
  $previousLinkRegistration = $env:AGENTERM_SKIP_LINK_REGISTRATION
  try {
    $env:AGENTERM_LANG = if ($chinese) { 'zh-CN' } else { 'en' }
    if ($InstallMode -eq 'temporary-assistance') {
      $env:AGENTERM_SKIP_LINK_REGISTRATION = '1'
      & $program -token $Invitation -temporary-assistance
    } elseif ($Invitation) {
      if ($CredentialKind -eq "token" -or $Invitation -match "^https?://") { & $program -token $Invitation @clientOptions } else { & $program -invite $Invitation -consent-confirmed @clientOptions }
    } else {
      & $program -token= @clientOptions
    }
    $clientExitCode = $LASTEXITCODE
  } finally {
    $env:AGENTERM_LANG = $previousLanguage
    $env:AGENTERM_SKIP_LINK_REGISTRATION = $previousLinkRegistration
  }
} finally {
  if ($InstallMode -eq 'temporary-assistance') { Remove-Item -Recurse -Force -LiteralPath $work }
  else { Remove-Item -Recurse -Force -LiteralPath $work -ErrorAction SilentlyContinue }
}
if ($clientExitCode -ne 0) { exit $clientExitCode }
