# install-core.ps1 — payload run by CodexRemoteSetup.exe (elevated).
# Sets up the Codex Remote agent + tray on Windows: locates codex.exe, generates a
# per-machine token, writes the tray config, and registers two Scheduled Tasks that
# run AT LOGON in the user's interactive session (so codex finds the user's auth
# and the tray icon shows — no stored password).
#
# Install asks for NO subscription key: activation is a post-install step (tray ▸
# 激活订阅码…), and the agent waits for the credential file to appear. This keeps
# install outcomes independent of key/network problems.
param(
  [string]$Hub = "wss://relay.example.com/agent",
  [string]$MachineId = $env:COMPUTERNAME.ToLower(),
  [string]$MachineName = $env:COMPUTERNAME,
  [string]$InstallDir = "$env:ProgramData\codex-remote",
  [string]$CodexPath = ""
)
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

# Machine id must be URL/route-safe ASCII (the hub enforces [a-z0-9._-]). Chinese
# computer names sanitize to nothing — fall back to a STABLE id derived from the
# MachineGuid, so reinstalls keep the same id (and token file name).
$MachineId = ($MachineId.ToLower() -replace '[^a-z0-9._-]', '')
if ($MachineId.Length -lt 2 -or $MachineId -notmatch '^[a-z0-9]') {
  $guid = (Get-ItemProperty 'HKLM:\SOFTWARE\Microsoft\Cryptography' -ErrorAction Stop).MachineGuid
  $MachineId = 'win-' + $guid.Replace('-','').Substring(0, 8).ToLower()
}
Write-Output "machine id: $MachineId"

$bridge = Join-Path $InstallDir 'codexbridge.exe'
$tray   = Join-Path $InstallDir 'codexmenubar.exe'
if (-not (Test-Path $bridge)) { throw "codexbridge.exe missing in $InstallDir" }

# 1) locate the NATIVE codex.exe (the Rust binary npm ships, not the .cmd wrapper).
# The installer runs ELEVATED, so $env:APPDATA/$env:LOCALAPPDATA point at the
# ADMIN account's profile — but codex (npm -g / desktop app) is a PER-USER
# install that usually lives in the LOGGED-IN user's profile (a different one
# whenever UAC elevated to another account). That mismatch was the #1 cause of
# "codex not found" on machines that clearly have it — so search EVERY profile.
function Select-CodexHit($hits) {
  # Prefer the npm vendor binary, then a desktop-app resources CLI, then bin/;
  # a bare Codex.exe hit could be the Electron GUI itself, so it comes last.
  foreach ($pat in @('vendor', 'resources', 'bin')) {
    $f = $hits | Where-Object { $_.FullName -match $pat } | Select-Object -First 1
    if ($f) { return $f.FullName }
  }
  $f = $hits | Select-Object -First 1
  if ($f) { return $f.FullName }
  return $null
}
if (-not $CodexPath) {
  # a) a real codex.exe already on PATH (scoop shim, manual copy, …)
  $CodexPath = (Get-Command codex.exe -ErrorAction SilentlyContinue).Source
}
if (-not $CodexPath) {
  # b) the npm WRAPPER on PATH (codex.cmd/codex.ps1) — Get-Command codex.exe
  # misses it. The native exe lives in the same npm prefix under node_modules.
  $w = (Get-Command codex -ErrorAction SilentlyContinue).Source
  if ($w) {
    $nmRoot = Join-Path (Split-Path $w) 'node_modules\@openai\codex'
    if (Test-Path $nmRoot) {
      $hits = Get-ChildItem -Path $nmRoot -Recurse -Filter codex.exe -ErrorAction SilentlyContinue
      $CodexPath = Select-CodexHit $hits
    }
  }
}
if (-not $CodexPath) {
  # c) every user profile + machine-wide roots.
  $roots = @()
  foreach ($u in (Get-ChildItem 'C:\Users' -Directory -ErrorAction SilentlyContinue)) {
    $roots += (Join-Path $u.FullName 'AppData\Roaming\npm\node_modules\@openai\codex')
    $roots += (Join-Path $u.FullName 'AppData\Local\Programs')
  }
  $roots += @("$env:ProgramFiles", "${env:ProgramFiles(x86)}")
  foreach ($r in $roots) {
    if ($r -and (Test-Path $r)) {
      $hits = Get-ChildItem -Path $r -Recurse -Filter codex.exe -ErrorAction SilentlyContinue
      $CodexPath = Select-CodexHit $hits
      if ($CodexPath) { break }
    }
  }
}
if (-not $CodexPath) { throw "codex.exe not found. Install Codex first (npm i -g @openai/codex), or pass -CodexPath. Tip: 'npm root -g' shows where npm puts it." }
Write-Output "codex: $CodexPath"

