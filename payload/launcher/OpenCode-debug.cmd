@echo off
rem ===========================================================================
rem  OpenCode Portable - diagnostics.
rem  Starts the portable launcher, then prints the launcher and build logs so
rem  the detected proxy, paths and any problem are visible.
rem ===========================================================================
setlocal
set "ROOT=%~dp0"
if "%ROOT:~-1%"=="\" set "ROOT=%ROOT:~0,-1%"
cd /d "%ROOT%"
title OpenCode Portable - debug

if not exist "%ROOT%\OpenCode-Portable.exe" (
  echo [ERROR] OpenCode-Portable.exe was not found next to this script.
  pause
  exit /b 1
)

echo Starting OpenCode Portable ...
echo.
"%ROOT%\OpenCode-Portable.exe"
timeout /t 3 /nobreak >nul 2>&1

echo.
echo ============== data\launcher.log - last 25 lines ==============
if exist "%ROOT%\data\launcher.log" powershell -NoProfile -Command "Get-Content -LiteralPath '%ROOT%\data\launcher.log' -Tail 25" 2>nul
if exist "%ROOT%\data\launcher.log" if errorlevel 1 type "%ROOT%\data\launcher.log"
if not exist "%ROOT%\data\launcher.log" echo no launcher log yet
echo ==============================================================
echo.
echo ================ data\build.log - last 25 lines ==============
if exist "%ROOT%\data\build.log" powershell -NoProfile -Command "Get-Content -LiteralPath '%ROOT%\data\build.log' -Tail 25" 2>nul
if exist "%ROOT%\data\build.log" if errorlevel 1 type "%ROOT%\data\build.log"
if not exist "%ROOT%\data\build.log" echo no build log yet - opencode was probably already installed
echo ==============================================================
echo.
echo opencode's own logs live in %ROOT%\data\userdata\logs
echo.
pause
exit /b 0
