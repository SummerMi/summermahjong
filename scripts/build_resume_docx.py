"""把「简历 Markdown」渲染成可直接投递的 .docx（一页/两页，ATS 友好）。

为什么用脚本而不是手写 docx：
- Markdown 是唯一真相，改简历只改 md，再跑一次脚本即可；
- 避免手动排版导致每次改字都要重新对齐字体/项目符号。

用法（本机已装 python-docx，用系统 Python 即可）：

    python scripts/build_resume_docx.py \
        --md docs/简历-成品-20261102.md \
        --out docs/简历-成品-20261102.docx

渲染规则：
- `# 标题`  → 姓名（居中、加粗、大字号）
- 紧跟在姓名后的第一行加粗文本 → 求职意向副标题（居中）
- `## xxx` → 小节标题（加粗、带下边框色的简单分隔）
- `### xxx` → 项目/经历标题（加粗）
- `- xxx`  → 项目符号；行内 `**粗体**` 会保留为粗体
- 块引用 `>`、分隔线 `---`、以及「使用说明 / 按公司改三个词 / 诚实边界」这几节**不进简历**
  （它们是给你自己看的，投出去会减分）
"""

from __future__ import annotations

import argparse
import re
from pathlib import Path

from docx import Document
from docx.enum.text import WD_ALIGN_PARAGRAPH
from docx.oxml.ns import qn
from docx.shared import Cm, Pt

# 这些二级标题之后的内容不进简历（给作者自己看的）
EXCLUDE_SECTIONS = ("按公司改三个词", "诚实边界")
# 简历正文从这里开始
BODY_START = "教育背景"

INLINE_BOLD = re.compile(r"\*\*(.+?)\*\*")


def _set_cn_font(run, name: str = "微软雅黑", size: float = 10.0, bold: bool = False) -> None:
    """给一个 run 设置中英混排字体（docx 需要同时设置 ascii 与 eastAsia）。"""
    run.font.size = Pt(size)
    run.font.bold = bold
    run.font.name = name
    run._element.rPr.rFonts.set(qn("w:eastAsia"), name)


def _add_rich_text(paragraph, text: str, size: float = 10.0, base_bold: bool = False) -> None:
    """把一行里的 `**粗体**` 拆成多个 run；反引号与单星号（斜体标记）直接去掉。"""
    text = text.replace("`", "").replace("*", "")
    if not text:
        return
    cursor = 0
    for match in INLINE_BOLD.finditer(text):
        if match.start() > cursor:
            _set_cn_font(paragraph.add_run(text[cursor : match.start()]), size=size, bold=base_bold)
        _set_cn_font(paragraph.add_run(match.group(1)), size=size, bold=True)
        cursor = match.end()
    if cursor < len(text):
        _set_cn_font(paragraph.add_run(text[cursor:]), size=size, bold=base_bold)


def _parse(md_text: str) -> tuple[str, str, list[tuple[str, str]]]:
    """把 Markdown 拆成（姓名, 求职意向, [(块类型, 文本), ...]）。

    块类型：h2 / h3 / bullet / para
    """
    lines = md_text.splitlines()
    name = ""
    subtitle = ""
    blocks: list[tuple[str, str]] = []
    in_body = False
    skip = False

    for raw in lines:
        line = raw.rstrip()
        stripped = line.strip()

        if stripped.startswith("# ") and not name:
            name = stripped[2:].strip()
            continue
        if not name:
            continue
        # 姓名后的第一条加粗非空行 = 求职意向
        if not subtitle and stripped.startswith("**") and stripped.endswith("**"):
            subtitle = stripped.strip("*").strip()
            continue
        if stripped.startswith("## "):
            title = stripped[3:].strip()
            in_body = title.startswith(BODY_START) or in_body
            skip = any(title.startswith(x) for x in EXCLUDE_SECTIONS)
            if in_body and not skip:
                blocks.append(("h2", title))
            continue
        if not in_body:
            # 正文之前、姓名/求职意向之后的普通行 = 联系方式行（之前漏渲染了）
            if stripped and not stripped.startswith(("#", ">", "---")):
                blocks.append(("contact", stripped))
            continue
        if skip:
            continue
        if stripped.startswith("### "):
            blocks.append(("h3", stripped[4:].strip()))
        elif stripped.startswith("- "):
            blocks.append(("bullet", stripped[2:].strip()))
        elif not stripped or stripped == "---" or stripped.startswith(">"):
            continue
        else:
            blocks.append(("para", stripped))
    return name, subtitle, blocks


def build(md_path: Path, out_path: Path) -> None:
    """读 Markdown 并写出 .docx。"""
    name, subtitle, blocks = _parse(md_path.read_text(encoding="utf-8"))
    if not name:
        raise SystemExit("没找到 `# 姓名` 这一行，请检查 Markdown")

    doc = Document()
    section = doc.sections[0]
    section.top_margin = Cm(0.9)
    section.bottom_margin = Cm(0.9)
    section.left_margin = Cm(1.3)
    section.right_margin = Cm(1.3)

    # 姓名
    title = doc.add_paragraph()
    title.alignment = WD_ALIGN_PARAGRAPH.CENTER
    title.paragraph_format.space_after = Pt(1)
    _set_cn_font(title.add_run(name), size=17, bold=True)

    # 求职意向
    if subtitle:
        sub = doc.add_paragraph()
        sub.alignment = WD_ALIGN_PARAGRAPH.CENTER
        sub.paragraph_format.space_after = Pt(4)
        _set_cn_font(sub.add_run(subtitle), size=9)

    for kind, text in blocks:
        if kind == "contact":
            p = doc.add_paragraph()
            p.alignment = WD_ALIGN_PARAGRAPH.CENTER
            p.paragraph_format.space_after = Pt(6)
            _set_cn_font(p.add_run(text.replace("**", "")), size=9)
        elif kind == "h2":
            p = doc.add_paragraph()
            p.paragraph_format.space_before = Pt(5)
            p.paragraph_format.space_after = Pt(2)
            _set_cn_font(p.add_run(text), size=10.8, bold=True)
            # 小节标题下加一条细横线（用下边框实现）
            pPr = p._p.get_or_add_pPr()
            borders = pPr.makeelement(qn("w:pBdr"), {})
            bottom = borders.makeelement(
                qn("w:bottom"), {qn("w:val"): "single", qn("w:sz"): "6", qn("w:color"): "999999"}
            )
            borders.append(bottom)
            pPr.append(borders)
        elif kind == "h3":
            p = doc.add_paragraph()
            p.paragraph_format.space_before = Pt(4)
            p.paragraph_format.space_after = Pt(1)
            _add_rich_text(p, text, size=9.6, base_bold=True)
        elif kind == "bullet":
            p = doc.add_paragraph(style="List Bullet")
            p.paragraph_format.space_after = Pt(1)
            p.paragraph_format.line_spacing = 1.0
            _add_rich_text(p, text, size=9.0)
        else:
            p = doc.add_paragraph()
            p.paragraph_format.space_after = Pt(1)
            _add_rich_text(p, text, size=9.0)

    out_path.parent.mkdir(parents=True, exist_ok=True)
    doc.save(str(out_path))
    print(f"已生成：{out_path}（{len(blocks)} 个内容块）")


def main() -> None:
    """命令行入口。"""
    parser = argparse.ArgumentParser(description="Markdown 简历 → docx")
    parser.add_argument("--md", default="docs/简历-成品-20261102.md")
    parser.add_argument("--out", default="docs/简历-成品-20261102.docx")
    args = parser.parse_args()
    build(Path(args.md), Path(args.out))


if __name__ == "__main__":
    main()
