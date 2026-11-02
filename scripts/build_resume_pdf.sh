#!/usr/bin/env bash
# 一条命令重建简历：Markdown → docx → PDF（Word 导出）→ 预览图
#
# 用法（在仓库根目录或任意位置执行）：
#   bash scripts/build_resume_pdf.sh
#
# 前置：
#   - Python 环境已装 python-docx（pip install -e ".[dev]"）
#   - 本机装了 Word（用 Word 导 PDF，兼容性比 LibreOffice 好）
#   - pdftoppm（poppler）用来生成预览图；没有就跳过预览
#
# 改简历只需要改 docs/简历-成品-20261102.md，然后跑这一条命令。

set -euo pipefail
cd "$(dirname "$0")/.."

MD="docs/简历-成品-20261102.md"
DOCX="docs/简历-成品-20261102.docx"
PDF="docs/简历-成品-20261102.pdf"
PNG="docs/简历预览-20261102.png"

echo "[1/4] Markdown → docx"
python scripts/build_resume_docx.py --md "$MD" --out "$DOCX"

# Word 只吃得到 ASCII 路径（PowerShell 5.1 会把中文路径按 ANSI 读坏）
TMPDIR_ASCII="/tmp/resume_build_$$"
mkdir -p "$TMPDIR_ASCII"
cp "$DOCX" "$TMPDIR_ASCII/resume.docx"
TMP_WIN=$(cygpath -w "$TMPDIR_ASCII" 2>/dev/null || echo "$TMPDIR_ASCII")

echo "[2/4] docx → PDF（Word）"
powershell.exe -NoProfile -ExecutionPolicy Bypass \
  -File "scripts/word_to_pdf.ps1" \
  -Docx "${TMP_WIN}\\resume.docx" \
  -Pdf "${TMP_WIN}\\resume.pdf"
cp "$TMPDIR_ASCII/resume.pdf" "$PDF"

echo "[3/4] 生成预览图"
if command -v pdftoppm >/dev/null 2>&1; then
  pdftoppm -png -r 100 -f 1 -l 1 "$PDF" "${TMPDIR_ASCII}/preview"
  cp "${TMPDIR_ASCII}"/*.png "$PNG"
else
  echo "    跳过：没找到 pdftoppm"
fi

echo "[4/4] 自检"
python - "$PDF" "$MD" <<'PY'
import sys
from pathlib import Path

pdf_path, md_path = Path(sys.argv[1]), Path(sys.argv[2])
try:
    import fitz
    doc = fitz.open(str(pdf_path))
    text = doc[0].get_text()
    has_cn = any("\u4e00" <= c <= "\u9fff" for c in text)
    print(f"    PDF: {doc.page_count} 页 / {len(text)} 字符 / 中文可提取: {has_cn}")
    if doc.page_count > 1:
        print("    ⚠️ 超过 1 页，考虑删减内容或调小字号")
except ImportError:
    print("    未装 PyMuPDF，跳过 PDF 自检")

md = md_path.read_text(encoding="utf-8")
missing = [x for x in ("<待填>", "<按实际填写") if x in md]
if missing:
    print(f"    ⚠️ Markdown 里还有未填的占位符: {missing}")
else:
    print("    ✅ 没有待填占位符")
PY

rm -rf "$TMPDIR_ASCII"
echo "完成：$PDF"
