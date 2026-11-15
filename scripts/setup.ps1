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
  $taskName = 'Gossip Dashboard ' + [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
  $currentTask = $null
  if (Get-Command Get-ScheduledTask -ErrorAction SilentlyContinue) {
    $currentTask = Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
  }
  if ($currentTask -and $currentTask.State -eq 'Running') {
    Stop-ScheduledTask -TaskName $taskName
    for ($i = 0; $i -lt 15 -and (Get-ScheduledTask -TaskName $taskName).State -eq 'Running'; $i++) {
      Start-Sleep -Seconds 1
    }
  }
  Copy-Item (Join-Path $tmp 'gossip.exe') (Join-Path $binDir 'gossip.exe') -Force
  $skillSource = Join-Path $tmp 'skills\gossip\SKILL.md'
  Write-Host "Installed Gossip at $binDir."
  Write-Host 'Choose an agent to configure:'
  Write-Host '  1) Codex  2) Antigravity  3) OpenCode  4) Claude Code  5) Gemini CLI  6) Skill only'
  $choice = Read-Host 'Selection [1-6]'
  $credential = ''
  switch ($choice) {
    '1' { $client = 'codex'; $skill = Join-Path $env:USERPROFILE '.agents\skills\gossip' }
    '2' { $client = 'antigravity'; $skill = Join-Path $env:USERPROFILE '.gemini\config\skills\gossip' }
    '3' { $client = 'opencode'; $skill = Join-Path $env:USERPROFILE '.config\opencode\skills\gossip' }
    '4' { $client = 'claude'; $skill = Join-Path $env:USERPROFILE '.claude\skills\gossip' }
    '5' { $client = 'gemini'; $skill = Join-Path $env:USERPROFILE '.gemini\skills\gossip' }
    '6' { $client = 'none'; $skill = Join-Path $env:USERPROFILE '.agents\skills\gossip' }
    default { throw 'Choose a number from 1 to 6.' }
  }
  New-Item -ItemType Directory -Path $skill -Force | Out-Null
  Copy-Item $skillSource (Join-Path $skill 'SKILL.md') -Force
  Write-Host "Installed Gossip skill at $skill."
  if ($client -ne 'none') {
    $profile = Read-Host 'Local Gossip identity profile [default]'
    if (-not $profile) { $profile = 'default' }
      $exe = Join-Path $binDir 'gossip.exe'
      switch ($client) {
        'codex' {
          if (-not (Get-Command codex -ErrorAction SilentlyContinue)) { throw 'Install Codex CLI, then rerun this setup.' }
          $codexHome = if ($env:CODEX_HOME) { $env:CODEX_HOME } else { Join-Path $env:USERPROFILE '.codex' }
          New-Item -ItemType Directory -Path $codexHome -Force | Out-Null
          $codexConfig = Join-Path $codexHome 'config.toml'
          if ((Test-Path -LiteralPath $codexConfig) -and (Select-String -LiteralPath $codexConfig -Pattern '^\[mcp_servers\.gossip\]' -Quiet)) {
            $backup = "$codexConfig.backup-$(Get-Date -Format yyyyMMdd-HHmmss)"
            Copy-Item -LiteralPath $codexConfig -Destination $backup
            Write-Host "Backed up the previous Codex MCP config to $backup"
          }
          & codex mcp add gossip -- $exe coord-mcp --profile $profile
        }
        'antigravity' {
          $configPath = Join-Path $env:USERPROFILE '.gemini\config\mcp_config.json'
          New-Item -ItemType Directory -Path (Split-Path $configPath) -Force | Out-Null
          $config = if (Test-Path -LiteralPath $configPath) { Get-Content -LiteralPath $configPath -Raw | ConvertFrom-Json } else { [pscustomobject]@{} }
          if (-not $config.mcpServers) { $config | Add-Member -NotePropertyName mcpServers -NotePropertyValue ([pscustomobject]@{}) }
          if ($config.mcpServers.PSObject.Properties.Name -contains 'gossip') {
            $backup = "$configPath.backup-$(Get-Date -Format yyyyMMdd-HHmmss)"
            Copy-Item -LiteralPath $configPath -Destination $backup
            Write-Host "Backed up the previous Gossip entry to $backup"
          }
          $entry = [pscustomobject]@{ command = $exe; args = @('coord-mcp', '--profile', $profile) }
          $config.mcpServers | Add-Member -NotePropertyName gossip -NotePropertyValue $entry -Force
          $config | ConvertTo-Json -Depth 100 | Set-Content -LiteralPath $configPath -Encoding UTF8
        }
        'opencode' {
          if (-not (Get-Command opencode -ErrorAction SilentlyContinue)) { throw 'Install OpenCode, then rerun this setup.' }
          $configRoot = if ($env:XDG_CONFIG_HOME) { $env:XDG_CONFIG_HOME } else { Join-Path $env:USERPROFILE '.config' }
          $opencodeConfig = Join-Path $configRoot 'opencode\opencode.json'
          New-Item -ItemType Directory -Path (Split-Path $opencodeConfig) -Force | Out-Null
          $config = if (Test-Path -LiteralPath $opencodeConfig) { Get-Content -LiteralPath $opencodeConfig -Raw | ConvertFrom-Json } else { [pscustomobject]@{ '$schema' = 'https://opencode.ai/config.json' } }
          if (-not $config -or $config -isnot [pscustomobject]) { throw 'OpenCode config must be a JSON object; file left unchanged.' }
          if (-not $config.mcp) { $config | Add-Member -NotePropertyName mcp -NotePropertyValue ([pscustomobject]@{}) }
          $oldMcp = $config.mcp
          $servers = [ordered]@{}
          if ($oldMcp.servers) {
            foreach ($property in $oldMcp.servers.PSObject.Properties) { $servers[$property.Name] = $property.Value }
          }
          $needsBackup = $servers.Contains('gossip') -or @($oldMcp.PSObject.Properties | Where-Object { $_.Name -ne 'servers' }).Count -gt 0
          if ($needsBackup -and (Test-Path -LiteralPath $opencodeConfig)) {
            $backup = "$opencodeConfig.backup-$(Get-Date -Format yyyyMMdd-HHmmss)"
            Copy-Item -LiteralPath $opencodeConfig -Destination $backup
            Write-Host "Backed up the previous OpenCode config to $backup"
          }
          $servers['gossip'] = [pscustomobject]@{ type = 'local'; command = @($exe, 'coord-mcp', '--profile', $profile) }
          $mcpMerged = [ordered]@{}
          foreach ($property in $oldMcp.PSObject.Properties) {
            if ($property.Name -ne 'servers') { $mcpMerged[$property.Name] = $property.Value }
          }
          $mcpMerged['servers'] = [pscustomobject]$servers
          $config.mcp = [pscustomobject]$mcpMerged
          $config | ConvertTo-Json -Depth 100 | Set-Content -LiteralPath $opencodeConfig -Encoding UTF8
        }
        'claude' {
          if (-not (Get-Command claude -ErrorAction SilentlyContinue)) { throw 'Install Claude Code, then rerun this setup.' }
          & claude mcp add --scope user gossip -- $exe coord-mcp --profile $profile
        }
        'gemini' {
          if (-not (Get-Command gemini -ErrorAction SilentlyContinue)) { throw 'Install Gemini CLI, then rerun this setup.' }
          & gemini mcp add --scope user gossip $exe coord-mcp --profile $profile
        }
      }
      if ($LASTEXITCODE -ne 0) { throw "MCP setup failed for $client (exit $LASTEXITCODE)." }
      Write-Host "Configured Gossip MCP for $client. Restart the app and verify the connection."
  }
  $serviceExe = Join-Path $binDir 'gossip.exe'
  try {
    if ((& $serviceExe coord-ui --service-capability 2>$null) -ne 'gossip-inbox-v1') {
      throw 'This Gossip release does not support inbox service autostart.'
    }
    $user = [Security.Principal.WindowsIdentity]::GetCurrent().Name
    $action = New-ScheduledTaskAction -Execute $serviceExe -Argument 'coord-ui --listen 127.0.0.1'
    $trigger = New-ScheduledTaskTrigger -AtLogOn -User $user
    $principal = New-ScheduledTaskPrincipal -UserId $user -LogonType Interactive
    $settings = New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::Zero) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
    Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $trigger -Principal $principal -Settings $settings -Force | Out-Null
    Start-ScheduledTask -TaskName $taskName
    $dashboardPort = if ($env:GOSSIP_DASHBOARD_PORT) { $env:GOSSIP_DASHBOARD_PORT } else { '47281' }
    $healthy = $false
    for ($i = 0; $i -lt 15 -and -not $healthy; $i++) {
      try {
        $reply = Invoke-WebRequest "http://127.0.0.1:${dashboardPort}/healthz" -UseBasicParsing -TimeoutSec 1
        $serverPid = 0
        if ($reply.Content -eq 'gossip-inbox-v1' -and
            [int]::TryParse([string]$reply.Headers['Gossip-Service-Pid'], [ref]$serverPid) -and
            $serverPid -gt 0) {
          # Match the response to an instance of this exact scheduled task.
          $scheduler = New-Object -ComObject 'Schedule.Service'
          $scheduler.Connect()
          $registered = $scheduler.GetFolder('\').GetTask($taskName)
          foreach ($instance in $registered.GetInstances(0)) {
            $process = Get-CimInstance Win32_Process -Filter "ProcessId = $serverPid"
            if ($process -and $process.ExecutablePath -eq $serviceExe -and
                ($instance.EnginePID -eq $serverPid -or $instance.EnginePID -eq $process.ParentProcessId)) {
              $healthy = $true
              break
            }
          }
        }
      } catch {
        $healthy = $false
      }
      if (-not $healthy) { Start-Sleep -Seconds 1 }
    }
    if (-not $healthy) { throw "Gossip did not become healthy on port ${dashboardPort}. Check Task Scheduler and whether the port is occupied." }
    Write-Host "Gossip starts at sign-in. Open http://127.0.0.1:${dashboardPort} to add a project."
  } catch {
    Write-Warning $_.Exception.Message
    Write-Host "Start Gossip manually with: & `"$serviceExe`" coord-ui"
  }
  Write-Host "Manual guide: https://github.com/$repo/blob/main/docs/installation/README.md"
} finally {
  Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
}
