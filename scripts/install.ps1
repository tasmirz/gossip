<#
.SYNOPSIS
    Gossip Universal Auto-Installer for Windows PowerShell
.DESCRIPTION
    Installs Gossip binaries and auto-configures MCP servers for Antigravity, OpenCode, Claude Desktop, Cursor, and VS Code.
.EXAMPLE
    .\scripts\install.ps1 -All
    .\scripts\install.ps1 -Client claude
    .\scripts\install.ps1 -DryRun
#>
[CmdletBinding()]
param (
    [switch]$All = $true,
    [string]$Client = "",
    [string]$BinDir = "",
    [switch]$DryRun = $false,
    [switch]$WithFRP = $false,
    [string]$FRPServer = "",
    [string]$User = "admin",
    [string]$Pass = "SecureMeshPass123"
)

$ErrorActionPreference = "Stop"

Write-Host "+--------------------------------------------------------+"
Write-Host "| GOSSIP UNIVERSAL AUTO-INSTALLER (Windows PowerShell)   |"
Write-Host "| PLATFORM: Windows // ARCH: $env:PROCESSOR_ARCHITECTURE                         |"
Write-Host "+--------------------------------------------------------+"

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$RepoRoot = Split-Path -Parent $ScriptDir

# 1. Target Binary Directory
if (-not $BinDir) {
    $BinDir = Join-Path $env:LOCALAPPDATA "Programs\gossip"
}

Write-Host "[1/6] Binary Installation Destination: $BinDir"
if (-not $DryRun) {
    if (-not (Test-Path $BinDir)) {
        New-Item -ItemType Directory -Path $BinDir -Force | Out-Null
    }

    # If Go compiler is installed, build
    if (Get-Command go -ErrorAction SilentlyContinue) {
        Write-Host "  -> Compiling gossip.exe from source..."
        Push-Location $RepoRoot
        try {
            go build -o "$BinDir\gossip.exe" .\cmd\gossip
            go build -o "$BinDir\gossip-mcp.exe" .\cmd\gossip
        } finally {
            Pop-Location
        }
    } elseif (Test-Path "$RepoRoot\bin\gossip-windows-amd64.exe") {
        Copy-Item "$RepoRoot\bin\gossip-windows-amd64.exe" "$BinDir\gossip.exe" -Force
        Copy-Item "$RepoRoot\bin\gossip-windows-amd64.exe" "$BinDir\gossip-mcp.exe" -Force
    } else {
        Write-Warning "No prebuilt Windows binary found and Go compiler not in PATH."
    }

    # Add to User PATH if missing
    $UserPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if ($UserPath -notlike "*$BinDir*") {
        Write-Host "  -> Adding $BinDir to User PATH..."
        [Environment]::SetEnvironmentVariable("Path", "$UserPath;$BinDir", "User")
        $env:Path = "$env:Path;$BinDir"
    }
    Write-Host "  [OK] Binaries installed at $BinDir\gossip.exe"
} else {
    Write-Host "  [DRY-RUN] Would install binaries to $BinDir"
}

function Update-McpJson {
    param (
        [string]$FilePath,
        [string]$ServerName,
        [string]$CommandPath
    )

    if ($DryRun) {
        Write-Host "  [DRY-RUN] Would register '$ServerName' in $FilePath"
        return
    }

    $ParentDir = Split-Path -Parent $FilePath
    if (-not (Test-Path $ParentDir)) {
        New-Item -ItemType Directory -Path $ParentDir -Force | Out-Null
    }

    $jsonObj = [PSCustomObject]@{}
    if (Test-Path $FilePath) {
        try {
            $raw = Get-Content -Raw -Path $FilePath -ErrorAction SilentlyContinue
            if ($raw) {
                $jsonObj = $raw | ConvertFrom-Json
            }
        } catch {}
    }

    if (-not $jsonObj.mcpServers) {
        $jsonObj | Add-Member -MemberType NoteProperty -Name "mcpServers" -Value ([PSCustomObject]@{}) -Force
    }

    $srvObj = [PSCustomObject]@{
        command = $CommandPath
        args = @("mcp")
    }

    $jsonObj.mcpServers | Add-Member -MemberType NoteProperty -Name $ServerName -Value $srvObj -Force

    $jsonObj | ConvertTo-Json -Depth 10 | Set-Content -Path $FilePath -Encoding UTF8
    Write-Host "  [OK] Registered MCP in $FilePath"
}

