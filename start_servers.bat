@echo off
start "MakeRoom" cmd /k "cd /d %~dp0 && go run ./MakeRoom"
start "Battle" cmd /k "cd /d %~dp0 && go run ./Battle"
