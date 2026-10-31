param([Parameter(Mandatory=$true)][string]$Version, [Parameter(Mandatory=$true)][string]$Invitation)
$ErrorActionPreference = 'Stop'
$architecture = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant()
switch ($architecture) { 'x64' { $arch = 'amd64' } 'arm64' { $arch = 'arm64' } default { throw 'Unsupported architecture' } }
$base = "https://github.com/Heming9/agenterm/releases/download/$Version"
$work = Join-Path ([System.IO.Path]::GetTempPath()) ("agenterm-" + [System.Guid]::NewGuid())
New-Item -ItemType Directory -Path $work | Out-Null
try {
  $asset = "agenterm-cli-windows-$arch.zip"
  $binary = "agenterm-cli-windows-$arch.exe"
  $file = Join-Path $work $asset
  Invoke-WebRequest "$base/SHA256SUMS" -OutFile (Join-Path $work 'SHA256SUMS')
  Invoke-WebRequest "$base/$asset" -OutFile $file
  $line = Get-Content (Join-Path $work 'SHA256SUMS') | Where-Object { $_ -match ('^[a-fA-F0-9]{64}  ' + [regex]::Escape($asset) + '$') } | Select-Object -First 1
  if (-not $line -or (Get-FileHash -Algorithm SHA256 $file).Hash -ne $line.Substring(0, 64)) { throw 'Missing checksum or checksum mismatch' }
  Expand-Archive -LiteralPath $file -DestinationPath $work
  $program = Join-Path $work $binary
  & $program -invite $Invitation
} finally { Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue }
