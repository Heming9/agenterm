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
if (!(Test-Path -LiteralPath $bin) -or ((Get-FileHash -LiteralPath $bin -Algorithm SHA256).Hash.ToLower() -ne $expected)) {
  $tmp = Join-Path $cache ('download-' + [Guid]::NewGuid().ToString('N'))
  New-Item -ItemType Directory -Path $tmp | Out-Null
  try {
    $format = 'raw'
    $tar = Get-Command tar.exe -ErrorAction SilentlyContinue
    if ($tar) {
      $probeFile = Join-Path $tmp 'probe.tar.xz'
      try {
        [IO.File]::WriteAllBytes($probeFile, [Convert]::FromBase64String('/Td6WFoAAATm1rRGAgAhAQwAAACPmEGc4AP/AAtdAABv/f//o7d/RjIAAAAMJ2kgl2N4wwABJ4AIAAAALnmurbHEZ/sCAAAAAARZWg=='))
        & $tar.Source -tf $probeFile 2>$null | Out-Null
        if ($LASTEXITCODE -eq 0) { $format = 'tar.xz' }
      } catch { $format = 'raw' } finally { Remove-Item -LiteralPath $probeFile -ErrorAction SilentlyContinue }
    }
    if ($format -eq 'raw' -and (Get-Command Expand-Archive -ErrorAction SilentlyContinue)) { $format = 'zip' }
    $archive = $source
    if ($format -ne 'raw') { $archive = ($source -replace '\.exe$', '') + '.' + $format }
    $url = ($url -replace '/[^/]+$', '') + '/' + $archive
    $download = Join-Path $tmp $archive
    Invoke-WebRequest -UseBasicParsing -TimeoutSec 120 -Uri $url -OutFile $download
    if ($format -ne 'raw') {
      $hashFile = Join-Path $tmp 'archive.sha256'
      Invoke-WebRequest -UseBasicParsing -TimeoutSec 30 -Uri ($url + '.sha256') -OutFile $hashFile
      $archiveHash = (Get-Content -Raw -LiteralPath $hashFile).Trim()
      if ($archiveHash -notmatch '^[a-fA-F0-9]{64}$' -or (Get-FileHash -LiteralPath $download -Algorithm SHA256).Hash -ne $archiveHash) { throw 'p2p-cli archive checksum mismatch' }
      if ($format -eq 'tar.xz') {
        & $tar.Source -xf $download -C $tmp 'p2p-cli.exe'
        if ($LASTEXITCODE -ne 0) { throw 'Failed to extract p2p-cli archive' }
      } else { Expand-Archive -LiteralPath $download -DestinationPath $tmp }
    } else { Move-Item -LiteralPath $download -Destination (Join-Path $tmp 'p2p-cli.exe') }
    $staged = Join-Path $tmp 'p2p-cli.exe'
    if ((Get-FileHash -LiteralPath $staged -Algorithm SHA256).Hash.ToLower() -ne $expected) { throw 'p2p-cli checksum mismatch' }
    Move-Item -Force -LiteralPath $staged -Destination $bin
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
