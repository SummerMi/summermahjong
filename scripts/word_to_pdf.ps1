<#
  把 docx 转成 PDF（用本机 Word，兼容性最好）。

  用法（由 build_resume_pdf.sh 调用，路径必须是 ASCII，避免 PowerShell 5.1 把中文按 ANSI 读坏）：
      powershell -File scripts/word_to_pdf.ps1 -Docx C:\tmp\resume.docx -Pdf C:\tmp\resume.pdf
#>
param(
    [Parameter(Mandatory = $true)][string]$Docx,
    [Parameter(Mandatory = $true)][string]$Pdf
)

$ErrorActionPreference = 'Stop'

if (-not (Test-Path -LiteralPath $Docx)) { throw "找不到输入文件: $Docx" }

$word = New-Object -ComObject Word.Application
$word.Visible = $false
$word.DisplayAlerts = 0
try {
    $doc = $word.Documents.Open($Docx)
    $doc.SaveAs2([string]$Pdf, 17)   # 17 = wdFormatPDF
    $doc.Close($false)
    Write-Output "OK $Pdf"
}
finally {
    $word.Quit()
    [System.Runtime.InteropServices.Marshal]::ReleaseComObject($word) | Out-Null
}