$ExePath = Join-Path $BinDir "gossip.exe"

# 2. Configure Antigravity
if ($All -or ($Client -like "*antigravity*")) {
    Write-Host "[2/6] Configuring Antigravity Agent & MCP plugin..."
    $AgyCfg = Join-Path $env:USERPROFILE ".gemini\config\config.json"
    $AgyMcpCfg = Join-Path $env:USERPROFILE ".gemini\config\mcp_config.json"
    Update-McpJson -FilePath $AgyCfg -ServerName "gossip" -CommandPath $ExePath
    Update-McpJson -FilePath $AgyMcpCfg -ServerName "gossip" -CommandPath $ExePath
}

# 3. Configure OpenCode
if ($All -or ($Client -like "*opencode*")) {
    Write-Host "[3/6] Configuring OpenCode MCP..."
    $OcCfg = Join-Path $env:USERPROFILE ".config\opencode\opencode.json"
    Update-McpJson -FilePath $OcCfg -ServerName "gossip" -CommandPath $ExePath
}

# 4. Configure Claude Desktop
if ($All -or ($Client -like "*claude*")) {
    Write-Host "[4/6] Configuring Claude Desktop MCP..."
    $ClaudeCfg = Join-Path $env:APPDATA "Claude\claude_desktop_config.json"
    Update-McpJson -FilePath $ClaudeCfg -ServerName "gossip" -CommandPath $ExePath
}

# 5. Configure Cursor
if ($All -or ($Client -like "*cursor*")) {
    Write-Host "[5/6] Configuring Cursor MCP..."
    $CursorCfg = Join-Path $env:USERPROFILE ".cursor\mcp.json"
    Update-McpJson -FilePath $CursorCfg -ServerName "gossip" -CommandPath $ExePath
}

# 6. Configure VS Code
if ($All -or ($Client -like "*vscode*")) {
    Write-Host "[6/6] Configuring VS Code Workspace MCP..."
    $VscodeCfg = ".vscode\mcp.json"
    if ((Test-Path ".vscode") -or ($Client -like "*vscode*")) {
        Update-McpJson -FilePath $VscodeCfg -ServerName "gossip" -CommandPath $ExePath
    } else {
        Write-Host "  [SKIP] No .vscode directory found in current working directory"
    }
}

# Optional FRP Tunnel
if ($WithFRP) {
    Write-Host ""
    Write-Host "[TUNNEL] Configuring Protected FRP Reverse-Proxy Tunnel..."
    if ($FRPServer) {
        & "$ExePath" tunnel init --server $FRPServer --user $User --pass $Pass
    } else {
        Write-Host "  [NOTE] Run 'gossip tunnel init --server <addr> --user <user> --pass <pass>' to configure tunnel."
    }
}

Write-Host ""
Write-Host "+--------------------------------------------------------+"
Write-Host "| [SUCCESS] GOSSIP INSTALLATION COMPLETED                |"
Write-Host "+--------------------------------------------------------+"
Write-Host "Verify status:          gossip status"
Write-Host "Start MCP stdio mode:   gossip mcp"
Write-Host "Launch Web Control:     gossip ui --port 8080"
Write-Host "Periodic cron check:    gossip cron --interval 30s"
Write-Host "Protected FRP tunnel:   gossip tunnel status"
Write-Host "+--------------------------------------------------------+"
