@echo off
setlocal
title Relay Agent - close this window to disconnect

rem ===================== EDIT THESE 3 LINES =====================
set "RELAY=https://relay.example.com"
set "HOSTID=win-01"
set "TOKEN=PUT_YOUR_TOKEN_HERE"
rem ==============================================================

set "CA=%~dp0ca.pem"
set "EXE=%~dp0relay-agent-windows-amd64.exe"

if not exist "%EXE%" (
  echo [ERROR] %EXE% not found. Extract the whole bundle first.
  pause
  exit /b 1
)
if "%TOKEN%"=="PUT_YOUR_TOKEN_HERE" (
  echo [ERROR] Please edit TOKEN in this file first.
  pause
  exit /b 1
)

echo ============================================================
echo   Relay Agent - temporary session
echo.
echo   host  : %HOSTID%
echo   relay : %RELAY%
echo.
echo   While this window is OPEN, the model can reach this machine.
echo   Close the window (or press Ctrl+C) to disconnect instantly.
echo ============================================================
echo.

"%EXE%" -relay "%RELAY%" -host "%HOSTID%" -token "%TOKEN%" -ca "%CA%"

echo.
echo [agent exited] press any key to close this window
pause >nul
