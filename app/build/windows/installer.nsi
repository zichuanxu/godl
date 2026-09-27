; Per-user installer for the godl desktop app: no administrator rights, an
; entry in Apps & features, and Start menu shortcuts. Built by
; scripts/package-windows.sh, which defines VERSION, NUMVERSION (digits
; only), BINARY, LICENSE, ICON, and OUTFILE.
Unicode true
!include "MUI2.nsh"
!include "LogicLib.nsh"

!define PRODUCT "godl"
!define UNINST_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\godl"

Name "${PRODUCT} ${VERSION}"
OutFile "${OUTFILE}"
InstallDir "$LOCALAPPDATA\Programs\godl"
InstallDirRegKey HKCU "Software\godl" "InstallDir"
RequestExecutionLevel user
SetCompressor /SOLID lzma
VIProductVersion "${NUMVERSION}.0"
VIAddVersionKey "ProductName" "${PRODUCT}"
VIAddVersionKey "ProductVersion" "${VERSION}"
VIAddVersionKey "FileVersion" "${NUMVERSION}"
VIAddVersionKey "FileDescription" "godl installer"
VIAddVersionKey "LegalCopyright" "Copyright 2026 zichuanxu. Apache-2.0."

!define MUI_ICON "${ICON}"
!define MUI_UNICON "${ICON}"
!define MUI_FINISHPAGE_RUN "$INSTDIR\godl.exe"
!insertmacro MUI_PAGE_LICENSE "${LICENSE}"
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"

; The app renders with the WebView2 runtime, which Windows 11 includes and
; Windows 10 receives through updates.
Function CheckWebView2
  ReadRegStr $0 HKLM "SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}" "pv"
  ${If} $0 == ""
    ReadRegStr $0 HKCU "Software\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}" "pv"
  ${EndIf}
  ${If} $0 == ""
  ${OrIf} $0 == "0.0.0.0"
    IfSilent done
    MessageBox MB_YESNO|MB_ICONEXCLAMATION "godl needs the Microsoft Edge WebView2 Runtime, which is not installed.$\r$\n$\r$\nOpen its download page now?" IDNO done
    ExecShell "open" "https://developer.microsoft.com/microsoft-edge/webview2/#download"
  ${EndIf}
  done:
FunctionEnd

Section "godl"
  ; Quit a running copy so its files can be replaced.
  nsExec::Exec 'taskkill /IM godl.exe /F'
  SetOutPath "$INSTDIR"
  File "/oname=godl.exe" "${BINARY}"
  File "/oname=LICENSE.txt" "${LICENSE}"
  WriteUninstaller "$INSTDIR\uninstall.exe"
  CreateShortcut "$SMPROGRAMS\godl.lnk" "$INSTDIR\godl.exe"
  WriteRegStr HKCU "Software\godl" "InstallDir" "$INSTDIR"
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayName" "godl"
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNINST_KEY}" "Publisher" "zichuanxu"
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayIcon" "$INSTDIR\godl.exe"
  WriteRegStr HKCU "${UNINST_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINST_KEY}" "URLInfoAbout" "https://github.com/zichuanxu/godl"
  WriteRegStr HKCU "${UNINST_KEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegStr HKCU "${UNINST_KEY}" "QuietUninstallString" '"$INSTDIR\uninstall.exe" /S'
  WriteRegDWORD HKCU "${UNINST_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINST_KEY}" "NoRepair" 1
  Call CheckWebView2
SectionEnd

; Downloads, settings, and logs in %AppData%\godl are the user's and stay.
Section "Uninstall"
  nsExec::Exec 'taskkill /IM godl.exe /F'
  Delete "$SMPROGRAMS\godl.lnk"
  Delete "$INSTDIR\godl.exe"
  Delete "$INSTDIR\LICENSE.txt"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR"
  DeleteRegKey HKCU "${UNINST_KEY}"
  DeleteRegKey HKCU "Software\godl"
  ; "Start godl when I log in" (Wails autostart) registers this value.
  DeleteRegValue HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "godl"
SectionEnd
