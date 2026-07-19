# install-agent-windows.ps1 — register THIS Windows machine with the hub (agent mode).
# Run in an ELEVATED PowerShell. No inbound port / DNS. Installs a Scheduled Task
# (auto-start at boot, runs AS YOU) that dials the hub; prints the phone connect URL.
#
#   ./install-agent-windows.ps1 -AgentKey <shared> [-MachineId reechi-win] [-CodexPath C:\...\codex.exe]
#
# Why a Scheduled Task running as your user (not a LocalSystem service): codex reads
# its login/auth from %USERPROFILE%\.codex. A LocalSystem service runs under the
# system profile and can't see your codex auth, so codex app-server never serves —
# the task runs as you, so codex finds your auth. (Verified 2026-06-03.)
#
# Copy codexbridge-win-amd64.exe next to this script, or pass -BridgeExe. Build it
# with the GUI subsystem so it never pops a console window at logon (it logs to
# agent.log next to the exe instead):
#   GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
#     go build -ldflags="-H windowsgui -s -w" -o codexbridge-win-amd64.exe ./cmd/codexbridge
param(
  [Parameter(Mandatory=$true)][string]$AgentKey,
  [string]$Hub = "wss://relay.example.com/agent",
  [string]$MachineId = $env:COMPUTERNAME.ToLower(),
  [string]$MachineName = $env:COMPUTERNAME,
  [string]$CodexPath = "",
  [string]$BridgeExe = "$PSScriptRoot\codexbridge-win-amd64.exe",
  [string]$TaskName = "CodexRemoteAgent"
)
$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

if (-not (Test-Path $BridgeExe)) { throw "bridge exe not found: $BridgeExe (build: GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags=`"-H windowsgui -s -w`" -o codexbridge-win-amd64.exe ./cmd/codexbridge)" }

# Resolve the NATIVE codex.exe the bridge will spawn (not the npm .cmd/.ps1 wrapper).
if (-not $CodexPath) {
  $c = (Get-Command codex.exe -ErrorAction SilentlyContinue).Source
  if (-not $c) {
    # npm global install ships a Rust binary under the platform vendor dir.
    $roots = @("$env:APPDATA\npm\node_modules\@openai\codex",
               "$env:LOCALAPPDATA\Programs", "$env:ProgramFiles", "${env:ProgramFiles(x86)}")
    foreach ($r in $roots) {
      if ($r -and (Test-Path $r)) {
        $f = Get-ChildItem -Path $r -Recurse -Filter codex.exe -ErrorAction SilentlyContinue |
             Where-Object { $_.FullName -match 'vendor|bin' } | Select-Object -First 1
        if ($f) { $c = $f.FullName; break }
      }
    }
  }
  if (-not $c) { throw "codex.exe not found — pass -CodexPath C:\path\to\codex.exe (the native binary, e.g. under %APPDATA%\npm\node_modules\@openai\codex\...\vendor\...\bin\codex.exe)" }
  $CodexPath = $c
}
Write-Host "codex: $CodexPath"

# Install the bridge to a stable location.
$dir = "$env:ProgramData\codex-remote"
New-Item -ItemType Directory -Force -Path $dir | Out-Null
$exe = "$dir\codexbridge.exe"
Copy-Item -Force $BridgeExe $exe

# Per-machine token (generated once, kept beside the config; never committed).
$tokFile = "$dir\machine-$MachineId.token"
if (Test-Path $tokFile) {
  $token = (Get-Content $tokFile -Raw).Trim()
} else {
  $bytes = New-Object 'System.Byte[]' 32
  [System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($bytes)
  $token = -join ($bytes | ForEach-Object { $_.ToString('x2') })
  Set-Content -Path $tokFile -Value $token -NoNewline
}

# Need your password so the task can run as you at boot (stored encrypted by Task
# Scheduler, not in plaintext). Required for codex to find your auth.
$cred = Get-Credential -UserName "$env:USERDOMAIN\$env:USERNAME" -Message "Windows password (so the agent runs as you)"
$pw = [Runtime.InteropServices.Marshal]::PtrToStringAuto([Runtime.InteropServices.Marshal]::SecureStringToBSTR($cred.Password))

# Keep the token + agentKey OUT of the task command line: Tasks XML under
# System32\Tasks is readable by the Users group, so argv secrets leak to every
# local account. Write the agent key beside the token and pass only file PATHS on
# argv — codexbridge reads them via -token-file / -agent-key-file. Lock both files
# to SYSTEM, Administrators and the task's own user (so the agent can read them).
$keyFile = "$dir\agent-key"
Set-Content -Path $keyFile -Value $AgentKey -NoNewline -Encoding ascii
foreach ($f in @($tokFile, $keyFile)) {
  icacls $f /inheritance:r /grant:r "*S-1-5-18:(R)" "*S-1-5-32-544:(R)" "$($cred.UserName):(R)" | Out-Null
}
$argline = '-codex "{0}" agent -hub {1} -id {2} -name "{3}" -token-file "{4}" -agent-key-file "{5}"' -f $CodexPath,$Hub,$MachineId,$MachineName,$tokFile,$keyFile

Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false -ErrorAction SilentlyContinue
$action  = New-ScheduledTaskAction -Execute $exe -Argument $argline
$trigger = New-ScheduledTaskTrigger -AtStartup
$set     = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
             -StartWhenAvailable -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) `
             -ExecutionTimeLimit ([TimeSpan]::Zero)
Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger -Settings $set `
  -User $cred.UserName -Password $pw -RunLevel Highest | Out-Null
Start-ScheduledTask -TaskName $TaskName
Start-Sleep 5

$state = (Get-ScheduledTask -TaskName $TaskName).State
Write-Host ""
Write-Host "OK Agent task '$TaskName' installed for machine '$MachineId' ($MachineName), runs as $($cred.UserName), auto-start at boot. State: $state"
Write-Host ""
Write-Host "   Phone connects to:"
Write-Host "     wss://relay.example.com/ws?token=$token"
Write-Host ""
Write-Host "   Manage: Get-ScheduledTask $TaskName ; Stop-ScheduledTask $TaskName ; Unregister-ScheduledTask $TaskName"
