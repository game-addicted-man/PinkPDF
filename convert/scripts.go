package convert

// PowerShell scripts that drive Microsoft Office and Hancom Office through
// COM (used on Windows only; see convert_windows.go). They follow the rules
// of package winps: ASCII only, no double quotes, paths arrive as
// $env:PINKPDF_IN / $env:PINKPDF_OUT, and failures are reported with Fail.
// scripts_test.go checks them with PowerShell 7 when it is installed.

const psPaths = `
$InPath = $env:PINKPDF_IN
$OutPath = $env:PINKPDF_OUT
# Office programs started through COM are not child processes of this
# script. Owned writes the process id of the copy New-Object just started to
# $env:PINKPDF_PIDFILE, so the program can end it if the conversion gets stuck
# (on a hidden prompt, say) and has to be stopped.
function Track($name) { @(Get-Process -Name $name -ErrorAction SilentlyContinue | ForEach-Object { $_.Id }) }
function Owned($name, $before) {
    $new = @(Get-Process -Name $name -ErrorAction SilentlyContinue | Where-Object { $before -notcontains $_.Id } | ForEach-Object { $_.Id })
    if ($new.Count -ne 1) { return $false }
    if ($env:PINKPDF_PIDFILE) { try { [IO.File]::WriteAllText($env:PINKPDF_PIDFILE, [string]$new[0]) } catch {} }
    return $true
}
`

const psWord = psPaths + `
$app = $null; $doc = $null
try {
    $before = Track 'WINWORD'
    $app = New-Object -ComObject Word.Application
    [void](Owned 'WINWORD' $before)
    $app.Visible = $false
    $app.DisplayAlerts = 0
    try { $app.AutomationSecurity = 3 } catch {}
    # read-only, not added to recent files; a dummy password makes protected
    # files fail at once instead of waiting on a hidden password prompt;
    # NoEncodingDialog (15th) lets Word pick the encoding of text files itself
    $m = [Type]::Missing
    $doc = $app.Documents.Open($InPath, $false, $true, $false, 'pinkpdf-no-password', $m, $m, $m, $m, $m, $m, $m, $m, $m, $true)
    $doc.ExportAsFixedFormat($OutPath, 17)
    $doc.Saved = $true
} catch {
    Fail $_.Exception.Message
    $code = 2
} finally {
    if ($doc -ne $null) { try { $doc.Close(0) } catch {} }
    if ($app -ne $null) { try { $app.Quit(0) } catch {} }
    Release $doc; Release $app
}
exit $code
`

const psExcel = psPaths + `
$app = $null; $wb = $null
try {
    $before = Track 'EXCEL'
    $app = New-Object -ComObject Excel.Application
    [void](Owned 'EXCEL' $before)
    $app.Visible = $false
    $app.DisplayAlerts = $false
    $app.ScreenUpdating = $false
    try { $app.AskToUpdateLinks = $false } catch {}
    try { $app.AutomationSecurity = 3 } catch {}
    $m = [Type]::Missing
    # no link updates, read-only, dummy password, ignore read-only recommendation
    $wb = $app.Workbooks.Open($InPath, 0, $true, $m, 'pinkpdf-no-password', $m, $true)
    $wb.ExportAsFixedFormat(0, $OutPath)
} catch {
    Fail $_.Exception.Message
    $code = 2
} finally {
    if ($wb -ne $null) { try { $wb.Close($false) } catch {} }
    if ($app -ne $null) { try { $app.Quit() } catch {} }
    Release $wb; Release $app
}
exit $code
`

const psPowerPoint = psPaths + `
$app = $null; $pres = $null; $own = $false
try {
    # PowerPoint runs as one shared copy: if it is already open, New-Object
    # joins it and this script must leave it running
    $before = Track 'POWERPNT'
    $app = New-Object -ComObject PowerPoint.Application
    $own = Owned 'POWERPNT' $before
    # read-only, untitled = false, no window
    $pres = $app.Presentations.Open($InPath, -1, 0, 0)
    $pres.SaveAs($OutPath, 32)
} catch {
    Fail $_.Exception.Message
    $code = 2
} finally {
    if ($pres -ne $null) { try { $pres.Close() } catch {} }
    if ($app -ne $null -and $own) { try { if ($app.Presentations.Count -eq 0) { $app.Quit() } } catch {} }
    Release $pres; Release $app
}
exit $code
`

const psHwp = psPaths + `
$hwp = $null
try {
    $before = Track 'Hwp'
    $hwp = New-Object -ComObject HWPFrame.HwpObject
    [void](Owned 'Hwp' $before)
    try { [void]$hwp.RegisterModule('FilePathCheckDLL', 'FilePathCheckerModule') } catch {}
    $ok = $hwp.Open($InPath, '', 'forceopen:true;suspendpassword:true;versionwarning:false')
    if (-not $ok) { throw 'HWP_OPEN' }
    $ok = $hwp.SaveAs($OutPath, 'PDF', '')
    if (-not $ok) { throw 'HWP_SAVE' }
} catch {
    Fail $_.Exception.Message
    $code = 2
} finally {
    if ($hwp -ne $null) { try { [void]$hwp.Clear(1) } catch {}; try { $hwp.Quit() } catch {} }
    Release $hwp
}
exit $code
`

// Word 2013+ opens PDFs ("PDF Reflow") and can save them as .docx. Its
// "Word will now convert your PDF" notice is switched off first so it
// cannot block the hidden Word window.
const psWordPDF = psPaths + `
$app = $null; $doc = $null; $saved = @{}
$name = 'DisableConvertPdfWarning'
foreach ($v in @('16.0', '15.0')) {
    $key = 'HKCU:\Software\Microsoft\Office\' + $v + '\Word\Options'
    try {
        if (-not (Test-Path -LiteralPath $key)) { [void](New-Item -Path $key -Force) }
        $saved[$key] = (Get-ItemProperty -LiteralPath $key -ErrorAction SilentlyContinue).$name
        Set-ItemProperty -LiteralPath $key -Name $name -Value 1 -Type DWord
    } catch {}
}
try {
    $before = Track 'WINWORD'
    $app = New-Object -ComObject Word.Application
    [void](Owned 'WINWORD' $before)
    $app.Visible = $false
    $app.DisplayAlerts = 0
    try { $app.AutomationSecurity = 3 } catch {}
    $doc = $app.Documents.Open($InPath, $false, $true, $false)
    $doc.SaveAs2($OutPath, 16)
    $doc.Saved = $true
} catch {
    Fail $_.Exception.Message
    $code = 2
} finally {
    if ($doc -ne $null) { try { $doc.Close(0) } catch {} }
    if ($app -ne $null) { try { $app.Quit(0) } catch {} }
    Release $doc; Release $app
    # put the user's Word setting back the way it was
    foreach ($key in @($saved.Keys)) {
        try {
            if ($saved[$key] -eq $null) { Remove-ItemProperty -LiteralPath $key -Name $name }
            else { Set-ItemProperty -LiteralPath $key -Name $name -Value $saved[$key] -Type DWord }
        } catch {}
    }
}
exit $code
`
