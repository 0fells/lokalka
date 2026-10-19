@echo off
set "HOST=%~1"
if "%HOST%"=="" set /p "HOST=SSH login for the server (e.g. root@203.0.113.10): "
if "%HOST%"=="" exit /b 1
start "Lokalka SSH" ssh -N -L 127.0.0.1:8756:127.0.0.1:8756 %HOST%
timeout /t 3 /nobreak >nul
start http://127.0.0.1:8756
