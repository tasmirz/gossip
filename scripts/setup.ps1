$ErrorActionPreference = 'Stop'
$repo = 'tasmirz/gossip'
$releases = Invoke-RestMethod "https://api.github.com/repos/$repo/releases?per_page=10"
if (-not $releases -or $releases.Count -eq 0) { throw 'No Gossip binary release is available yet.' }
$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
  'AMD64' { 'amd64' }
  'ARM64' { 'arm64' }
  default { throw "Unsupported Windows architecture: $env:PROCESSOR_ARCHITECTURE" }
}
$release = $releases | Where-Object { $candidate = "gossip_$($_.tag_name)_windows_$arch.zip"; $_.assets.name -contains $candidate } | Select-Object -First 1
if (-not $release) { throw "No Gossip binary release is available for Windows $arch." }
$tag = $release.tag_name
$asset = "gossip_${tag}_windows_${arch}.zip"
$base = "https://github.com/$repo/releases/download/$tag"
$root = Join-Path $env:LOCALAPPDATA 'Gossip'
$tmp = Join-Path ([IO.Path]::GetTempPath()) ([guid]::NewGuid().ToString())
New-Item -ItemType Directory -Path $tmp -Force | Out-Null
try {
  Write-Host "Downloading Gossip $tag for Windows $arch..."
  Invoke-WebRequest "$base/$asset" -OutFile (Join-Path $tmp $asset) -UseBasicParsing
  Invoke-WebRequest "$base/$asset.sha256" -OutFile (Join-Path $tmp "$asset.sha256") -UseBasicParsing
  $expected = ((Get-Content (Join-Path $tmp "$asset.sha256") -Raw) -split '\s+')[0]
  $actual = (Get-FileHash (Join-Path $tmp $asset) -Algorithm SHA256).Hash.ToLowerInvariant()
  if ($actual -ne $expected.ToLowerInvariant()) { throw 'Release archive SHA-256 verification failed.' }
  Expand-Archive (Join-Path $tmp $asset) -DestinationPath $tmp -Force
  $binDir = Join-Path $root 'bin'
  New-Item -ItemType Directory -Path $binDir -Force | Out-Null
  Copy-Item (Join-Path $tmp 'gossip.exe') (Join-Path $binDir 'gossip.exe') -Force
  $skillSource = Join-Path $tmp 'skills\gossip\SKILL.md'
  Write-Host "Installed Gossip at $binDir."
  Write-Host 'Choose an agent to configure:'
  Write-Host '  1) Codex  2) Claude Code  3) Gemini CLI  4) Skill only'
  $choice = Read-Host 'Selection [1-4]'
  $credential = ''
  switch ($choice) {
    '1' { $client = 'codex'; $skill = Join-Path $env:USERPROFILE '.agents\skills\gossip' }
    '2' { $client = 'claude'; $skill = Join-Path $env:USERPROFILE '.claude\skills\gossip' }
    '3' { $client = 'gemini'; $skill = Join-Path $env:USERPROFILE '.gemini\skills\gossip' }
    '4' { $client = 'none'; $skill = Join-Path $env:USERPROFILE '.agents\skills\gossip' }
    default { throw 'Choose 1, 2, 3, or 4.' }
  }
  New-Item -ItemType Directory -Path $skill -Force | Out-Null
  Copy-Item $skillSource (Join-Path $skill 'SKILL.md') -Force
  Write-Host "Installed Gossip skill at $skill."
  if ($client -ne 'none') {
    $credential = Read-Host 'Path to this project’s private credential JSON (blank to configure MCP later)'
    if ($credential) {
      if (-not (Test-Path -LiteralPath $credential -PathType Leaf)) { throw "Credential file not found: $credential" }
      $credential = [IO.Path]::GetFullPath($credential)
      $clientCommand = @{ codex = 'codex'; claude = 'claude'; gemini = 'gemini' }[$client]
      if (-not (Get-Command $clientCommand -ErrorAction SilentlyContinue)) { throw "Install $clientCommand, then rerun this setup." }
      $exe = Join-Path $binDir 'gossip.exe'
      switch ($client) {
        'codex' { & codex mcp add gossip -- $exe coord-mcp --credential-file $credential }
        'claude' { & claude mcp add --scope user gossip -- $exe coord-mcp --credential-file $credential }
        'gemini' { & gemini mcp add --scope user gossip $exe coord-mcp --credential-file $credential }
      }
      if ($LASTEXITCODE -ne 0) { throw "MCP setup failed for $client (exit $LASTEXITCODE)." }
      Write-Host "Configured Gossip MCP for $client. Restart the app and verify the connection."
    }
  }
  Write-Host "Next: run '& `"$binDir\gossip.exe`" coord-ui', open http://127.0.0.1:8081, and choose Create local project."
  Write-Host "Manual guide: https://github.com/$repo/blob/main/docs/installation/README.md"
} finally {
  Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
}
