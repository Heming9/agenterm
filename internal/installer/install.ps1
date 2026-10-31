param([Parameter(Mandatory=$true)][string]$Invitation)
$ErrorActionPreference = 'Stop'
$Version = '@RELEASE_TAG@'
$architecture = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant()
switch ($architecture) { 'x64' { $arch = 'amd64' } 'arm64' { $arch = 'arm64' } default { throw 'Unsupported architecture' } }
$base = "https://github.com/Heming9/agenterm/releases/download/$Version"
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
  Invoke-WebRequest "$base/SHA256SUMS" -OutFile (Join-Path $work 'SHA256SUMS')
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
    Write-Host 'Using verified cached client.'
  } else {
    Write-Host 'Downloading client.'
    $expectedAsset = Get-ExpectedHash $asset
    Invoke-WebRequest "$base/$asset" -OutFile $file
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
  }
  & $program -invite $Invitation
} finally { Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue }
