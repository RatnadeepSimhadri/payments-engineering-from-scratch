#!/usr/bin/env python3
"""Generate the static learner-facing website into site/ from the repository content.

Usage:
    python3 scripts/build_site.py            # build site/ (reads catalog.json, falls back to computing it)
    python3 scripts/build_site.py --check    # build, then validate every generated page
    python3 scripts/build_site.py --out DIR  # build somewhere else

Standard library only. The Markdown renderer below implements the subset the lesson
contract (AGENTS.md / LESSON_TEMPLATE.md) uses: ATX headings, paragraphs, bullet and
numbered lists (nested, task lists), GitHub tables, fenced code blocks with language tags,
blockquotes, horizontal rules, bold/italic/strikethrough/inline code, links and images.
Browser-side rendering of ```mermaid blocks and syntax highlighting come from pinned CDN
scripts; everything else lives in site-src/ and is copied into site/assets/.

The site uses relative links only, so it works at
https://<user>.github.io/payments-engineering-from-scratch/ and when opened from disk.

--check fails when a generated HTML file does not parse, an internal link points at a
file that was not generated, or raw Markdown markers (`## `, ```) survive in page text.
"""

from __future__ import annotations

import argparse
import html
import json
import posixpath
import re
import shutil
import sys
import time
from html.parser import HTMLParser
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from _lib import ROOT, load_json, load_languages  # noqa: E402

SITE_SRC = ROOT / "site-src"
GITHUB_REPO = "https://github.com/RatnadeepSimhadri/payments-engineering-from-scratch"
GITHUB_BLOB = GITHUB_REPO + "/blob/main/"
GITHUB_TREE = GITHUB_REPO + "/tree/main/"
HLJS_VERSION = "11.9.0"
MERMAID_VERSION = "10.9.8"
HLJS_BASE = f"https://cdnjs.cloudflare.com/ajax/libs/highlight.js/{HLJS_VERSION}/"
MERMAID_URL = f"https://cdnjs.cloudflare.com/ajax/libs/mermaid/{MERMAID_VERSION}/mermaid.min.js"
STATUS_GLYPH = {"Complete": "✅", "In Progress": "🚧", "Planned": "⬚"}
STATUS_CLASS = {"Complete": "complete", "In Progress": "progress", "Planned": "planned"}
# Fence languages that highlight.js's common bundle does not know are shown as plain text.
HLJS_LANG = {"text": "plaintext", "console": "plaintext", "": "plaintext", "sh": "bash", "shell": "bash"}

WARNINGS: list[str] = []
UNHANDLED: dict[str, list[str]] = {}


def warn(message: str) -> None:
    WARNINGS.append(message)


def unhandled(source: str, message: str) -> None:
    UNHANDLED.setdefault(source, []).append(message)


# --------------------------------------------------------------------------------------
# Markdown renderer
# --------------------------------------------------------------------------------------

FENCE_RE = re.compile(r"^(\s{0,3})(`{3,}|~{3,})\s*([\w+-]*)\s*$")
HEADING_RE = re.compile(r"^(#{1,6})\s+(.*?)\s*#*\s*$")
HR_RE = re.compile(r"^\s{0,3}([-*_])(\s*\1){2,}\s*$")
LIST_RE = re.compile(r"^(\s*)([-*+]|\d{1,9}[.)])\s+(.*)$")
EMPTY_LIST_RE = re.compile(r"^(\s*)([-*+]|\d{1,9}[.)])\s*$")
TABLE_SEP_RE = re.compile(r"^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$")
TASK_RE = re.compile(r"^\[([ xX])\]\s+(.*)$", re.DOTALL)
META_LINE_RE = re.compile(r"^\*\*([^*]+?):\*\*\s*(.*)$")
CODESPAN_RE = re.compile(r"(`+)(.+?)\1(?!`)")
COMMENT_RE = re.compile(r"<!--.*?-->", re.DOTALL)
BR_RE = re.compile(r"<br\s*/?>", re.IGNORECASE)
AUTOLINK_RE = re.compile(r"<(https?://[^\s<>]+)>")
IMAGE_RE = re.compile(r"!\[([^\]]*)\]\(([^)\s]+)(?:\s+\"([^\"]*)\")?\)")
LINK_RE = re.compile(r"\[((?:[^\[\]]|\[[^\]]*\])*)\]\(([^)\s]*)(?:\s+\"([^\"]*)\")?\)")
STRONG_EM_RE = re.compile(r"\*\*\*(?!\s)(.+?)(?<!\s)\*\*\*")
STRONG_RE = re.compile(r"\*\*(?!\s)(.+?)(?<!\s)\*\*")
STRONG_U_RE = re.compile(r"(?<!\w)__(?!\s)(.+?)(?<!\s)__(?!\w)")
EM_RE = re.compile(r"(?<![\w*\\])\*(?![\s*])(.+?)(?<![\s*])\*(?![\w*])")
EM_U_RE = re.compile(r"(?<![\w\\])_(?![\s_])([^_]+?)(?<![\s_])_(?![\w])")
STRIKE_RE = re.compile(r"~~(?!\s)(.+?)(?<!\s)~~")
AMP_RE = re.compile(r"&(?![a-zA-Z][a-zA-Z0-9]*;|#\d+;|#x[0-9a-fA-F]+;)")
TAG_RE = re.compile(r"<[^>]+>")
LESSON_PATH_RE = re.compile(r"^phases/(\d{2}-[a-z0-9-]+)/(\d{2}-[a-z0-9-]+)/?$")
PLACEHOLDER = "\x00"


def escape(text: str) -> str:
    return AMP_RE.sub("&amp;", text).replace("<", "&lt;").replace(">", "&gt;")


def attr(text: str) -> str:
    return html.escape(text, quote=True)


def slugify(text: str) -> str:
    plain = TAG_RE.sub("", text)
    plain = html.unescape(plain).lower()
    plain = re.sub(r"[^\w\s-]", "", plain)
    plain = re.sub(r"[\s_]+", "-", plain).strip("-")
    return plain or "section"


class RenderContext:
    """Where the Markdown came from and which page it is rendered into."""

    def __init__(self, site: "Site", source: str, page_url: str, *, id_prefix: str = "",
                 heading_shift: int = 0, link_code_paths: bool = False):
        self.site = site
        self.source = source            # repo-relative path of the Markdown file
        self.page_url = page_url        # site-relative path of the page being generated
        self.id_prefix = id_prefix
        self.heading_shift = heading_shift
        self.link_code_paths = link_code_paths
        self.headings: list[tuple[int, str, str]] = []   # (level, text, id)
        self.used_ids: dict[str, int] = {}
        self.placeholders: list[str] = []

    def heading_id(self, text_html: str) -> str:
        base = self.id_prefix + slugify(text_html)
        count = self.used_ids.get(base, 0)
        self.used_ids[base] = count + 1
        return base if count == 0 else f"{base}-{count + 1}"

    def stash(self, rendered: str) -> str:
        self.placeholders.append(rendered)
        return f"{PLACEHOLDER}{len(self.placeholders) - 1}{PLACEHOLDER}"

    def restore(self, text: str) -> str:
        def sub(match: re.Match) -> str:
            return self.placeholders[int(match.group(1))]
        return re.sub(f"{PLACEHOLDER}(\\d+){PLACEHOLDER}", sub, text)

    def href(self, url: str) -> tuple[str, bool]:
        """Rewrite a Markdown link target to a site URL. Returns (href, is_external)."""
        return self.site.rewrite_link(url, self.source, self.page_url)


def parse_frontmatter(text: str) -> tuple[dict, str]:
    lines = text.split("\n")
    if not lines or lines[0].strip() != "---":
        return {}, text
    meta: dict[str, str] = {}
    for index in range(1, len(lines)):
        if lines[index].strip() == "---":
            return meta, "\n".join(lines[index + 1:])
        if ":" in lines[index]:
            key, _, value = lines[index].partition(":")
            meta[key.strip()] = value.strip().strip("\"'")
    return {}, text


def render_inline(text: str, ctx: RenderContext) -> str:
    ctx.placeholders = []
    text = COMMENT_RE.sub("", text)

    def code(match: re.Match) -> str:
        content = match.group(2)
        if len(match.group(1)) > 1:
            content = content.strip()
        inner = escape(content)
        if ctx.link_code_paths:
            lesson = ctx.site.lesson_by_path(content.strip().rstrip("/"))
            if lesson:
                href, _ = ctx.href("/" + content.strip().lstrip("/"))   # repo-root relative
                return ctx.stash(f'<a href="{attr(href)}"><code>{inner}</code></a>')
        return ctx.stash(f"<code>{inner}</code>")

    text = CODESPAN_RE.sub(code, text)
    text = BR_RE.sub(lambda _: ctx.stash("<br>"), text)
    text = AUTOLINK_RE.sub(
        lambda m: ctx.stash(f'<a href="{attr(m.group(1))}" rel="noopener" target="_blank">{escape(m.group(1))}</a>'),
        text,
    )
    text = escape(text)

    def image(match: re.Match) -> str:
        href, _ = ctx.href(html.unescape(match.group(2)))
        title = f' title="{attr(match.group(3))}"' if match.group(3) else ""
        return ctx.stash(f'<img src="{attr(href)}" alt="{attr(html.unescape(match.group(1)))}"{title} loading="lazy">')

    text = IMAGE_RE.sub(image, text)

    def link(match: re.Match) -> str:
        label, url, title = match.group(1), html.unescape(match.group(2)), match.group(3)
        if not url:
            return label
        href, external = ctx.href(url)
        extra = ' rel="noopener" target="_blank"' if external else ""
        title_attr = f' title="{attr(title)}"' if title else ""
        return f'<a href="{attr(href)}"{title_attr}{extra}>{label}</a>'

    text = LINK_RE.sub(link, text)
    text = STRONG_EM_RE.sub(r"<strong><em>\1</em></strong>", text)
    text = STRONG_RE.sub(r"<strong>\1</strong>", text)
    text = STRONG_U_RE.sub(r"<strong>\1</strong>", text)
    text = EM_RE.sub(r"<em>\1</em>", text)
    text = EM_U_RE.sub(r"<em>\1</em>", text)
    text = STRIKE_RE.sub(r"<del>\1</del>", text)
    return ctx.restore(text)


def render_code_block(code: str, lang: str, ctx: RenderContext) -> str:
    lang = lang.lower()
    if lang == "mermaid":
        return f'<div class="diagram"><pre class="mermaid">{escape(code)}</pre></div>'
    hl = HLJS_LANG.get(lang, lang)
    label = lang or "text"
    return (
        f'<div class="codeblock" data-lang="{attr(label)}"><div class="codeblock-bar">'
        f'<span class="codeblock-lang">{escape(label)}</span>'
        f'<button type="button" class="copy-btn" aria-label="Copy code">Copy</button></div>'
        f'<pre><code class="language-{attr(hl)}">{escape(code)}</code></pre></div>'
    )


def render_table(rows: list[str], ctx: RenderContext) -> str:
    def split(row: str) -> list[str]:
        row = row.strip()
        if row.startswith("|"):
            row = row[1:]
        if row.endswith("|") and not row.endswith("\\|"):
            row = row[:-1]
        cells, current, index = [], [], 0
        while index < len(row):
            char = row[index]
            if char == "\\" and index + 1 < len(row) and row[index + 1] == "|":
                current.append("|")
                index += 2
                continue
            if char == "`":
                end = row.find("`", index + 1)
                if end == -1:
                    current.append(char)
                    index += 1
                    continue
                current.append(row[index:end + 1])
                index = end + 1
                continue
            if char == "|":
                cells.append("".join(current).strip())
                current = []
            else:
                current.append(char)
            index += 1
        cells.append("".join(current).strip())
        return cells

    header = split(rows[0])
    aligns = []
    for cell in split(rows[1]):
        cell = cell.strip()
        if cell.startswith(":") and cell.endswith(":"):
            aligns.append("center")
        elif cell.endswith(":"):
            aligns.append("right")
        elif cell.startswith(":"):
            aligns.append("left")
        else:
            aligns.append("")
    out = ['<div class="table-wrap"><table>', "<thead><tr>"]
    for index, cell in enumerate(header):
        style = f' style="text-align:{aligns[index]}"' if index < len(aligns) and aligns[index] else ""
        out.append(f"<th{style}>{render_inline(cell, ctx)}</th>")
    out.append("</tr></thead><tbody>")
    for row in rows[2:]:
        cells = split(row)
        out.append("<tr>")
        for index in range(len(header)):
            cell = cells[index] if index < len(cells) else ""
            style = f' style="text-align:{aligns[index]}"' if index < len(aligns) and aligns[index] else ""
            out.append(f"<td{style}>{render_inline(cell, ctx)}</td>")
        out.append("</tr>")
    out.append("</tbody></table></div>")
    return "".join(out)


def is_block_start(line: str, next_line: str | None) -> bool:
    """Does this line start a block that interrupts a paragraph?"""
    if FENCE_RE.match(line) or HEADING_RE.match(line) or HR_RE.match(line):
        return True
    if line.lstrip().startswith(">"):
        return True
    if LIST_RE.match(line) and len(LIST_RE.match(line).group(1)) < 4:
        return True
    if "|" in line and next_line is not None and TABLE_SEP_RE.match(next_line) and "|" in next_line:
        return True
    return False


def render_list(lines: list[str], ctx: RenderContext) -> str:
    first = LIST_RE.match(lines[0]) or EMPTY_LIST_RE.match(lines[0])
    base_indent = len(first.group(1))
    ordered = first.group(2)[0].isdigit()
    items: list[list[str]] = []
    for line in lines:
        match = LIST_RE.match(line) or EMPTY_LIST_RE.match(line)
        if match and abs(len(match.group(1)) - base_indent) <= 1 and match.group(2)[0].isdigit() == ordered:
            content_indent = len(match.group(1)) + len(match.group(2)) + 1
            items.append([match.group(3) if match.re is LIST_RE else "", str(content_indent)])
        elif items:
            strip = int(items[-1][1])
            stripped = line[:strip].lstrip() + line[strip:] if line[:strip].strip() == "" else line.lstrip()
            items[-1].append(stripped)
        else:
            items.append([line.strip(), "0"])
    start_attr = ""
    if ordered:
        start = int(re.match(r"\d+", first.group(2)).group(0))
        if start != 1:
            start_attr = f' start="{start}"'
    tag = "ol" if ordered else "ul"
    out = [f"<{tag}{start_attr}>"]
    task_list = False
    for item in items:
        body_lines = [item[0]] + item[2:]
        text = "\n".join(body_lines).rstrip("\n")
        task = TASK_RE.match(text)
        checkbox = ""
        if task:
            task_list = True
            checked = " checked" if task.group(1).lower() == "x" else ""
            checkbox = f'<input type="checkbox"{checked} aria-label="task"> '
            text = task.group(2)
        inner = render_blocks(text.split("\n"), ctx)
        if inner.count("<p>") == 1 and inner.startswith("<p>") and inner.endswith("</p>"):
            inner = inner[3:-4]
        li_class = ' class="task"' if task else ""
        if task:
            inner = f'<span class="task-text">{inner}</span>'
        out.append(f"<li{li_class}>{checkbox}{inner}</li>")
    out.append(f"</{tag}>")
    result = "".join(out)
    if task_list:
        result = result.replace(f"<{tag}{start_attr}>", f'<{tag}{start_attr} class="task-list">', 1)
    return result


def render_blocks(lines: list[str], ctx: RenderContext) -> str:
    out: list[str] = []
    index, total = 0, len(lines)
    while index < total:
        line = lines[index]
        if not line.strip():
            index += 1
            continue

        fence = FENCE_RE.match(line)
        if fence:
            marker, lang = fence.group(2), fence.group(3)
            indent = len(fence.group(1))
            index += 1
            code_lines = []
            closed = False
            while index < total:
                candidate = lines[index]
                if candidate.strip().startswith(marker[0] * 3) and candidate.strip().rstrip(marker[0]) == "" and len(candidate.strip()) >= len(marker):
                    closed = True
                    index += 1
                    break
                code_lines.append(candidate[indent:] if candidate[:indent].strip() == "" else candidate.lstrip())
                index += 1
            if not closed:
                unhandled(ctx.source, "unclosed code fence")
            out.append(render_code_block("\n".join(code_lines), lang, ctx))
            continue

        heading = HEADING_RE.match(line)
        if heading:
            level = min(6, len(heading.group(1)) + ctx.heading_shift)
            text_html = render_inline(heading.group(2), ctx)
            hid = ctx.heading_id(text_html)
            ctx.headings.append((level, html.unescape(TAG_RE.sub("", text_html)), hid))
            out.append(f'<h{level} id="{hid}">{text_html}<a class="anchor" href="#{hid}" aria-label="Link to section">#</a></h{level}>')
            index += 1
            continue

        if HR_RE.match(line) and not (index + 1 < total and "|" in lines[index + 1] and TABLE_SEP_RE.match(lines[index + 1])):
            out.append("<hr>")
            index += 1
            continue

        if line.lstrip().startswith(">"):
            quote_lines = []
            while index < total and lines[index].strip():
                current = lines[index].lstrip()
                if current.startswith(">"):
                    current = current[1:]
                    if current.startswith(" "):
                        current = current[1:]
                quote_lines.append(current)
                index += 1
            out.append(f"<blockquote>{render_blocks(quote_lines, ctx)}</blockquote>")
            continue

        if "|" in line and index + 1 < total and TABLE_SEP_RE.match(lines[index + 1]) and "|" in lines[index + 1]:
            rows = [line, lines[index + 1]]
            index += 2
            while index < total and lines[index].strip() and "|" in lines[index] and not FENCE_RE.match(lines[index]):
                rows.append(lines[index])
                index += 1
            out.append(render_table(rows, ctx))
            continue

        list_match = LIST_RE.match(line) or EMPTY_LIST_RE.match(line)
        if list_match and len(list_match.group(1)) < 4:
            base = len(list_match.group(1))
            block = [line]
            index += 1
            while index < total:
                current = lines[index]
                if not current.strip():
                    # a blank line ends the list unless the next non-blank line is still part of it
                    peek = index + 1
                    while peek < total and not lines[peek].strip():
                        peek += 1
                    if peek >= total:
                        break
                    nxt = lines[peek]
                    nxt_list = LIST_RE.match(nxt) or EMPTY_LIST_RE.match(nxt)
                    indent = len(nxt) - len(nxt.lstrip())
                    if (nxt_list and abs(len(nxt_list.group(1)) - base) <= 1) or indent >= base + 2:
                        block.extend(lines[index:peek])
                        index = peek
                        continue
                    break
                indent = len(current) - len(current.lstrip())
                cur_list = LIST_RE.match(current) or EMPTY_LIST_RE.match(current)
                if cur_list and abs(len(cur_list.group(1)) - base) <= 1:
                    block.append(current)
                elif indent >= base + 2:
                    block.append(current)
                elif cur_list and len(cur_list.group(1)) > base:
                    block.append(current)
                elif is_block_start(current, lines[index + 1] if index + 1 < total else None):
                    break
                else:
                    block.append(current)   # lazy continuation of the previous item
                index += 1
            out.append(render_list(block, ctx))
            continue

        # paragraph
        para = [line]
        index += 1
        while index < total and lines[index].strip():
            nxt = lines[index + 1] if index + 1 < total else None
            if is_block_start(lines[index], nxt):
                break
            para.append(lines[index])
            index += 1
        if all(META_LINE_RE.match(p.strip()) for p in para):
            cells = []
            for p in para:
                meta = META_LINE_RE.match(p.strip())
                cells.append(f"<div><dt>{render_inline(meta.group(1), ctx)}</dt><dd>{render_inline(meta.group(2), ctx) or '—'}</dd></div>")
            out.append(f'<dl class="meta">{"".join(cells)}</dl>')
            continue
        rendered = []
        for position, p in enumerate(para):
            text = p.strip()
            if position < len(para) - 1 and p.endswith("  "):
                text += "<br>"
            rendered.append(text)
        out.append(f"<p>{render_inline(' '.join(rendered), ctx).replace('<br> ', '<br>')}</p>")
    return "".join(out)


def render_markdown(text: str, ctx: RenderContext) -> str:
    text = text.replace("\r\n", "\n").replace("\t", "    ")
    return render_blocks(text.split("\n"), ctx)


# --------------------------------------------------------------------------------------
# Site model
# --------------------------------------------------------------------------------------

class Site:
    def __init__(self, out_dir: Path):
        self.out = out_dir
        self.catalog = self.load_catalog()
        self.languages = load_languages()
        self.lang_by_name = {lang["name"]: lang for lang in self.languages}
        self.lessons: list[dict] = []
        self.lesson_by_dir: dict[str, dict] = {}
        self.phase_by_slug: dict[str, dict] = {}
        for phase in self.catalog["phases"]:
            self.phase_by_slug[phase["slug"]] = phase
            for lesson in phase["lessons"]:
                lesson["phase"] = phase
                lesson["id"] = f"{phase['slug']}/{lesson['slug']}"
                lesson["url"] = f"phases/{phase['slug']}/{lesson['slug']}/index.html"
                self.lessons.append(lesson)
                self.lesson_by_dir[lesson["path"]] = lesson
        for position, lesson in enumerate(self.lessons):
            lesson["prev"] = self.lessons[position - 1] if position else None
            lesson["next"] = self.lessons[position + 1] if position + 1 < len(self.lessons) else None
        self.projects = []
        for manifest in sorted((ROOT / "projects").glob("*/project.json")):
            data = load_json(manifest)
            data["dir"] = manifest.parent
            self.projects.append(data)
        self.project_ids = {p["id"] for p in self.projects}
        self.paths = []
        for manifest in sorted((ROOT / "learning-paths").glob("*.json")):
            data = load_json(manifest)
            data["file"] = manifest.stem
            self.paths.append(data)
        self.path_ids = {p["file"] for p in self.paths}
        self.written: list[Path] = []
        self.search_entries: list[dict] = []

    @staticmethod
    def load_catalog() -> dict:
        catalog_file = ROOT / "catalog.json"
        if catalog_file.is_file():
            return load_json(catalog_file)
        from build_catalog import build_catalog  # local import: same directory
        return build_catalog()

    # -- lookups -----------------------------------------------------------------------

    def lesson_by_path(self, repo_path: str) -> dict | None:
        match = LESSON_PATH_RE.match(repo_path.strip("/"))
        if not match:
            return None
        return self.lesson_by_dir.get(f"phases/{match.group(1)}/{match.group(2)}")

    # -- links -------------------------------------------------------------------------

    def rewrite_link(self, url: str, source: str, page_url: str) -> tuple[str, bool]:
        """Map a Markdown link target found in `source` to an href usable from `page_url`."""
        if re.match(r"^[a-zA-Z][a-zA-Z0-9+.-]*:", url) or url.startswith("//"):
            return url, True
        if url.startswith("#"):
            return url, False
        target, _, fragment = url.partition("#")
        fragment = f"#{fragment}" if fragment else ""
        if target.startswith("/"):
            repo_path = target.lstrip("/")
        else:
            repo_path = posixpath.normpath(posixpath.join(posixpath.dirname(source), target))
        if repo_path.startswith("../") or repo_path == "..":
            return GITHUB_REPO, True
        repo_path = repo_path.strip("/")
        if repo_path == ".":
            repo_path = ""
        site_target, external = self.map_repo_path(repo_path, source)
        if external:
            return site_target + fragment, True
        if site_target == "index.html" and fragment:
            # README anchors: the Contents section lives on the roadmap; other anchors map to home sections.
            readme_anchor = {"#contents": ("roadmap.html", ""), "#projects": ("projects/index.html", ""),
                             "#learning-paths": ("paths/index.html", ""), "#choose-how-to-learn": ("index.html", "#how-to-learn")}
            site_target, fragment = readme_anchor.get(fragment.lower(), ("index.html", ""))
        if "#" in site_target:
            site_target, _, own = site_target.partition("#")
            fragment = f"#{own}"   # the generated anchor replaces whatever the source pointed at
        return self.relative(site_target, page_url) + fragment, False

    def map_repo_path(self, repo_path: str, source: str) -> tuple[str, bool]:
        parts = repo_path.split("/") if repo_path else []
        if not parts or repo_path in ("README.md", "README"):
            return "index.html", False
        if repo_path == "ROADMAP.md":
            return "roadmap.html", False
        if repo_path in ("glossary/terms.md", "glossary"):
            return "glossary.html", False
        if parts[0] == "phases" and len(parts) >= 2:
            phase = self.phase_by_slug.get(parts[1])
            if phase is None:
                return GITHUB_TREE + repo_path, True
            if len(parts) == 2 or (len(parts) == 3 and parts[2] in ("README.md", "")):
                return f"phases/{phase['slug']}/index.html", False
            lesson = self.lesson_by_dir.get("/".join(parts[:3]))
            if lesson is None:
                return GITHUB_BLOB + repo_path, True
            rest = parts[3:]
            if not rest or rest == ["docs", "en.md"] or rest == ["docs"] or rest == ["quiz.json"]:
                return lesson["url"], False
            if rest[0] == "outputs":
                anchor = f"#artifact-{slugify(Path(rest[-1]).stem)}" if len(rest) > 1 else "#artifacts"
                return lesson["url"] + anchor, False
            if rest[0] == "code":
                return GITHUB_BLOB + repo_path, True
            return GITHUB_BLOB + repo_path, True
        if parts[0] == "projects" and len(parts) >= 2 and parts[1] in self.project_ids:
            if len(parts) >= 4 and parts[2] == "stages":
                return f"projects/{parts[1]}/index.html#stage-{slugify(parts[3])}", False
            if len(parts) == 2 or parts[2] in ("README.md", "project.json", ""):
                return f"projects/{parts[1]}/index.html", False
            if parts[2] == "solution":
                return f"projects/{parts[1]}/index.html", False      # never publish solutions
            return GITHUB_BLOB + repo_path, True
        if parts[0] == "learning-paths" and len(parts) == 2:
            stem = Path(parts[1]).stem
            if stem in self.path_ids:
                return f"paths/{stem}.html", False
        full = ROOT / repo_path
        if full.is_dir():
            return GITHUB_TREE + repo_path, True
        if not full.exists():
            warn(f"{source}: link target does not exist in the repository: {repo_path}")
        return GITHUB_BLOB + repo_path, True

    @staticmethod
    def relative(target: str, page_url: str) -> str:
        base = posixpath.dirname(page_url)
        rel = posixpath.relpath(target, base) if base else target
        return rel

    # -- output ------------------------------------------------------------------------

    def write(self, url: str, content: str) -> None:
        path = self.out / url
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content, encoding="utf-8")
        self.written.append(path)


# --------------------------------------------------------------------------------------
# Page chrome
# --------------------------------------------------------------------------------------

NAV_LINKS = [
    ("roadmap.html", "Roadmap"),
    ("projects/index.html", "Projects"),
    ("paths/index.html", "Paths"),
    ("glossary.html", "Glossary"),
    ("search.html", "Search"),
    ("progress.html", "Progress"),
]


def page_shell(site: Site, page_url: str, *, title: str, body: str, description: str = "",
               active: str = "", toc: str = "", sidebar_phase: dict | None = None,
               needs_hljs: bool = False, needs_mermaid: bool = False, data_scripts: str = "",
               body_class: str = "") -> str:
    rel = lambda target: site.relative(target, page_url)  # noqa: E731
    active_class = lambda href: ' class="active"' if active == href else ""  # noqa: E731
    catalog = site.catalog
    nav = "".join(
        f'<a href="{rel(href)}"{active_class(href)}>{label}</a>'
        for href, label in NAV_LINKS
    )
    lang_buttons = "".join(
        f'<button type="button" data-lang="{lang["id"]}" title="{attr(lang["why"])}">{lang["name"]}</button>'
        for lang in site.languages
    )
    sidebar_items = []
    for track in catalog["tracks"]:
        sidebar_items.append(f'<li class="track-label">{escape(track["title"])}</li>')
        for phase in catalog["phases"]:
            if phase["number"] not in track["phases"]:
                continue
            current = sidebar_phase is not None and phase["slug"] == sidebar_phase["slug"]
            cls = ' class="current"' if current else ""
            sidebar_items.append(
                f'<li{cls}><a href="{rel("phases/" + phase["slug"] + "/index.html")}">'
                f'<span class="num">{phase["number"]}</span> {escape(phase["title"])}</a>'
            )
            if current:
                sidebar_items.append('<ul class="lesson-list">')
                for lesson in phase["lessons"]:
                    here_class = ' class="here"' if lesson["url"] == page_url else ""
                    sidebar_items.append(
                        f'<li{here_class}><a href="{rel(lesson["url"])}" data-lesson="{attr(lesson["id"])}">'
                        f'<span class="done-mark" aria-hidden="true"></span>{lesson["n"]:02d} {escape(lesson["title"])}</a></li>'
                    )
                sidebar_items.append("</ul>")
            sidebar_items.append("</li>")
    head_scripts = []
    if needs_hljs:
        head_scripts.append(f'<link rel="stylesheet" href="{HLJS_BASE}styles/github.min.css" media="(prefers-color-scheme: light)" data-hljs-theme="light">')
        head_scripts.append(f'<link rel="stylesheet" href="{HLJS_BASE}styles/github-dark.min.css" media="(prefers-color-scheme: dark)" data-hljs-theme="dark">')
        head_scripts.append(f'<script defer src="{HLJS_BASE}highlight.min.js"></script>')
    if needs_mermaid:
        head_scripts.append(f'<script defer src="{MERMAID_URL}"></script>')
    toc_block = ""
    toc_inline = ""
    if toc:
        toc_block = f'<aside class="toc" aria-label="On this page"><div class="toc-inner"><p class="toc-title">On this page</p>{toc}</div></aside>'
        toc_inline = f'<details class="toc-inline"><summary>On this page</summary>{toc}</details>'
    desc = attr(description or catalog["tagline"])
    return f"""<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{escape(title)} · {escape(catalog["title"])}</title>
<meta name="description" content="{desc}">
<meta name="color-scheme" content="light dark">
<link rel="icon" href="{rel("assets/favicon.svg")}" type="image/svg+xml">
<link rel="stylesheet" href="{rel("assets/site.css")}">
<script>(function(){{try{{var t=localStorage.getItem('pefs:theme');if(t)document.documentElement.setAttribute('data-theme',t);}}catch(e){{}}}})();</script>
{chr(10).join(head_scripts)}
</head>
<body class="{attr(body_class)}">
<a class="skip" href="#main">Skip to content</a>
<header class="topbar">
  <button type="button" class="nav-toggle" aria-label="Toggle phase navigation" aria-expanded="false">☰</button>
  <a class="brand" href="{rel("index.html")}"><span class="brand-mark" aria-hidden="true">₿</span><span>Payments Engineering <em>from Scratch</em></span></a>
  <nav class="site-nav" aria-label="Site">{nav}</nav>
  <div class="topbar-tools">
    <div class="lang-pick" role="group" aria-label="Implementation language">{lang_buttons}</div>
    <button type="button" class="theme-toggle" aria-label="Toggle dark mode" title="Toggle dark mode"><span class="sun">☀</span><span class="moon">☾</span></button>
    <a class="gh" href="{GITHUB_REPO}" rel="noopener" target="_blank" aria-label="GitHub repository">GitHub</a>
  </div>
</header>
<div class="layout">
  <aside class="sidebar" aria-label="Phases">
    <div class="sidebar-inner">
      <div class="sidebar-progress"><div class="progress" data-progress="all" data-total="{catalog["lessonCount"]}"><div class="progress-bar"></div></div><p class="progress-label"><span data-progress-text="all">0 of {catalog["lessonCount"]}</span> lessons complete</p></div>
      <ul class="phase-list">{"".join(sidebar_items)}</ul>
    </div>
  </aside>
  <main id="main" class="content">
    {toc_inline}
    {body}
  </main>
  {toc_block}
</div>
<footer class="footer">
  <p>{escape(catalog["title"])} · MIT licensed · <a href="{GITHUB_REPO}" rel="noopener" target="_blank">Source on GitHub</a> · built by <code>scripts/build_site.py</code>. Progress and language choice live in your browser only.</p>
</footer>
{data_scripts}
<script src="{rel("assets/site.js")}" defer></script>
</body>
</html>
"""


