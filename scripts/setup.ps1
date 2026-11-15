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
  $frpSource = Join-Path $tmp 'gossip-frp.exe'
  if (Test-Path -LiteralPath $frpSource) {
    Copy-Item $frpSource (Join-Path $binDir 'gossip-frp.exe') -Force
  }
  $exe = Join-Path $binDir 'gossip.exe'
  $skillSource = Join-Path $tmp 'skills\gossip\SKILL.md'
  Write-Host "Installed Gossip at $binDir."
  $binaryOnly = ($env:GOSSIP_BINARY_ONLY -eq '1') -or ($args -contains '--binary-only')
  if ($binaryOnly) {
    Write-Host 'Binary-only mode complete.'
    Write-Host "Next: auto-configure all detected coding agents with:`n  & `"$exe`" install"
  } else {
    Write-Host 'Choose an agent to configure:'
    Write-Host '  0) Auto-configure all detected (recommended)  1) Codex  2) Antigravity  3) OpenCode  4) Claude Code  5) Gemini CLI  6) Skill only'
    $choice = Read-Host 'Selection [0-6, default 0]'
    if (-not $choice) { $choice = '0' }
    if ($choice -eq '0') {
      $profile = Read-Host 'Local Gossip identity profile [default]'
      if (-not $profile) { $profile = 'default' }
      & $exe install --all --profile $profile
      if ($LASTEXITCODE -ne 0) { throw "Gossip auto-install failed (exit $LASTEXITCODE)." }
    } else {
      switch ($choice) {
        '1' { $client = 'codex'; $skill = Join-Path $env:USERPROFILE '.agents\skills\gossip' }
        '2' { $client = 'antigravity'; $skill = Join-Path $env:USERPROFILE '.gemini\config\skills\gossip' }
        '3' { $client = 'opencode'; $skill = Join-Path $env:USERPROFILE '.config\opencode\skills\gossip' }
        '4' { $client = 'claude'; $skill = Join-Path $env:USERPROFILE '.claude\skills\gossip' }
        '5' { $client = 'gemini'; $skill = Join-Path $env:USERPROFILE '.gemini\skills\gossip' }
        '6' { $client = 'none'; $skill = Join-Path $env:USERPROFILE '.agents\skills\gossip' }
        default { throw 'Choose a number from 0 to 6.' }
      }
      New-Item -ItemType Directory -Path $skill -Force | Out-Null
      Copy-Item $skillSource (Join-Path $skill 'SKILL.md') -Force
      Write-Host "Installed Gossip skill at $skill."
      if ($client -ne 'none') {
        $profile = Read-Host 'Local Gossip identity profile [default]'
        if (-not $profile) { $profile = 'default' }
        & $exe install --client $client --profile $profile
        if ($LASTEXITCODE -ne 0) { throw "MCP setup failed for $client (exit $LASTEXITCODE)." }
        Write-Host "Configured Gossip MCP for $client. Restart the app and verify the connection."
      }
    }
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
