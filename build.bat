@echo off
chcp 65001 >nul
rem ---------------------------------------------------------------
rem  PinkPDF 빌드 (Windows)
rem    dist\PinkPDF.exe        프로그램 (설치 없이 실행)
rem    dist\PinkPDF-Setup.exe  설치 파일
rem  필요한 것: Go 1.25 이상  https://go.dev/dl/
rem  필요한 라이브러리는 vendor 폴더에 모두 있어 인터넷 없이 빌드됩니다.
rem ---------------------------------------------------------------
setlocal
cd /d "%~dp0"

where go >nul 2>nul
if errorlevel 1 (
  echo Go가 설치되어 있지 않습니다. https://go.dev/dl/ 에서 설치한 뒤 다시 실행해 주세요.
  pause
  exit /b 1
)

set CGO_ENABLED=0
set GOFLAGS=-mod=vendor
set PAYLOAD=cmd\setup\payload
if not exist dist mkdir dist

echo [1/2] 프로그램을 만드는 중...
set GOOS=windows
set GOARCH=amd64
go build -trimpath -buildvcs=false -ldflags "-H windowsgui" -o dist\PinkPDF.exe .
if errorlevel 1 goto fail

echo [2/2] 설치 파일을 만드는 중...
go run .\cmd\pack dist\PinkPDF.exe %PAYLOAD%\PinkPDF.exe.gz
if errorlevel 1 goto fail
copy /y docs\USER_GUIDE.txt %PAYLOAD%\README.txt >nul
copy /y THIRD_PARTY_LICENSES.txt %PAYLOAD%\THIRD_PARTY_LICENSES.txt >nul
go build -trimpath -buildvcs=false -ldflags "-H windowsgui" -o dist\PinkPDF-Setup.exe .\cmd\setup
if errorlevel 1 goto fail
del /q %PAYLOAD%\PinkPDF.exe.gz %PAYLOAD%\README.txt %PAYLOAD%\THIRD_PARTY_LICENSES.txt >nul 2>nul

echo.
echo 완료:
echo   %~dp0dist\PinkPDF.exe
echo   %~dp0dist\PinkPDF-Setup.exe
pause
exit /b 0

:fail
echo.
echo 빌드에 실패했습니다. 위의 오류 메시지를 확인해 주세요.
pause
exit /b 1
