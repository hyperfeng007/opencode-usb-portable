@echo off
rem ===========================================================================
rem  OpenCode Portable - update opencode to the newest release.
rem  Your data folder data\ is kept; only the application folder is replaced.
rem ===========================================================================
setlocal
set "ROOT=%~dp0"
if "%ROOT:~-1%"=="\" set "ROOT=%ROOT:~0,-1%"
cd /d "%ROOT%"
title OpenCode Portable - update

set "BUILDER=%ROOT%\tools\OpenCode-Portable-Builder.exe"
if not exist "%BUILDER%" (
  echo [ERROR] tools\OpenCode-Portable-Builder.exe was not found.
  pause
  exit /b 1
)

echo.
echo Updating opencode. Your data in data\ is not touched.
echo.
"%BUILDER%" --root "%ROOT%" --update --version latest %OPENCODE_BUILDER_ARGS%
if errorlevel 1 (
  echo.
  echo Update failed. See %ROOT%\data\build.log
  pause
  exit /b 1
)
echo.
echo Update finished.
pause
exit /b 0