# 2) per-machine token (reuse if present)
$tokFile = Join-Path $InstallDir "machine-$MachineId.token"
if (Test-Path $tokFile) {
  $token = (Get-Content $tokFile -Raw).Trim()
} else {
  $b = New-Object 'System.Byte[]' 32
  [System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($b)
  $token = -join ($b | ForEach-Object { $_.ToString('x2') })
  Set-Content -Path $tokFile -Value $token -NoNewline
}

# 3) tray config. CRED_FILE/BRIDGE feed the tray's 激活订阅码… flow; the file
# doesn't exist yet — the agent polls for it (no AGENT_KEY in license mode; the
# tray then shows 未激活 and falls back to the local task for its indicator).
# UTF-8 WITHOUT BOM: -Encoding ascii mangled Chinese computer names into "???",
# and PS5's UTF8 writes a BOM that would corrupt the first key on the Go side.
$credFile = Join-Path $InstallDir 'machine-cred'
$envText = (@("MACHINE_ID=$MachineId", "MACHINE_NAME=$MachineName", "HUB=$Hub", "TOKEN=$token",
  "CRED_FILE=$credFile", "BRIDGE=$bridge") -join "`r`n") + "`r`n"
[IO.File]::WriteAllText((Join-Path $InstallDir 'menubar.env'), $envText,
  (New-Object System.Text.UTF8Encoding($false)))

# 4) Scheduled Tasks — both at logon, interactive session, highest (no password)
# Use the fully-qualified current identity (COMPUTERNAME\user on workgroup machines,
# DOMAIN\user on domain-joined). $env:USERDOMAIN is "WORKGROUP" on a workgroup box,
# which is NOT a valid task principal ("No mapping between account names and SIDs").
$user = [System.Security.Principal.WindowsIdentity]::GetCurrent().Name
function New-CRTask([string]$name, [string]$exe, [string]$argline) {
  Unregister-ScheduledTask -TaskName $name -Confirm:$false -ErrorAction SilentlyContinue
  if ($argline) { $action = New-ScheduledTaskAction -Execute $exe -Argument $argline }
  else          { $action = New-ScheduledTaskAction -Execute $exe }
  $trigger   = New-ScheduledTaskTrigger -AtLogOn -User $user
  $principal = New-ScheduledTaskPrincipal -UserId $user -LogonType Interactive -RunLevel Highest
  $set = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
           -StartWhenAvailable -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) `
           -ExecutionTimeLimit ([TimeSpan]::Zero)
  Register-ScheduledTask -TaskName $name -Action $action -Trigger $trigger -Principal $principal -Settings $set | Out-Null
  Start-ScheduledTask -TaskName $name
}
# Keep secrets OUT of the Scheduled-Task command line: Tasks XML under
# C:\Windows\System32\Tasks is readable by the Users group, so argv secrets leak to
# every local account. The per-machine token and credential live as files inside the
# ACL-locked install dir (below); only the file PATHS go on argv — codexbridge reads
# them via -token-file / -cred-file. This keeps the GUI-subsystem bridge running
# directly (no console-window flash from a wrapper).
$agentArgs = '-codex "{0}" agent -hub {1} -id {2} -name "{3}" -token-file "{4}" -cred-file "{5}"' -f `
  $CodexPath, $Hub, $MachineId, $MachineName, $tokFile, $credFile
New-CRTask 'CodexRemoteAgent' $bridge $agentArgs
New-CRTask 'CodexRemoteTray'  $tray  $null

# 5) leave the connect string for the installer / user (derived from -Hub)
$wsBase = $Hub -replace '/agent$', ''
$url = "$wsBase/ws?token=$token"
Set-Content -Path (Join-Path $InstallDir 'connect-url.txt') -Value $url -Encoding ascii
Write-Output "CONNECT_URL=$url"

# 6) lock down the install dir. By default C:\ProgramData grants the Users group
# read access, which would expose machine-$id.token, machine-cred, menubar.env and
# connect-url.txt (all crown-jewel secrets) to every local account. Strip inherited
# ACEs and grant only SYSTEM, the Administrators group (well-known SID, locale-safe)
# and the agent's own user. The task runs as $user at Highest, so it keeps access.
icacls $InstallDir /inheritance:r /grant:r "*S-1-5-18:(OI)(CI)F" "*S-1-5-32-544:(OI)(CI)F" "${user}:(OI)(CI)F" | Out-Null

Write-Output "OK machine=$MachineId"
