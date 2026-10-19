# payload

빌드 스크립트(`build.bat`, `build.sh`)가 설치 파일을 만들기 직전에 이 폴더로
`PinkPDF.exe.gz`, `README.txt`, `THIRD_PARTY_LICENSES.txt`를 복사하고,
`cmd/setup`이 이 파일들을 `PinkPDF-Setup.exe` 안에 넣습니다.

이 안내 파일 말고는 Git에 올리지 않습니다 (`.gitignore` 참고).
