<#
.SYNOPSIS
    Gossip Universal Auto-Installer for Windows PowerShell
.DESCRIPTION
    Installs Gossip and prepares editor integrations; MCP registration is deferred until a project credential exists.
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
        } finally {
            Pop-Location
        }
    } elseif (Test-Path "$RepoRoot\bin\gossip-windows-amd64.exe") {
        Copy-Item "$RepoRoot\bin\gossip-windows-amd64.exe" "$BinDir\gossip.exe" -Force
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

function Explain-McpSetup {
    param ([string]$FilePath, [string]$ServerName, [string]$CommandPath)
    Write-Host "  [NOTE] Leaving $FilePath unchanged; configure coord-mcp after creating a project credential."
}

$ExePath = Join-Path $BinDir "gossip.exe"

# 2. Configure Antigravity
if ($All -or ($Client -like "*antigravity*")) {
    Write-Host "[2/6] Preparing Antigravity integration..."
    $AgyCfg = Join-Path $env:USERPROFILE ".gemini\config\config.json"
    $AgyMcpCfg = Join-Path $env:USERPROFILE ".gemini\config\mcp_config.json"
    Explain-McpSetup -FilePath $AgyCfg -ServerName "gossip" -CommandPath $ExePath
    Explain-McpSetup -FilePath $AgyMcpCfg -ServerName "gossip" -CommandPath $ExePath
}

# 3. Configure OpenCode
if ($All -or ($Client -like "*opencode*")) {
    Write-Host "[3/6] Preparing OpenCode integration..."
    $OcCfg = Join-Path $env:USERPROFILE ".config\opencode\opencode.json"
    Explain-McpSetup -FilePath $OcCfg -ServerName "gossip" -CommandPath $ExePath
}

# 4. Configure Claude Desktop
if ($All -or ($Client -like "*claude*")) {
    Write-Host "[4/6] Preparing Claude Desktop integration..."
    $ClaudeCfg = Join-Path $env:APPDATA "Claude\claude_desktop_config.json"
    Explain-McpSetup -FilePath $ClaudeCfg -ServerName "gossip" -CommandPath $ExePath
}

# 5. Configure Cursor
if ($All -or ($Client -like "*cursor*")) {
    Write-Host "[5/6] Preparing Cursor integration..."
    $CursorCfg = Join-Path $env:USERPROFILE ".cursor\mcp.json"
    Explain-McpSetup -FilePath $CursorCfg -ServerName "gossip" -CommandPath $ExePath
}

# 6. Configure VS Code
if ($All -or ($Client -like "*vscode*")) {
    Write-Host "[6/6] Preparing VS Code Workspace integration..."
    $VscodeCfg = ".vscode\mcp.json"
    if ((Test-Path ".vscode") -or ($Client -like "*vscode*")) {
        Explain-McpSetup -FilePath $VscodeCfg -ServerName "gossip" -CommandPath $ExePath
    } else {
        Write-Host "  [SKIP] No .vscode directory found in current working directory"
    }
}

# Optional FRP Tunnel
if ($WithFRP) {
    Write-Host ""
    Write-Host "[TUNNEL] FRP forwards TCP to a coord-serve mutual-TLS listener."
    Write-Host "  [NOTE] Configure the home certificate and client grants, then run 'gossip coord-frp'."
}

Write-Host ""
Write-Host "+--------------------------------------------------------+"
Write-Host "| [SUCCESS] GOSSIP INSTALLATION COMPLETED                |"
Write-Host "+--------------------------------------------------------+"
Write-Host "Verify status:          gossip status"
Write-Host "Connect a project folder, then configure coord-mcp with its private project credential."
Write-Host "Launch Web Control:     gossip ui --port 8080"
Write-Host "Periodic cron check:    gossip cron --interval 30s"
Write-Host "FRP transport:          gossip coord-frp (after coord-serve is configured)"
Write-Host "+--------------------------------------------------------+"
