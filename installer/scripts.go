package installer

// PowerShell scripts used by the Windows installer (see installer_windows.go).
// They follow the rules of package winps: ASCII only, no double quotes,
// values arrive in $env:PINKPDF_* variables, failures are reported with Fail.
// scripts_test.go checks them with PowerShell 7 when it is installed.

// psShortcuts creates the Start menu, desktop and Send To shortcuts.
// Only a missing Start menu shortcut counts as a failure (exit code 3).
//
// WScript.Shell keeps only characters of the system code page, so it saves
// under a temporary ASCII name that .NET then renames (shortcut folders can
// have any name). When the program path itself has non-ASCII characters (a
// Korean user name, say), Shell.Application, which keeps every character,
// sets the target once more.
const psShortcuts = `
$ws = New-Object -ComObject WScript.Shell
$sh = $null
try { $sh = New-Object -ComObject Shell.Application } catch {}
$wide = $env:PINKPDF_TARGET -match '[^\x00-\x7F]'
foreach ($n in @('START', 'DESKTOP', 'SENDTO')) {
    $p = [Environment]::GetEnvironmentVariable('PINKPDF_LNK_' + $n)
    if ([string]::IsNullOrEmpty($p)) { continue }
    $tmp = $null
    try {
        $dir = [IO.Path]::GetDirectoryName($p)
        [void][IO.Directory]::CreateDirectory($dir)
        $tmp = [IO.Path]::Combine($dir, 'pinkpdf-' + [Guid]::NewGuid().ToString('N') + '.lnk')
        $s = $ws.CreateShortcut($tmp)
        $s.TargetPath = $env:PINKPDF_TARGET
        $s.WorkingDirectory = $env:PINKPDF_DIR
        $s.IconLocation = $env:PINKPDF_TARGET + ',0'
        $s.Description = $env:PINKPDF_DESC
        $s.Save()
        Release $s
        if ([IO.File]::Exists($p)) { [IO.File]::Delete($p) }
        [IO.File]::Move($tmp, $p)
        $tmp = $null
        if ($wide -and $sh -ne $null) {
            try {
                $link = $sh.NameSpace($dir).ParseName([IO.Path]::GetFileName($p)).GetLink
                $link.Path = $env:PINKPDF_TARGET
                $link.WorkingDirectory = $env:PINKPDF_DIR
                $link.SetIconLocation($env:PINKPDF_TARGET, 0)
                $link.Save()
            } catch {}
        }
    } catch {
        Fail ($n + ': ' + $_.Exception.Message)
        if ($n -eq 'START') { $code = 3 }
        if ($tmp -ne $null) { try { [IO.File]::Delete($tmp) } catch {} }
    }
}
Release $sh; Release $ws
exit $code
`

// psSelfDelete removes the program a moment after the uninstaller exits
// (a running program cannot delete itself), then the folder $env:PINKPDF_DIR,
// when given, if it is empty. The folder is never removed recursively, so
// nothing else can be lost.
const psSelfDelete = `
Start-Sleep -Seconds 2
for ($i = 0; $i -lt 20; $i++) {
    if (-not (Test-Path -LiteralPath $env:PINKPDF_EXE)) { break }
    try { Remove-Item -LiteralPath $env:PINKPDF_EXE -Force; break } catch { Start-Sleep -Milliseconds 500 }
}
if ($env:PINKPDF_DIR) {
    try {
        if (-not (Get-ChildItem -LiteralPath $env:PINKPDF_DIR -Force)) { Remove-Item -LiteralPath $env:PINKPDF_DIR -Force }
    } catch {}
}
`