def breadcrumbs(site: Site, page_url: str, crumbs: list[tuple[str, str]]) -> str:
    parts = ['<nav class="crumbs" aria-label="Breadcrumb"><ol>']
    for index, (href, label) in enumerate(crumbs):
        if href and index < len(crumbs) - 1:
            parts.append(f'<li><a href="{site.relative(href, page_url)}">{escape(label)}</a></li>')
        else:
            parts.append(f'<li aria-current="page">{escape(label)}</li>')
    parts.append("</ol></nav>")
    return "".join(parts)


def toc_html(headings: list[tuple[int, str, str]], extra: list[tuple[str, str]] = ()) -> str:
    items = [f'<li class="lvl{level}"><a href="#{hid}">{escape(text)}</a></li>' for level, text, hid in headings if 2 <= level <= 3]
    items += [f'<li class="lvl2"><a href="#{hid}">{escape(text)}</a></li>' for hid, text in extra]
    return f'<ul class="toc-list">{"".join(items)}</ul>' if items else ""


def status_badge(status: str) -> str:
    """Authoring state. Every published lesson is Complete, so the badge is shown only for the
    exceptional states a fork might have (In Progress / Planned); learner progress is the tick mark."""
    if status == "Complete":
        return ""
    return f'<span class="badge status-{STATUS_CLASS[status]}" title="{attr("Authoring status: " + status)}">{STATUS_GLYPH[status]} {escape(status)}</span>'


def type_badge(kind: str) -> str:
    return f'<span class="badge type-{attr(kind.lower())}">{escape(kind)}</span>'


def lesson_list_item(site: Site, lesson: dict, page_url: str, *, show_phase: bool = False, show_hook: bool = True) -> str:
    langs = ", ".join(lesson["languages"]) or "—"
    phase_html = f'<span class="muted">Phase {lesson["phase"]["number"]} · </span>' if show_phase else ""
    hook = f'<p class="hook">{escape(lesson["hook"])}</p>' if show_hook and lesson.get("hook") else ""
    return (
        f'<li class="lesson-card" data-lesson="{attr(lesson["id"])}">'
        f'<div class="lesson-card-head"><span class="done-mark" aria-hidden="true"></span>'
        f'<a href="{site.relative(lesson["url"], page_url)}">{phase_html}{lesson["n"]:02d}. {escape(lesson["title"])}</a></div>'
        f'<div class="lesson-card-meta">{type_badge(lesson["type"])} {status_badge(lesson["status"])} '
        f'<span class="badge">~{lesson["minutes"]} min</span> <span class="badge langs">{escape(langs)}</span></div>{hook}</li>'
    )


def shell_command(command: list[str]) -> str:
    if len(command) == 3 and command[0] == "sh" and command[1] == "-c":
        return command[2]
    return " ".join(command)


# --------------------------------------------------------------------------------------
# Page builders
# --------------------------------------------------------------------------------------

def build_assets(site: Site) -> None:
    dest = site.out / "assets"
    dest.mkdir(parents=True, exist_ok=True)
    for asset in SITE_SRC.iterdir():
        if asset.is_file():
            shutil.copy2(asset, dest / asset.name)
            site.written.append(dest / asset.name)


def extract_readme_table(readme: str, summary: str) -> list[str]:
    marker = f"<summary>{summary}</summary>"
    start = readme.find(marker)
    if start == -1:
        return []
    end = readme.find("</details>", start)
    block = readme[start + len(marker):end]
    return [line for line in block.split("\n") if line.strip().startswith("|")]


def extract_readme_list(readme: str, summary: str) -> list[str]:
    marker = f"<summary>{summary}</summary>"
    start = readme.find(marker)
    if start == -1:
        return []
    end = readme.find("</details>", start)
    block = readme[start + len(marker):end]
    return [line for line in block.split("\n") if LIST_RE.match(line)]


def build_index(site: Site) -> None:
    url = "index.html"
    catalog = site.catalog
    readme = (ROOT / "README.md").read_text(encoding="utf-8")
    ctx = RenderContext(site, "README.md", url)
    skills_table = extract_readme_table(readme, "All learning skills")
    skills_html = render_table(skills_table, ctx) if len(skills_table) >= 2 else ""
    loop_lines = extract_readme_list(readme, "Use every lesson the same way")
    loop_html = render_list(loop_lines, ctx) if loop_lines else ""
    host_table = []
    in_host = False
    for line in readme.split("\n"):
        if line.startswith("| Host |"):
            in_host = True
        if in_host:
            if line.strip().startswith("|"):
                host_table.append(line)
            else:
                break
    host_html = render_table(host_table, ctx) if len(host_table) >= 2 else ""

    tracks = []
    for track in catalog["tracks"]:
        phases = [p for p in catalog["phases"] if p["number"] in track["phases"]]
        lesson_total = sum(len(p["lessons"]) for p in phases)
        done = sum(1 for p in phases for l in p["lessons"] if l["status"] == "Complete")
        minutes = sum(p["minutes"] for p in phases)
        phase_links = "".join(
            f'<li><a href="{site.relative("phases/" + p["slug"] + "/index.html", url)}"><span class="num">{p["number"]}</span> {escape(p["title"])}</a>'
            f'<span class="muted">{len(p["lessons"])} lessons</span></li>'
            for p in phases
        )
        track_id = track["id"]
        tracks.append(
            f'<section class="track track-{attr(track_id)}"><h3>{escape(track["title"])}</h3>'
            f'<p class="track-stats">{lesson_total} lessons · {(str(done) + " complete · ") if done != lesson_total else ""}~{minutes // 60} h</p>'
            f'<ul class="phase-links">{phase_links}</ul></section>'
        )
    hours = catalog["totalMinutes"] // 60
    stats = (
        f'<ul class="stats">'
        f'<li><strong>{catalog["lessonCount"]}</strong><span>lessons</span></li>'
        f'<li><strong>{catalog["phaseCount"]}</strong><span>phases</span></li>'
        f'<li><strong>{len(catalog["tracks"])}</strong><span>tracks</span></li>'
        f'<li><strong>{len(site.projects)}</strong><span>projects</span></li>'
        f'<li><strong>{hours}</strong><span>hours</span></li>'
        f'<li><strong>{len(catalog["languages"])}</strong><span>languages</span></li>'
        f'</ul>'
    )
    lang_cards = "".join(
        f'<li><button type="button" class="lang-card" data-lang="{lang["id"]}"><strong>{escape(lang["name"])}</strong>'
        f'<span>{escape(lang["why"])}</span></button></li>'
        for lang in site.languages
    )
    quick = [
        ("roadmap.html", "Roadmap", "Every phase and lesson with status, type, time, and your progress."),
        ("projects/index.html", "Projects", "Staged projects with graders in Java, Python, and Go."),
        ("paths/index.html", "Learning paths", "Focused routes: ledgers, consensus, interview prep."),
        ("glossary.html", "Glossary", "Canonical definitions with the lesson that teaches each one."),
        ("search.html", "Search", "Find the lesson that teaches a term or a mechanism."),
        ("progress.html", "Progress", "Your completed lessons by phase, stored in this browser."),
    ]
    quick_html = "".join(
        f'<li><a class="card" href="{site.relative(href, url)}"><strong>{escape(label)}</strong><span>{escape(text)}</span></a></li>'
        for href, label, text in quick
    )
    first = site.lessons[0]
    body = f"""
<section class="hero">
  <p class="eyebrow">Free · open source · standard library only</p>
  <h1>{escape(catalog["title"])}</h1>
  <p class="tagline">{escape(catalog["tagline"])}</p>
  <p class="hero-actions">
    <a class="btn primary" href="{site.relative(first["url"], url)}">Start with Phase 0</a>
    <a class="btn" href="{site.relative("roadmap.html", url)}">Browse the roadmap</a>
    <a class="btn" href="{GITHUB_REPO}" rel="noopener" target="_blank">Clone the repository</a>
  </p>
  {stats}
  <div class="progress-block"><div class="progress" data-progress="all" data-total="{catalog["lessonCount"]}"><div class="progress-bar"></div></div>
  <p class="progress-label"><span data-progress-text="all">0 of {catalog["lessonCount"]}</span> lessons marked complete in this browser · <a href="{site.relative("progress.html", url)}">see your progress</a></p></div>
</section>

<section id="language" class="section">
  <h2>Pick your language</h2>
  <p>Every Build lesson ships the same design in Java, Python, and Go, with the same tests and the same demo output. Choose once; every code tab on the site follows your choice (stored in this browser).</p>
  <ul class="lang-cards">{lang_cards}</ul>
</section>

<section id="tracks" class="section">
  <h2>Three tracks, one spine</h2>
  <p>Build the mechanism from scratch, prove its invariant with a test, then compare it with the production equivalent.</p>
  <div class="tracks">{"".join(tracks)}</div>
</section>

<section id="how-to-learn" class="section">
  <h2>How to learn</h2>
  <p>Every Build lesson follows the same loop: <strong>The Problem → The Concept → Build It → Use It → Ship It</strong>. Read the lesson here, then run and test the code from the repository in your language.</p>
  <h3>With an AI tutor</h3>
  <p>Clone the repository and open it in Claude Code, Codex, or any agent that reads <code>SKILL.md</code> files. The course teaches itself one lesson per session in your chosen language.</p>
  {host_html}
  <h3>The learning skills</h3>
  {skills_html}
  <h3>Use every lesson the same way</h3>
  {loop_html}
</section>

<section id="explore" class="section">
  <h2>Explore</h2>
  <ul class="cards">{quick_html}</ul>
</section>
"""
    site.write(url, page_shell(site, url, title="Home", body=body, active="index.html", body_class="home"))


