# install-menubar-windows.ps1 — install the Codex Remote system-tray app (status + QR).
#
# Run in an ELEVATED PowerShell, AFTER install-agent-windows.ps1 (it reuses the
# per-machine token that script created). Installs a Scheduled Task that runs the
# tray in your interactive session at logon, so a ">_" icon shows in the tray —
# click it to copy the connect string or pop a QR the phone scans.
#
#   ./install-menubar-windows.ps1 [-MachineId reechi-win] [-AgentKey <shared>]
#
# -AgentKey is optional: with it the tray shows true hub online status (queries
# /machines); without it the tray shows status from whether the agent task runs.
#
# Copy codexmenubar-win-amd64.exe (GOOS=windows GOARCH=amd64 CGO_ENABLED=0
# go build -ldflags="-H windowsgui -s -w" -o codexmenubar-win-amd64.exe
# ./cmd/codexmenubar) next to this script, or pass -TrayExe.
param(
  [string]$MachineId = $env:COMPUTERNAME.ToLower(),
  [string]$MachineName = $env:COMPUTERNAME,
  [string]$Hub = "wss://relay.example.com/agent",
  [string]$AgentKey = "",
  [string]$TrayExe = "$PSScriptRoot\codexmenubar-win-amd64.exe",
  [string]$TaskName = "CodexRemoteTray"
)
$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

if (-not (Test-Path $TrayExe)) { throw "tray exe not found: $TrayExe" }

$dir = "$env:ProgramData\codex-remote"
New-Item -ItemType Directory -Force -Path $dir | Out-Null
$exe = "$dir\codexmenubar.exe"
Copy-Item -Force $TrayExe $exe

$tokFile = "$dir\machine-$MachineId.token"
if (-not (Test-Path $tokFile)) { throw "$tokFile not found — run install-agent-windows.ps1 first" }
$token = (Get-Content $tokFile -Raw).Trim()

# Config the tray reads. AGENT_KEY is optional (only for accurate hub status).
$lines = @("MACHINE_ID=$MachineId", "MACHINE_NAME=$MachineName", "HUB=$Hub", "TOKEN=$token")
if ($AgentKey) { $lines += "AGENT_KEY=$AgentKey" }
$lines | Set-Content -Path "$dir\menubar.env" -Encoding ascii

# Tray MUST run in the interactive desktop session — Interactive logon trigger.
Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false -ErrorAction SilentlyContinue
$action    = New-ScheduledTaskAction -Execute $exe
$trigger   = New-ScheduledTaskTrigger -AtLogOn -User "$env:USERDOMAIN\$env:USERNAME"
$principal = New-ScheduledTaskPrincipal -UserId "$env:USERDOMAIN\$env:USERNAME" -LogonType Interactive -RunLevel Highest
$set       = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
               -StartWhenAvailable -ExecutionTimeLimit ([TimeSpan]::Zero)
Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger -Principal $principal -Settings $set | Out-Null
Start-ScheduledTask -TaskName $TaskName
Start-Sleep 4

Write-Host ""
Write-Host "OK Tray '$TaskName' installed ($MachineId), runs at logon. State: $((Get-ScheduledTask -TaskName $TaskName).State)"
Write-Host "   Look for the green >_ icon in the system tray (you may need to expand hidden icons)."
Write-Host "   Phone connects to: wss://relay.example.com/ws?token=$token"
Write-Host "   Manage: Unregister-ScheduledTask $TaskName ; Get-Process codexmenubar | Stop-Process"
