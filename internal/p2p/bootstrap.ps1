$ErrorActionPreference = 'Stop'
$OutputEncoding = New-Object Text.UTF8Encoding $false
$platform = 'windows'
$arch = switch ($env:PROCESSOR_ARCHITECTURE) { 'ARM64' { 'arm64' } 'AMD64' { 'amd64' } default { throw 'Unsupported p2p-cli architecture' } }
switch ("$platform/$arch") {
@ASSET_CASES@
  default { throw 'No p2p-cli artifact for this platform' }
}
$cache = Join-Path $env:LOCALAPPDATA "agenterm\p2p-cli\@VERSION@-$expected"
$bin = Join-Path $cache 'p2p-cli.exe'
if ('@MODE@' -eq 'stop') {
  if (Test-Path -LiteralPath $bin) { & $bin stop --session '@SESSION@' }
  exit 0
}
New-Item -ItemType Directory -Force -Path $cache | Out-Null
$cached = Join-Path $cache $archive
if (!(Test-Path -LiteralPath $bin) -or !(Test-Path -LiteralPath $cached) -or ((Get-FileHash -LiteralPath $cached -Algorithm SHA256).Hash.ToLower() -ne $expected)) {
  $tmp = Join-Path $cache ('download-' + [Guid]::NewGuid().ToString('N'))
  New-Item -ItemType Directory -Path $tmp | Out-Null
  try {
    $download = Join-Path $tmp $archive
    Invoke-WebRequest -UseBasicParsing -TimeoutSec 120 -Uri $url -OutFile $download
    if ((Get-FileHash -LiteralPath $download -Algorithm SHA256).Hash.ToLower() -ne $expected) { throw 'p2p-cli checksum mismatch' }
    Expand-Archive -LiteralPath $download -DestinationPath $tmp
    Move-Item -Force -LiteralPath (Join-Path $tmp 'p2p-cli.exe') -Destination $bin
    Move-Item -Force -LiteralPath $download -Destination $cached
  } finally { Remove-Item -Recurse -Force -LiteralPath $tmp }
}
$config = $env:AGENTERM_P2P_CONFIG_B64
if (!$config) { $config = '@CONFIG_B64@' }
if (!$config) { throw 'Missing P2P negotiation parameters' }
$json = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($config))
if ('@MODE@' -eq 'foreground') {
  $json | & $bin start --config - --foreground
} else {
  $json | & $bin start --config -
}
if ($LASTEXITCODE -ne 0) { throw "p2p-cli exited with code $LASTEXITCODE" }