def build_roadmap(site: Site) -> None:
    url = "roadmap.html"
    catalog = site.catalog
    sections = []
    for track in catalog["tracks"]:
        sections.append(f'<h2 id="{attr(track["id"])}">{escape(track["title"])}</h2>')
        for phase in catalog["phases"]:
            if phase["number"] not in track["phases"]:
                continue
            done = sum(1 for l in phase["lessons"] if l["status"] == "Complete")
            rows = []
            for lesson in phase["lessons"]:
                langs = ", ".join(lesson["languages"]) or "—"
                rows.append(
                    f'<tr data-lesson="{attr(lesson["id"])}"><td class="num">{lesson["n"]:02d}</td>'
                    f'<td><span class="done-mark" aria-hidden="true"></span><a href="{site.relative(lesson["url"], url)}">{escape(lesson["title"])}</a>'
                    f'<div class="hook small">{escape(lesson["hook"])}</div></td>'
                    f'<td>{type_badge(lesson["type"])}</td><td class="num">{lesson["minutes"]}</td>'
                    f'<td class="langs">{escape(langs)}</td><td class="done-cell"><span class="done-mark" aria-hidden="true"></span><span class="done-when small muted"></span>{status_badge(lesson["status"])}</td></tr>'
                )
            scope = f"phase-{phase['number']}"
            sections.append(
                f'<section class="phase-block" id="phase-{phase["number"]}" data-progress-scope="{scope}">'
                f'<h3><a href="{site.relative("phases/" + phase["slug"] + "/index.html", url)}"><span class="num">Phase {phase["number"]}</span> {escape(phase["title"])}</a></h3>'
                f'<p class="motto">{escape(phase["motto"])}</p>'
                f'<p class="phase-stats">{len(phase["lessons"])} lessons · {(str(done) + " complete · ") if done != len(phase["lessons"]) else ""}~{phase["minutes"] // 60} h · '
                f'<span data-progress-text="{scope}">0 of {len(phase["lessons"])}</span> done by you</p>'
                f'<div class="progress" data-progress="{scope}"><div class="progress-bar"></div></div>'
                f'<div class="table-wrap"><table class="roadmap"><thead><tr><th>#</th><th>Lesson</th><th>Type</th><th>Min</th><th>Languages</th><th>Done</th></tr></thead>'
                f'<tbody>{"".join(rows)}</tbody></table></div></section>'
            )
    body = f"""
{breadcrumbs(site, url, [("index.html", "Home"), ("", "Roadmap")])}
<h1>Roadmap</h1>
<p class="lede">{catalog["phaseCount"]} phases, {catalog["lessonCount"]} lessons, about {catalog["totalMinutes"] // 60} hours; every lesson ships docs, a quiz, and code in {escape(", ".join(catalog["languages"]))}. The <strong>Done</strong> column is your own progress, stored in this browser: mark lessons complete on their pages, or sync from <code>LEARNING.md</code> on the <a href="progress.html">Progress</a> page.</p>
<div class="progress-block"><div class="progress" data-progress="all" data-total="{catalog["lessonCount"]}"><div class="progress-bar"></div></div>
<p class="progress-label"><span data-progress-text="all">0 of {catalog["lessonCount"]}</span> lessons complete</p></div>
{"".join(sections)}
"""
    toc = toc_html([(2, t["title"], t["id"]) for t in catalog["tracks"]] +
                   [(3, f"Phase {p['number']}: {p['title']}", f"phase-{p['number']}") for p in catalog["phases"]])
    site.write(url, page_shell(site, url, title="Roadmap", body=body, active="roadmap.html", toc=toc))


def build_phase(site: Site, phase: dict) -> None:
    url = f"phases/{phase['slug']}/index.html"
    readme_path = ROOT / "phases" / phase["slug"] / "README.md"
    ctx = RenderContext(site, f"phases/{phase['slug']}/README.md", url)
    readme_html = render_markdown(readme_path.read_text(encoding="utf-8"), ctx) if readme_path.is_file() else f"<h1>Phase {phase['number']}: {escape(phase['title'])}</h1><blockquote><p>{escape(phase['motto'])}</p></blockquote>"
    done = sum(1 for l in phase["lessons"] if l["status"] == "Complete")
    scope = f"phase-{phase['number']}"
    lesson_items = "".join(lesson_list_item(site, lesson, url) for lesson in phase["lessons"])
    body = f"""
{breadcrumbs(site, url, [("index.html", "Home"), ("roadmap.html", "Roadmap"), ("", f"Phase {phase['number']}")])}
<div class="phase-readme" data-progress-scope="{scope}">
{readme_html}
<h2 id="your-progress">Your progress</h2>
<p class="phase-stats">{len(phase["lessons"])} lessons · {done} complete in the repository · <span data-progress-text="{scope}">0 of {len(phase["lessons"])}</span> marked done by you</p>
<div class="progress" data-progress="{scope}"><div class="progress-bar"></div></div>
<h2 id="lesson-cards">Lesson list</h2>
<ul class="lesson-cards">{lesson_items}</ul>
</div>
"""
    site.write(url, page_shell(
        site, url, title=f"Phase {phase['number']}: {phase['title']}", body=body, description=phase["motto"],
        sidebar_phase=phase, toc=toc_html(ctx.headings + [(2, "Your progress", "your-progress"), (2, "Lesson list", "lesson-cards")]),
        needs_hljs=True, needs_mermaid='class="mermaid"' in readme_html,
    ))


def lesson_code_files(lesson_dir: Path, lang: dict) -> list[Path]:
    folder = lesson_dir / "code" / lang["dir"]
    if not folder.is_dir():
        return []
    files = [f for f in folder.iterdir() if f.is_file() and not f.name.startswith(".") and f.suffix not in (".class", ".pyc")]
    entry, test_entry = lang["entry"], lang.get("testEntry")

    def order(path: Path) -> tuple:
        if path.name == entry:
            return (0, path.name)
        if path.name == test_entry:
            return (1, path.name)
        is_test = "test" in path.name.lower()
        return (2 if is_test else 3, path.name)   # main, tests, then helpers

    return sorted(files, key=order)


