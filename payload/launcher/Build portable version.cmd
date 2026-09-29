@echo off
rem ===========================================================================
rem  OpenCode Portable - first-time build; manual entry point.
rem
rem    Build portable version.cmd            interactive
rem    Build portable version.cmd --auto     install, then start opencode
rem
rem  Normally this file is not needed: OpenCode-Portable.exe starts the builder
rem  automatically when opencode is not installed yet.
rem
rem  Extra arguments can be passed through OPENCODE_BUILDER_ARGS, for example:
rem    set OPENCODE_BUILDER_ARGS=--version v1.18.31
rem    set OPENCODE_BUILDER_ARGS=--proxy http://127.0.0.1:7897
rem    set OPENCODE_BUILDER_ARGS=--installer "D:\opencode-desktop-win-x64.exe"
rem    set OPENCODE_PORTABLE_MIRROR=https://ghfast.top/
rem
rem  NOTE: this script is deliberately plain ASCII, uses CRLF line endings and
rem  avoids goto/labels and parentheses inside text, because those are the
rem  things that make cmd.exe abort a batch file on real Windows.
rem ===========================================================================
setlocal
set "ROOT=%~dp0"
if "%ROOT:~-1%"=="\" set "ROOT=%ROOT:~0,-1%"
cd /d "%ROOT%"
title OpenCode Portable - first build

set "BUILDER=%ROOT%\tools\OpenCode-Portable-Builder.exe"
if not exist "%BUILDER%" (
  echo.
  echo [ERROR] tools\OpenCode-Portable-Builder.exe was not found.
  echo         Please unzip the complete package onto the stick first.
  echo.
  pause
  exit /b 1
)

set "AUTO="
if /i "%~1"=="--auto" set "AUTO=1"
set "GITFLAG="
set "WITHGIT="
if not defined OPENCODE_PORTABLE_SKIP_GIT if defined AUTO set "GITFLAG=--with-git"
if not defined OPENCODE_PORTABLE_SKIP_GIT if not defined AUTO set /p "WITHGIT=Install portable Git as well? recommended, about 56 MB extra [Y/N] "
if /i "%WITHGIT%"=="y" set "GITFLAG=--with-git"

echo.
echo -------------------------------------------------------------------
echo  opencode portable - first-time build
echo  target : %ROOT%
echo  this downloads about 120 MB from GitHub, unpacks it here and turns
echo  it into a portable app. System proxy settings are used automatically.
echo -------------------------------------------------------------------
echo.
"%BUILDER%" --root "%ROOT%" %GITFLAG% %OPENCODE_BUILDER_ARGS%
set "CODE=%ERRORLEVEL%"
echo.

if not "%CODE%"=="0" (
  echo -------------------------------------------------------------------
  echo  BUILD FAILED with exit code %CODE%
  echo -------------------------------------------------------------------
  echo  Full log: %ROOT%\data\build.log
  echo.
  echo  Hints:
  echo    - turn your system proxy on and retry; it is detected automatically
  echo    - try the verified version:
  echo        set OPENCODE_BUILDER_ARGS=--version v1.18.31
  echo    - build offline from a downloaded installer:
  echo        set OPENCODE_BUILDER_ARGS=--installer "D:\opencode-desktop-win-x64.exe"
  echo    - use a download mirror, for example:
  echo        set OPENCODE_PORTABLE_MIRROR=https://ghfast.top/
  echo.
  pause
  exit /b %CODE%
)

echo -------------------------------------------------------------------
echo  BUILD FINISHED
echo -------------------------------------------------------------------
if defined AUTO (
  echo  starting opencode ...
  start "" "%ROOT%\OpenCode-Portable.exe"
  exit /b 0
)
echo  Double-click OpenCode-Portable.exe to start opencode.
echo  Your data lives in %ROOT%\data
echo.
pause
exit /b 0
