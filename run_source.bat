@echo off
set MASKTOOL_WORK_DIR=%~dp0kadai_masking
set MASKTOOL_AI_DIR=%~dp0kadai_ai
set MASKTOOL_SERVER_BASE=%~dp0kadai_server\riyousha
set MASKTOOL_SERVER_ENDED=%~dp0kadai_server\riyousha_end
set MASKTOOL_SAMPLES_DIR=%~dp0samples
cd /d %~dp0src\w27
go run -trimpath -ldflags "-X main.mode=panel" .