def build_lesson(site: Site, lesson: dict) -> None:
    phase = lesson["phase"]
    url = lesson["url"]
    lesson_dir = ROOT / lesson["path"]
    docs_path = lesson_dir / "docs" / "en.md"
    source = f"{lesson['path']}/docs/en.md"
    ctx = RenderContext(site, source, url)
    if docs_path.is_file():
        docs_md = docs_path.read_text(encoding="utf-8", errors="replace")
        try:
            docs_html = render_markdown(docs_md, ctx)
        except Exception as error:  # keep building; report the lesson
            unhandled(source, f"renderer raised {type(error).__name__}: {error}")
            docs_html = f"<h1>{escape(lesson['title'])}</h1><p>This lesson could not be rendered.</p>"
    else:
        docs_md = ""
        docs_html = f"<h1>{escape(lesson['title'])}</h1><blockquote><p>{escape(lesson['hook'])}</p></blockquote><p>This lesson is planned and has not been written yet.</p>"
    if "```" in TAG_RE.sub("", re.sub(r"<pre.*?</pre>", "", docs_html, flags=re.DOTALL)):
        unhandled(source, "a code fence survived into the rendered text")

    # key terms + headings for the search index
    key_terms = []
    in_terms = False
    for line in docs_md.split("\n"):
        if line.startswith("## "):
            in_terms = line.strip().lower() == "## key terms"
            continue
        if in_terms and line.startswith("|") and not TABLE_SEP_RE.match(line):
            first_cell = line.strip().strip("|").split("|")[0].strip().strip("`*")
            if first_cell and first_cell.lower() != "term":
                key_terms.append(first_cell)

    # code tabs
    code_sections = []
    tab_buttons = []
    lang_present = []
    for lang in site.languages:
        files = lesson_code_files(lesson_dir, lang)
        if not files:
            continue
        lang_present.append(lang["id"])
        run_cmd = shell_command(lang["run"])
        test_cmd = shell_command(lang["test"])
        file_blocks = []
        for path in files:
            text = path.read_text(encoding="utf-8", errors="replace")
            ext = path.suffix.lstrip(".")
            hl = {"java": "java", "py": "python", "go": "go", "mod": "plaintext", "sum": "plaintext", "md": "markdown", "txt": "plaintext"}.get(ext, "plaintext")
            blob = GITHUB_BLOB + f"{lesson['path']}/code/{lang['dir']}/{path.name}"
            file_blocks.append(
                f'<details class="code-file" {"open" if path.name == lang["entry"] else ""}>'
                f'<summary><code>{escape(path.name)}</code><span class="muted">{text.count(chr(10)) + 1} lines</span>'
                f'<a class="small" href="{blob}" rel="noopener" target="_blank">view on GitHub</a></summary>'
                f'<div class="codeblock" data-lang="{attr(lang["id"])}"><div class="codeblock-bar"><span class="codeblock-lang">{escape(path.name)}</span>'
                f'<button type="button" class="copy-btn" aria-label="Copy code">Copy</button></div>'
                f'<pre><code class="language-{hl}">{escape(text)}</code></pre></div></details>'
            )
        cmd_block = (
            f'<div class="codeblock" data-lang="bash"><div class="codeblock-bar"><span class="codeblock-lang">run &amp; test</span>'
            f'<button type="button" class="copy-btn" aria-label="Copy code">Copy</button></div>'
            f'<pre><code class="language-bash">cd {escape(lesson["path"])}/code/{lang["dir"]}\n{escape(run_cmd)}\n{escape(test_cmd)}</code></pre></div>'
        )
        tab_buttons.append(f'<button type="button" role="tab" data-lang="{lang["id"]}" aria-selected="false">{escape(lang["name"])}</button>')
        code_sections.append(
            f'<div class="code-panel" role="tabpanel" data-lang="{lang["id"]}" hidden>'
            f'<p class="small muted">{escape(lang["why"])}</p>{cmd_block}{"".join(file_blocks)}</div>'
        )
    code_html = ""
    if code_sections:
        code_html = (
            f'<section id="code" class="lesson-section code-tabs" data-langs="{" ".join(lang_present)}">'
            f'<h2 id="code-heading">Code<a class="anchor" href="#code" aria-label="Link to section">#</a></h2>'
            f'<p class="muted small">The same design in every language. The open tab follows your language choice; switch here or in the header.</p>'
            f'<div class="tabs" role="tablist" aria-label="Language">{"".join(tab_buttons)}</div>{"".join(code_sections)}</section>'
        )

    # quiz
    quiz_html = ""
    quiz_json = ""
    quiz_path = lesson_dir / "quiz.json"
    if quiz_path.is_file():
        try:
            quiz = load_json(quiz_path)
            questions = quiz.get("questions", [])
            if questions:
                quiz_json = json.dumps(questions, ensure_ascii=False).replace("</", "<\\/")
                quiz_html = (
                    f'<section id="quiz" class="lesson-section quiz"><h2 id="quiz-heading">Quiz<a class="anchor" href="#quiz" aria-label="Link to section">#</a></h2>'
                    f'<p class="muted small">{len(questions)} questions: <em>pre</em> activates what you already know, <em>check</em> tests the lesson, <em>post</em> is the exit quiz. Nothing is sent anywhere.</p>'
                    f'<div class="quiz-body" data-quiz="{attr(lesson["id"])}"><noscript><p>Enable JavaScript to take the quiz, or open <code>quiz.json</code> in the repository.</p></noscript></div></section>'
                )
        except (json.JSONDecodeError, OSError) as error:
            unhandled(f"{lesson['path']}/quiz.json", f"could not load quiz: {error}")

    # artifacts
    artifacts_html = ""
    outputs_dir = lesson_dir / "outputs"
    artifacts = []
    if outputs_dir.is_dir():
        for path in sorted(outputs_dir.glob("*.md")):
            meta, body_md = parse_frontmatter(path.read_text(encoding="utf-8", errors="replace"))
            art_id = f"artifact-{slugify(path.stem)}"
            actx = RenderContext(site, f"{lesson['path']}/outputs/{path.name}", url, id_prefix=f"{art_id}-", heading_shift=2)
            try:
                art_html = render_markdown(body_md, actx)
            except Exception as error:
                unhandled(f"{lesson['path']}/outputs/{path.name}", f"renderer raised {type(error).__name__}: {error}")
                art_html = "<p>This artifact could not be rendered.</p>"
            name = meta.get("name", path.stem)
            desc = meta.get("description", "")
            desc_html = f'<p class="muted">{escape(desc)}</p>' if desc else ""
            blob = GITHUB_BLOB + f"{lesson['path']}/outputs/{path.name}"
            artifacts.append(
                f'<article class="artifact" id="{art_id}"><header><h3 id="{art_id}-title">{escape(name)}<a class="anchor" href="#{art_id}" aria-label="Link to artifact">#</a></h3>'
                f'{desc_html}'
                f'<p class="small"><a href="{blob}" rel="noopener" target="_blank">Download from GitHub</a> · <code>{escape(path.name)}</code></p></header>'
                f'<div class="artifact-body">{art_html}</div></article>'
            )
    if artifacts:
        artifacts_html = (
            f'<section id="artifacts" class="lesson-section"><h2 id="artifacts-heading">Artifacts<a class="anchor" href="#artifacts" aria-label="Link to section">#</a></h2>'
            f'<p class="muted small">Reusable outputs from <code>outputs/</code>: checklists, design-review templates, runbooks, interview cards. Take them to work.</p>{"".join(artifacts)}</section>'
        )

    # navigation
    def nav_link(target: dict | None, label: str, cls: str) -> str:
        if not target:
            return f'<span class="{cls} empty"></span>'
        return (f'<a class="{cls}" href="{site.relative(target["url"], url)}"><span class="small muted">{label}</span>'
                f'<span>Phase {target["phase"]["number"]} · {target["n"]:02d} {escape(target["title"])}</span></a>')

    prev_next = f'<nav class="prev-next" aria-label="Lesson navigation">{nav_link(lesson["prev"], "← Previous", "prev")}{nav_link(lesson["next"], "Next →", "next")}</nav>'
    langs = ", ".join(lesson["languages"]) or "—"
    toolbar = (
        f'<div class="lesson-toolbar">{type_badge(lesson["type"])} {status_badge(lesson["status"])} '
        f'<span class="badge">~{lesson["minutes"]} min</span> <span class="badge langs">{escape(langs)}</span>'
        f'<span class="spacer"></span>'
        f'<button type="button" class="btn mark-complete" data-lesson="{attr(lesson["id"])}" aria-pressed="false">Mark complete</button>'
        f'<a class="btn ghost" href="{GITHUB_TREE}{lesson["path"]}" rel="noopener" target="_blank">Open in GitHub</a></div>'
    )
    extra_toc = []
    if code_html:
        extra_toc.append(("code", "Code"))
    if quiz_html:
        extra_toc.append(("quiz", "Quiz"))
    if artifacts_html:
        extra_toc.append(("artifacts", "Artifacts"))
    body = f"""
{breadcrumbs(site, url, [("index.html", "Home"), (f"phases/{phase['slug']}/index.html", f"Phase {phase['number']}: {phase['title']}"), ("", f"{lesson['n']:02d} {lesson['title']}")])}
<article class="lesson" data-lesson-id="{attr(lesson["id"])}">
{toolbar}
<div class="lesson-docs">{docs_html}</div>
{code_html}
{quiz_html}
{artifacts_html}
<div class="lesson-toolbar bottom"><button type="button" class="btn mark-complete" data-lesson="{attr(lesson["id"])}" aria-pressed="false">Mark complete</button><span class="small muted">Stored in this browser only.</span></div>
{prev_next}
</article>
"""
    data_scripts = f'<script type="application/json" id="quiz-data">{quiz_json}</script>' if quiz_json else ""
    site.write(url, page_shell(
        site, url, title=lesson["title"], body=body, description=lesson["hook"], sidebar_phase=phase,
        toc=toc_html(ctx.headings, extra_toc), needs_hljs=True, needs_mermaid='class="mermaid"' in docs_html or 'class="mermaid"' in artifacts_html,
        data_scripts=data_scripts, body_class="lesson-page",
    ))
    site.search_entries.append({
        "id": lesson["id"],
        "url": url,
        "title": lesson["title"],
        "hook": lesson["hook"],
        "phase": f"Phase {phase['number']}: {phase['title']}",
        "phaseNumber": phase["number"],
        "type": lesson["type"],
        "status": lesson["status"],
        "minutes": lesson["minutes"],
        "terms": key_terms,
        "headings": [text for level, text, _ in ctx.headings if level in (2, 3)],
    })


def build_projects(site: Site) -> None:
    index_url = "projects/index.html"
    cards = []
    for project in site.projects:
        url = f"projects/{project['id']}/index.html"
        cards.append(
            f'<li><a class="card" href="{site.relative(url, index_url)}"><strong>{escape(project["title"])}</strong>'
            f'<span class="badge">Level {project["level"]} · {escape(project["levelName"])}</span>'
            f'<span>{escape(project["tagline"])}</span><span class="muted small">{len(project["stages"])} stages · ~{project["hours"]} h · {escape(", ".join(project["languages"]))}</span></a></li>'
        )
        build_project(site, project)
    body = f"""
{breadcrumbs(site, index_url, [("index.html", "Home"), ("", "Projects")])}
<h1>Projects</h1>
<p class="lede">Staged projects with starters, graders, and reference solutions in Java, Python, and Go. The grader runs every earlier stage too, so a regression is reported the moment you introduce it. Reference solutions are for diagnosing, not copying: this site never shows them.</p>
<ul class="cards">{"".join(cards)}</ul>
"""
    site.write(index_url, page_shell(site, index_url, title="Projects", body=body, active="projects/index.html"))


def build_project(site: Site, project: dict) -> None:
    url = f"projects/{project['id']}/index.html"
    pid = project["id"]
    ctx = RenderContext(site, f"projects/{pid}/README.md", url, link_code_paths=True)
    prereqs = "".join(
        f'<li><a href="{ctx.href("/" + p["path"])[0]}">{escape(p["title"])}</a></li>' for p in project.get("prerequisites", [])
    )
    skills = "".join(f"<li>{escape(s)}</li>" for s in project.get("skills", []))
    lang_why = "".join(
        f'<div class="code-panel" data-lang="{site.lang_by_name[name]["id"]}" hidden><p>{escape(why)}</p></div>'
        for name, why in project.get("languageWhy", {}).items() if name in site.lang_by_name
    )
    langs = [site.lang_by_name[n] for n in project["languages"] if n in site.lang_by_name]
    tab_buttons = "".join(f'<button type="button" role="tab" data-lang="{l["id"]}" aria-selected="false">{escape(l["name"])}</button>' for l in langs)

    def grader(lang: dict, stage_number: int | None) -> str:
        ws = f"~/{pid}-{lang['id']}"
        if stage_number is None:
            lines = [
                f"python3 scripts/project_test.py {pid} --lang {lang['id']} --init {ws}",
                f"python3 scripts/project_test.py {pid} --lang {lang['id']} --stage 1 --path {ws}",
                f"python3 scripts/project_test.py {pid} --lang {lang['id']} --all --path {ws} --strict --report completion.json",
            ]
        else:
            lines = [f"python3 scripts/project_test.py {pid} --lang {lang['id']} --stage {stage_number} --path {ws}"]
        return (
            f'<div class="codeblock" data-lang="bash"><div class="codeblock-bar"><span class="codeblock-lang">{escape(lang["name"])} · from the repository root</span>'
            f'<button type="button" class="copy-btn" aria-label="Copy code">Copy</button></div><pre><code class="language-bash">{escape(chr(10).join(lines))}</code></pre></div>'
        )

    grader_panels = "".join(f'<div class="code-panel" data-lang="{l["id"]}" hidden>{grader(l, None)}</div>' for l in langs)
    stages_html = []
    toc_entries = []
    for number, stage in enumerate(project["stages"], start=1):
        sid = f"stage-{slugify(stage['id'])}"
        toc_entries.append((sid, f"Stage {number}: {stage['title']}"))
        stage_dir = project["dir"] / "stages" / stage["id"]
        docs_path = stage_dir / "docs" / "en.md"
        sctx = RenderContext(site, f"projects/{pid}/stages/{stage['id']}/docs/en.md", url, id_prefix=f"{sid}-", heading_shift=2, link_code_paths=True)
        docs_html = ""
        if docs_path.is_file():
            try:
                docs_html = render_markdown(docs_path.read_text(encoding="utf-8", errors="replace"), sctx)
            except Exception as error:
                unhandled(sctx.source, f"renderer raised {type(error).__name__}: {error}")
                docs_html = "<p>This stage could not be rendered.</p>"
        file_lists = []
        for lang in langs:
            parts = []
            for kind in ("starter", "tests"):
                folder = stage_dir / kind / lang["dir"]
                if folder.is_dir():
                    names = sorted(f.name for f in folder.iterdir() if f.is_file())
                    links = ", ".join(
                        f'<a href="{GITHUB_BLOB}projects/{pid}/stages/{stage["id"]}/{kind}/{lang["dir"]}/{escape(n)}" rel="noopener" target="_blank"><code>{escape(n)}</code></a>'
                        for n in names
                    )
                    parts.append(f"<li><strong>{kind}/</strong> {links}</li>")
            file_lists.append(f'<div class="code-panel" data-lang="{lang["id"]}" hidden><ul class="file-list">{"".join(parts) or "<li>No files for this stage.</li>"}</ul>{grader(lang, number)}</div>')
        concepts = "".join(f'<span class="badge">{escape(c)}</span> ' for c in stage.get("concepts", []))
        stages_html.append(
            f'<section class="stage code-tabs" id="{sid}"><h2 id="{sid}-heading"><span class="num">Stage {number}</span> {escape(stage["title"])}<a class="anchor" href="#{sid}" aria-label="Link to stage">#</a></h2>'
            f'<p class="lesson-card-meta"><span class="badge">{escape(stage.get("difficulty", ""))}</span> <span class="badge">~{stage.get("hours", "?")} h</span> {concepts}</p>'
            f'<p>{escape(stage["summary"])}</p>'
            f'<div class="stage-docs">{docs_html}</div>'
            f'<h3 id="{sid}-files">Files and grader</h3><div class="tabs" role="tablist" aria-label="Language">{tab_buttons}</div>{"".join(file_lists)}</section>'
        )
    body = f"""
{breadcrumbs(site, url, [("index.html", "Home"), ("projects/index.html", "Projects"), ("", project["title"])])}
<article class="project">
<h1>{escape(project["title"])}</h1>
<blockquote><p>{escape(project["tagline"])}</p></blockquote>
<dl class="meta"><div><dt>Level</dt><dd>{project["level"]} — {escape(project["levelName"])}</dd></div><div><dt>Time</dt><dd>~{project["hours"]} hours</dd></div>
<div><dt>Languages</dt><dd>{escape(", ".join(project["languages"]))}</dd></div><div><dt>Status</dt><dd>{escape(str(project.get("status", "")))}</dd></div></dl>
<p>{escape(project["summary"])}</p>
<h2 id="you-will-build">What you will build</h2>
<p>{escape(project.get("youWillBuild", ""))}</p>
<ul>{skills}</ul>
<h2 id="prerequisites">Prerequisites</h2>
<ul>{prereqs or "<li>None</li>"}</ul>
<section class="code-tabs" id="grader">
<h2 id="grader-heading">Grader commands</h2>
<p>Pick a language (the tab follows your global choice). Create a workspace from the stage-1 starter, grade a stage, or grade everything with a strict report.</p>
<div class="tabs" role="tablist" aria-label="Language">{tab_buttons}</div>
{grader_panels}
<h3 id="why-language">Why this language here</h3>
{lang_why}
</section>
<h2 id="stages">Stages</h2>
<p class="muted small">Work the stages in order. Starters and tests are linked on GitHub; reference solutions are deliberately not published here.</p>
{"".join(stages_html)}
<p><a class="btn ghost" href="{GITHUB_TREE}projects/{pid}" rel="noopener" target="_blank">Open the project on GitHub</a></p>
</article>
"""
    toc = toc_html([(2, "What you will build", "you-will-build"), (2, "Prerequisites", "prerequisites"), (2, "Grader commands", "grader-heading"), (2, "Stages", "stages")] +
                   [(3, text, hid) for hid, text in toc_entries])
    site.write(url, page_shell(site, url, title=project["title"], body=body, description=project["tagline"], active="projects/index.html", toc=toc, needs_hljs=True, needs_mermaid='class="mermaid"' in body))


