@echo off
rem 构建 cops.exe（单文件，无运行时依赖）
go build -trimpath -ldflags "-s -w" -o cops.exe .
if %errorlevel% neq 0 (
  echo 构建失败
  exit /b 1
)
echo 构建完成: cops.exe
