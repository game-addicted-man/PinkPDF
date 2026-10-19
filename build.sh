#!/bin/sh
# Builds the Windows programs from Linux or macOS (no C compiler needed):
#   dist/PinkPDF.exe        the program (runs without installing)
#   dist/PinkPDF-Setup.exe  the installer
# Requires Go 1.25+. All libraries are in ./vendor, so no network is needed.
set -e
cd "$(dirname "$0")"
export CGO_ENABLED=0 GOFLAGS=-mod=vendor
PAYLOAD=cmd/setup/payload

mkdir -p dist
GOOS=windows GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags "-H windowsgui" -o dist/PinkPDF.exe .

# pack the program into the installer
go run ./cmd/pack dist/PinkPDF.exe "$PAYLOAD/PinkPDF.exe.gz"
cp docs/USER_GUIDE.txt "$PAYLOAD/README.txt"
cp THIRD_PARTY_LICENSES.txt "$PAYLOAD/THIRD_PARTY_LICENSES.txt"
GOOS=windows GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags "-H windowsgui" -o dist/PinkPDF-Setup.exe ./cmd/setup
rm -f "$PAYLOAD/PinkPDF.exe.gz" "$PAYLOAD/README.txt" "$PAYLOAD/THIRD_PARTY_LICENSES.txt"

echo "built: dist/PinkPDF.exe dist/PinkPDF-Setup.exe"