def build_paths(site: Site) -> None:
    index_url = "paths/index.html"
    cards = []
    for path in site.paths:
        url = f"paths/{path['file']}.html"
        cards.append(
            f'<li><a class="card" href="{site.relative(url, index_url)}"><strong>{escape(path["title"])}</strong>'
            f'<span>{escape(path.get("summary", ""))}</span><span class="muted small">{len(path["lessons"])} lessons · ~{path.get("estimatedMinutes", 0) // 60} h</span></a></li>'
        )
        build_path(site, path)
    body = f"""
{breadcrumbs(site, index_url, [("index.html", "Home"), ("", "Learning paths")])}
<h1>Learning paths</h1>
<p class="lede">Focused routes through the curriculum. Each path is a manifest in <code>learning-paths/</code> that the tutor skills follow; here it is an ordered reading list with your progress.</p>
<ul class="cards">{"".join(cards)}</ul>
"""
    site.write(index_url, page_shell(site, index_url, title="Learning paths", body=body, active="paths/index.html"))


def build_path(site: Site, path: dict) -> None:
    url = f"paths/{path['file']}.html"
    items = []
    checkpoints = path.get("checkpoints", {})
    scope = f"path-{path['file']}"
    for entry in sorted(path["lessons"], key=lambda e: e.get("order", 0)):
        lesson = site.lesson_by_dir.get(entry["path"])
        checkpoint = checkpoints.get(entry["path"])
        cp_html = f'<p class="checkpoint"><strong>Checkpoint:</strong> {escape(checkpoint)}</p>' if checkpoint else ""
        required = "required" if entry.get("required", True) else "optional"
        if lesson:
            items.append(
                f'<li class="lesson-card" data-lesson="{attr(lesson["id"])}"><div class="lesson-card-head"><span class="done-mark" aria-hidden="true"></span>'
                f'<a href="{site.relative(lesson["url"], url)}">{escape(entry.get("title", lesson["title"]))}</a></div>'
                f'<div class="lesson-card-meta"><span class="badge">Phase {lesson["phase"]["number"]} · Lesson {lesson["n"]:02d}</span> {type_badge(lesson["type"])} {status_badge(lesson["status"])} '
                f'<span class="badge">~{entry.get("minutes", lesson["minutes"])} min</span> <span class="badge">{required}</span></div>{cp_html}</li>'
            )
        else:
            warn(f"learning-paths/{path['file']}.json: unknown lesson path {entry['path']}")
            items.append(f'<li class="lesson-card"><div class="lesson-card-head">{escape(entry.get("title", entry["path"]))} <span class="muted">(not in curriculum)</span></div></li>')
    project_html = ""
    if path.get("project") in site.project_ids:
        project = next(p for p in site.projects if p["id"] == path["project"])
        project_url = site.relative("projects/" + project["id"] + "/index.html", url)
        project_html = f'<p>Then build the project: <a href="{project_url}"><strong>{escape(project["title"])}</strong></a> — {escape(project["tagline"])}</p>'
    invocation = path.get("invocation", {})
    inv_rows = "".join(f"<tr><td>{escape(k)}</td><td><code>{escape(v)}</code></td></tr>" for k, v in invocation.items())
    body = f"""
{breadcrumbs(site, url, [("index.html", "Home"), ("paths/index.html", "Learning paths"), ("", path["title"])])}
<article class="path" data-progress-scope="{scope}">
<h1>{escape(path["title"])}</h1>
<p class="lede">{escape(path.get("summary", ""))}</p>
<dl class="meta"><div><dt>Lessons</dt><dd>{len(path["lessons"])}</dd></div><div><dt>Time</dt><dd>~{path.get("estimatedMinutes", 0) // 60} hours</dd></div>
<div><dt>Tutor languages</dt><dd>{escape(", ".join(path.get("languages", [])) or "any")}</dd></div><div><dt>State file</dt><dd><code>{escape(path.get("stateFile", ""))}</code></dd></div></dl>
<p class="phase-stats"><span data-progress-text="{scope}">0 of {len(path["lessons"])}</span> marked done by you</p>
<div class="progress" data-progress="{scope}"><div class="progress-bar"></div></div>
<h2 id="lessons">Lessons, in order</h2>
<ol class="lesson-cards ordered">{"".join(items)}</ol>
{project_html}
<h2 id="tutor">With a tutor</h2>
<div class="table-wrap"><table><thead><tr><th>Host</th><th>Invocation</th></tr></thead><tbody>{inv_rows}</tbody></table></div>
<p class="muted small">{escape(path.get("conceptualFallback", ""))}</p>
<p><a class="btn ghost" href="{GITHUB_BLOB}learning-paths/{path['file']}.json" rel="noopener" target="_blank">View the manifest</a></p>
</article>
"""
    site.write(url, page_shell(site, url, title=path["title"], body=body, description=path.get("summary", ""), active="paths/index.html",
                               toc=toc_html([(2, "Lessons, in order", "lessons"), (2, "With a tutor", "tutor")])))


def build_glossary(site: Site) -> None:
    url = "glossary.html"
    text = (ROOT / "glossary" / "terms.md").read_text(encoding="utf-8", errors="replace")
    ctx = RenderContext(site, "glossary/terms.md", url, link_code_paths=True)
    lines = text.split("\n")
    intro: list[str] = []
    letters: list[tuple[str, list[tuple[str, list[str]]]]] = []
    current_letter = None
    current_term = None
    in_fence = False
    for line in lines:
        if FENCE_RE.match(line):
            in_fence = not in_fence
        if not in_fence and re.match(r"^## \S+\s*$", line):
            current_letter = line[3:].strip()
            letters.append((current_letter, []))
            current_term = None
            continue
        if not in_fence and line.startswith("### ") and current_letter is not None:
            current_term = (line[4:].strip(), [])
            letters[-1][1].append(current_term)
            continue
        if current_letter is None:
            if not in_fence and line.startswith("# "):
                continue  # the page template already renders the H1
            intro.append(line)
        elif current_term is not None:
            current_term[1].append(line)
    intro_html = render_markdown("\n".join(intro), ctx)
    count = sum(len(terms) for _, terms in letters)
    jump = "".join(f'<a href="#letter-{attr(letter.lower())}">{escape(letter)}</a>' for letter, _ in letters)
    sections = []
    for letter, terms in letters:
        entries = []
        for name, body_lines in terms:
            tid = ctx.heading_id(name)
            body_html = render_markdown("\n".join(body_lines), ctx)
            category = ""
            match = re.search(r"\*\*Category:\*\*\s*(.+)", "\n".join(body_lines))
            if match:
                category = match.group(1).strip()
            entries.append(
                f'<article class="term" id="{tid}" data-term="{attr(name.lower())}" data-category="{attr(category.lower())}">'
                f'<h3>{render_inline(name, ctx)}<a class="anchor" href="#{tid}" aria-label="Link to term">#</a></h3>{body_html}</article>'
            )
        sections.append(f'<section class="letter" id="letter-{attr(letter.lower())}"><h2>{escape(letter)}</h2>{"".join(entries)}</section>')
    body = f"""
{breadcrumbs(site, url, [("index.html", "Home"), ("", "Glossary")])}
<h1>Glossary</h1>
<div class="glossary-intro">{intro_html}</div>
<div class="glossary-tools">
  <label class="filter"><span class="visually-hidden">Filter terms</span><input type="search" id="glossary-filter" placeholder="Filter {count} terms (name, definition, category)…" autocomplete="off"></label>
  <p class="small muted" id="glossary-count">{count} terms</p>
  <nav class="jump-bar" aria-label="Jump to letter">{jump}</nav>
</div>
<div class="glossary">{"".join(sections)}</div>
<p class="muted small glossary-empty" hidden>No terms match.</p>
"""
    site.write(url, page_shell(site, url, title="Glossary", body=body, active="glossary.html", needs_hljs=True))


