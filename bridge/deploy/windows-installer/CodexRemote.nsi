; CodexRemote.nsi — Windows installer for the Codex Remote agent + tray.
; Build on macOS/Linux/Windows:
;   makensis -DASSETS=/path/to/staged -DOUTFILE=/path/CodexRemoteSetup.exe CodexRemote.nsi
; where the staged dir contains: codexbridge.exe codexmenubar.exe icon_on.ico
; icon_off.ico install-core.ps1
;
; License mode: install asks for NOTHING — activation happens afterwards from
; the tray (激活订阅码…), which exchanges the key for a per-machine credential.
; The agent waits politely for the credential file, so install order is free.
;
; Silent install (e.g. mass deploy):
;   CodexRemoteSetup.exe /S

Unicode true
!include "MUI2.nsh"
!include "nsDialogs.nsh"
!include "LogicLib.nsh"
!include "FileFunc.nsh"

!ifndef ASSETS
  !define ASSETS "."
!endif
!ifndef OUTFILE
  !define OUTFILE "CodexRemoteSetup.exe"
!endif

Name "Codex Remote"
OutFile "${OUTFILE}"
RequestExecutionLevel admin
; With SetShellVarContext all (see .onInit), $APPDATA = C:\ProgramData
InstallDir "$APPDATA\codex-remote"
ShowInstDetails show
BrandingText "Codex Remote"

; ---- pages ----
!define MUI_ABORTWARNING
!define MUI_ICON "${ASSETS}/icon_on.ico"
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_INSTFILES
!define MUI_FINISHPAGE_TITLE "安装完成"
!define MUI_FINISHPAGE_TEXT "Codex Remote 已安装并启动。$\r$\n$\r$\n下一步:点任务栏托盘的 >_ 图标 → 「激活订阅码…」,填入你的 crk_ 订阅码;$\r$\n激活后点「显示二维码…」,用手机 App 扫码即可连接这台电脑。$\r$\n（图标可能折叠在托盘的 ^ 里。）"
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "SimpChinese"
!insertmacro MUI_LANGUAGE "English"

Function .onInit
  SetShellVarContext all                      ; $APPDATA -> C:\ProgramData (machine-wide)
  StrCpy $INSTDIR "$APPDATA\codex-remote"     ; force it (silent mode skips the dir page)
FunctionEnd

; ---- install ----
Section "Install"
  SetOutPath "$INSTDIR"
  File "${ASSETS}/codexbridge.exe"
  File "${ASSETS}/codexmenubar.exe"
  File "${ASSETS}/icon_on.ico"
  File "${ASSETS}/icon_off.ico"
  File "${ASSETS}/install-core.ps1"

  DetailPrint "配置服务（查找 codex、生成 token、注册计划任务）..."
  nsExec::ExecToLog 'powershell -NoProfile -ExecutionPolicy Bypass -File "$INSTDIR\install-core.ps1" -InstallDir "$INSTDIR"'
  Pop $0
  ${If} $0 != 0
    MessageBox MB_OK|MB_ICONSTOP "配置失败（错误码 $0）。最常见原因：这台电脑还没装 Codex（npm i -g @openai/codex 或安装 Codex 桌面版后重装本程序）。上方日志有具体提示。" /SD IDOK
  ${EndIf}

  ; 桌面快捷方式(公共桌面,SetShellVarContext all)——指向托盘程序;托盘有
  ; 单实例守卫,已在运行时双击它只会提示「已在运行」而不会出现第二个图标
  CreateShortCut "$DESKTOP\Caret.lnk" "$INSTDIR\codexmenubar.exe" "" "$INSTDIR\icon_on.ico" 0

  WriteUninstaller "$INSTDIR\Uninstall.exe"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\CodexRemote" "DisplayName" "Codex Remote Agent"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\CodexRemote" "DisplayIcon" "$INSTDIR\icon_on.ico"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\CodexRemote" "UninstallString" "$INSTDIR\Uninstall.exe"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\CodexRemote" "Publisher" "yunyuchen"
SectionEnd

; ---- uninstall ----
Function un.onInit
  SetShellVarContext all   ; $DESKTOP -> 公共桌面(与安装时一致)
FunctionEnd

Section "Uninstall"
  Delete "$DESKTOP\Caret.lnk"
  nsExec::ExecToLog 'schtasks /End /TN CodexRemoteTray'
  nsExec::ExecToLog 'schtasks /Delete /TN CodexRemoteTray /F'
  nsExec::ExecToLog 'schtasks /End /TN CodexRemoteAgent'
  nsExec::ExecToLog 'schtasks /Delete /TN CodexRemoteAgent /F'
  nsExec::ExecToLog 'taskkill /IM codexmenubar.exe /F'
  nsExec::ExecToLog 'taskkill /IM codexbridge.exe /F'
  Delete "$INSTDIR\codexbridge.exe"
  Delete "$INSTDIR\codexmenubar.exe"
  Delete "$INSTDIR\icon_on.ico"
  Delete "$INSTDIR\icon_off.ico"
  Delete "$INSTDIR\install-core.ps1"
  Delete "$INSTDIR\menubar.env"
  Delete "$INSTDIR\connect-url.txt"
  Delete "$INSTDIR\machine-cred"   ; per-machine credential — reissued on re-activation
  Delete "$INSTDIR\agent-key"      ; legacy shared-key file from pre-license installs
  Delete "$INSTDIR\Uninstall.exe"
  ; keep machine-*.token so a reinstall reuses the same phone connect string
  RMDir "$INSTDIR"
  DeleteRegKey HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\CodexRemote"
SectionEnd