def build_search(site: Site) -> None:
    url = "search.html"
    index = {"generated": time.strftime("%Y-%m-%d"), "lessons": site.search_entries}
    payload = json.dumps(index, ensure_ascii=False, separators=(",", ":"))
    site.write("search-index.json", payload + "\n")
    site.write("assets/search-index.js", "window.PEFS_SEARCH_INDEX=" + payload.replace("</", "<\\/") + ";\n")
    body = f"""
{breadcrumbs(site, url, [("index.html", "Home"), ("", "Search")])}
<h1>Search</h1>
<p class="lede">Search {len(site.search_entries)} lessons by title, hook, phase, key terms, and section headings. Matching is by word prefix; results rank title matches first, then key terms, then hooks and headings.</p>
<form class="search-form" role="search" onsubmit="return false">
  <label><span class="visually-hidden">Search lessons</span><input type="search" id="search-input" placeholder="idempotency, raft, rounding, ISO 8583…" autocomplete="off" autofocus></label>
</form>
<p class="small muted" id="search-status">Type to search.</p>
<ol class="search-results" id="search-results"></ol>
"""
    scripts = f'<script src="{site.relative("assets/search-index.js", url)}"></script>'
    site.write(url, page_shell(site, url, title="Search", body=body, active="search.html", data_scripts=scripts, body_class="search-page"))


def build_progress(site: Site) -> None:
    url = "progress.html"
    catalog = site.catalog
    sections = []
    for phase in catalog["phases"]:
        scope = f"phase-{phase['number']}"
        rows = "".join(
            f'<li data-lesson="{attr(l["id"])}"><span class="done-mark" aria-hidden="true"></span><a href="{site.relative(l["url"], url)}">{l["n"]:02d} {escape(l["title"])}</a>'
            f'<span class="muted small done-when"></span></li>'
            for l in phase["lessons"]
        )
        sections.append(
            f'<section class="progress-phase" data-progress-scope="{scope}"><h2 id="phase-{phase["number"]}"><a href="{site.relative("phases/" + phase["slug"] + "/index.html", url)}">Phase {phase["number"]}: {escape(phase["title"])}</a></h2>'
            f'<p class="phase-stats"><span data-progress-text="{scope}">0 of {len(phase["lessons"])}</span> complete · <span data-progress-percent="{scope}">0%</span></p>'
            f'<div class="progress" data-progress="{scope}"><div class="progress-bar"></div></div>'
            f'<details><summary>Lessons</summary><ul class="progress-list">{rows}</ul></details></section>'
        )
    lesson_index = json.dumps(
        [{"id": l["id"], "p": phase["number"], "n": l["n"], "t": l["title"]} for phase in catalog["phases"] for l in phase["lessons"]],
        separators=(",", ":"),
    ).replace("</", "<\\/")
    body = f"""
{breadcrumbs(site, url, [("index.html", "Home"), ("", "Progress")])}
<h1>Your progress</h1>
<p class="lede">Lessons you marked complete, by phase. Everything here lives in this browser's local storage; nothing is uploaded. The tutor skills keep the authoritative record in <code>LEARNING.md</code> in your clone.</p>
<div class="progress-block"><div class="progress big" data-progress="all" data-total="{catalog["lessonCount"]}"><div class="progress-bar"></div></div>
<p class="progress-label"><strong><span data-progress-text="all">0 of {catalog["lessonCount"]}</span></strong> lessons · <span data-progress-percent="all">0%</span></p></div>
<section class="progress-sync" id="sync">
<h2 id="move-progress">Move your progress between devices</h2>
<p>Progress is stored only in this browser. To carry it to another device, download it here and import it there; the import merges, so nothing already marked is lost.</p>
<p class="progress-actions"><button type="button" class="btn primary" id="download-progress">Download progress (JSON)</button> <button type="button" class="btn" id="import-file-btn">Import a progress file</button> <input type="file" id="import-file" accept=".json,application/json" hidden> <button type="button" class="btn ghost" id="export-progress">Copy as text</button> <button type="button" class="btn ghost" id="import-progress">Paste text</button></p>
<h3 id="sync-learning-md">Sync with <code>LEARNING.md</code></h3>
<p>If you study with the tutor skills (<code>/learn</code>), your clone's <code>LEARNING.md</code> is the authoritative record. Load it here and every lesson in its progress log, plus every phase marked <code>Done</code> in its path table, is marked complete on this site. <code>LEDGER-LEARNING.md</code> and <code>CONSENSUS-LEARNING.md</code> work too.</p>
<p class="progress-actions"><button type="button" class="btn" id="learning-file-btn">Load LEARNING.md</button> <input type="file" id="learning-file" accept=".md,text/markdown,text/plain" hidden> <button type="button" class="btn ghost" id="learning-paste-btn">Paste its contents</button></p>
<div id="sync-preview" class="sync-preview" hidden></div>
<p class="progress-actions"><button type="button" class="btn danger" id="reset-progress">Reset progress</button></p>
</section>
{"".join(sections)}
<script type="application/json" id="lesson-index">{lesson_index}</script>
"""
    site.write(url, page_shell(site, url, title="Progress", body=body, active="progress.html",
                               toc=toc_html([(2, "Move your progress", "move-progress")] + [(2, f"Phase {p['number']}: {p['title']}", f"phase-{p['number']}") for p in catalog["phases"]])))


# --------------------------------------------------------------------------------------
# Validation (--check)
# --------------------------------------------------------------------------------------

class PageInspector(HTMLParser):
    """Collects links and visible text (outside pre/code/script/style) from one page."""

    SKIP = {"pre", "code", "script", "style", "textarea"}

    def __init__(self) -> None:
        super().__init__(convert_charrefs=True)
        self.links: list[str] = []
        self.text: list[str] = []
        self.skip_depth = 0
        self.ids: set[str] = set()
        self.stack: list[str] = []
        self.errors: list[str] = []

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if tag in self.SKIP:
            self.skip_depth += 1
        if tag not in ("br", "img", "input", "meta", "link", "hr", "source"):
            self.stack.append(tag)
        if "id" in attrs:
            if attrs["id"] in self.ids:
                self.errors.append(f"duplicate id {attrs['id']!r}")
            self.ids.add(attrs["id"])
        for name in ("href", "src"):
            if attrs.get(name):
                self.links.append(attrs[name])

    def handle_endtag(self, tag):
        if tag in self.SKIP and self.skip_depth:
            self.skip_depth -= 1
        if self.stack and self.stack[-1] == tag:
            self.stack.pop()
        elif tag in self.stack:
            self.errors.append(f"misnested </{tag}> (open: {' > '.join(self.stack[-4:])})")
            while self.stack and self.stack[-1] != tag:
                self.stack.pop()
            if self.stack:
                self.stack.pop()
        else:
            self.errors.append(f"stray </{tag}>")

    def handle_data(self, data):
        if not self.skip_depth:
            self.text.append(data)


RAW_MD_RE = re.compile(r"\s*(#{1,6} |```|\|\s*-{3,}\s*\|)")
VOID_OK = {"html", "head", "body", "p", "li", "dt", "dd", "tr", "td", "th", "option", "thead", "tbody"}


def check_site(site: Site) -> int:
    problems: list[str] = []
    pages = sorted(site.out.rglob("*.html"))
    inspected: dict[Path, PageInspector] = {}
    for page in pages:
        rel_page = page.relative_to(site.out).as_posix()
        inspector = PageInspector()
        try:
            inspector.feed(page.read_text(encoding="utf-8"))
            inspector.close()
        except Exception as error:  # html.parser is lenient; anything raised is a real problem
            problems.append(f"{rel_page}: does not parse: {error}")
            continue
        inspected[page.resolve()] = inspector
        unclosed = [t for t in inspector.stack if t not in VOID_OK]
        if unclosed:
            problems.append(f"{rel_page}: unclosed tags {unclosed[:5]}")
        for error in inspector.errors[:5]:
            problems.append(f"{rel_page}: {error}")
        for chunk in inspector.text:
            if RAW_MD_RE.match(chunk) or "```" in chunk:
                problems.append(f"{rel_page}: raw Markdown marker in text: {chunk.strip()[:60]!r}")
                break
    # second pass: every internal link resolves to a generated file, and anchors exist on the target page
    for page, inspector in inspected.items():
        rel_page = page.relative_to(site.out.resolve()).as_posix()
        seen: set[str] = set()
        for link in inspector.links:
            if link in seen:
                continue
            seen.add(link)
            if re.match(r"^[a-zA-Z][a-zA-Z0-9+.-]*:", link) or link.startswith("//"):
                continue
            target, _, fragment = link.partition("#")
            resolved = page if not target else (page.parent / target).resolve()
            if not resolved.is_file():
                problems.append(f"{rel_page}: broken internal link {link!r}")
                continue
            if fragment and resolved in inspected and fragment not in inspected[resolved].ids:
                problems.append(f"{rel_page}: link {link!r} points at a missing anchor")
    for problem in problems[:80]:
        print("  " + problem)
    if len(problems) > 80:
        print(f"  ... and {len(problems) - 80} more")
    print(f"--check: {len(pages)} pages inspected, {len(problems)} problem(s)")
    return 1 if problems else 0


# --------------------------------------------------------------------------------------
# Main
# --------------------------------------------------------------------------------------

def build(out_dir: Path) -> Site:
    if out_dir.exists():
        shutil.rmtree(out_dir)
    out_dir.mkdir(parents=True)
    site = Site(out_dir)
    build_assets(site)
    build_index(site)
    build_roadmap(site)
    for phase in site.catalog["phases"]:
        build_phase(site, phase)
    for lesson in site.lessons:
        build_lesson(site, lesson)
    build_projects(site)
    build_paths(site)
    build_glossary(site)
    build_search(site)
    build_progress(site)
    (out_dir / ".nojekyll").write_text("", encoding="utf-8")
    return site


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--check", action="store_true", help="build, then validate the generated pages")
    parser.add_argument("--out", default=str(ROOT / "site"), help="output directory (default: site/)")
    args = parser.parse_args()
    started = time.perf_counter()
    site = build(Path(args.out))
    elapsed = time.perf_counter() - started
    pages = sum(1 for p in site.written if p.suffix == ".html")
    print(f"site: {pages} pages, {len(site.lessons)} lessons, {len(site.projects)} projects, {len(site.paths)} paths -> {args.out} in {elapsed:.1f}s")
    for source, messages in sorted(UNHANDLED.items()):
        for message in messages:
            print(f"  unhandled: {source}: {message}")
    for message in WARNINGS[:40]:
        print(f"  warning: {message}")
    if len(WARNINGS) > 40:
        print(f"  ... and {len(WARNINGS) - 40} more warnings")
    status = 0
    if args.check:
        status = check_site(site)
        if UNHANDLED:
            status = 1
    return status


if __name__ == "__main__":
    sys.exit(main())
