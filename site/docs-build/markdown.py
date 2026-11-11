"""A small CommonMark + GFM-tables renderer, enough for the p0ssess docs.

Covers ATX headings, paragraphs, fenced and indented code, lists (nested, loose
and tight), block quotes, GFM tables, thematic breaks, simple HTML blocks,
code spans, emphasis, links, images, autolinks, escapes, entities and hard
breaks. Inline raw HTML is escaped, not passed through.
"""

import html
import html.entities
import re
import unicodedata

FENCE = re.compile(r"^( {0,3})(`{3,}|~{3,})[ \t]*([^`\s]*)[^`]*$")
ATX = re.compile(r"^ {0,3}(#{1,6})(?:[ \t]+(.*?))?(?:[ \t]+#+)?[ \t]*$")
HR = re.compile(r"^ {0,3}([-*_])(?:[ \t]*\1){2,}[ \t]*$")
BULLET = re.compile(r"^( {0,3})([-+*])( +|\t|$)(.*)$")
ORDERED = re.compile(r"^( {0,3})(\d{1,9})([.)])( +|\t|$)(.*)$")
QUOTE = re.compile(r"^ {0,3}> ?(.*)$")
HTML_BLOCK = re.compile(
    r"^ {0,3}</?(?:p|div|h[1-6]|img|table|details|summary|section|figure|br|hr|center)(?:[\s/>]|$)",
    re.I,
)
DELIM_ROW = re.compile(r"^ {0,3}\|?[ \t]*:?-+:?[ \t]*(?:\|[ \t]*:?-+:?[ \t]*)*\|?[ \t]*$")
ENTITY = re.compile(r"&(#[0-9]{1,7}|#[xX][0-9a-fA-F]{1,6}|[A-Za-z][A-Za-z0-9]{1,31});")
AUTOLINK = re.compile(r"<([A-Za-z][A-Za-z0-9+.-]{1,31}:[^\s<>]*|[A-Za-z0-9.!#$%&'*+/=?^_`{|}~-]+@[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*)>")
BARE_URL = re.compile(r"(?:https?://|www\.)[^\s<]*")
PUNCT = set("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~")


def esc(s):
    return s.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;").replace('"', "&quot;")


def esc_text(s):
    """Escape text but keep valid entity references."""
    out, last = [], 0
    for m in ENTITY.finditer(s):
        name = m.group(1)
        ok = name.startswith("#") or (name + ";") in html.entities.html5
        if not ok:
            continue
        out.append(esc(s[last:m.start()]))
        out.append(m.group(0))
        last = m.end()
    out.append(esc(s[last:]))
    return "".join(out)


def unescape_url_text(s):
    s = re.sub(r"\\([!-/:-@\[-`{-~])", r"\1", s)
    return html.unescape(s)


def is_punct(c):
    if c in PUNCT:
        return True
    return unicodedata.category(c)[0] in "PS"


def is_space(c):
    return c == "" or c.isspace()


def expand_tabs(line):
    return line.expandtabs(4) if "\t" in line else line


def indent_of(line):
    return len(line) - len(line.lstrip(" "))


def plain_text(rendered):
    """Text content of rendered inline HTML."""
    return html.unescape(re.sub(r"<[^>]+>", "", rendered))


class Block:
    def __init__(self, kind, **kw):
        self.kind = kind
        self.blank_before = False
        self.__dict__.update(kw)


def starts_block(line, in_paragraph=True):
    """Whether a line can interrupt a paragraph."""
    if FENCE.match(line) or ATX.match(line) or HR.match(line) or QUOTE.match(line) or HTML_BLOCK.match(line):
        return True
    m = BULLET.match(line)
    if m and m.group(4).strip():
        return True
    m = ORDERED.match(line)
    if m and m.group(5).strip() and (not in_paragraph or m.group(2) == "1"):
        return True
    return False


def list_marker(line):
    """(kind, marker-key, start, content-indent, first-line content) or None."""
    m = BULLET.match(line)
    if m:
        lead, mark, sp, rest = m.groups()
        kind, key, start = "ul", mark, None
    else:
        m = ORDERED.match(line)
        if not m:
            return None
        lead, num, delim, sp, rest = m.groups()
        kind, key, start, mark = "ol", delim, int(num), num + delim
    if not rest.strip():
        width = len(lead) + len(mark) + 1
        return kind, key, start, width, ""
    if len(sp) > 4:
        width = len(lead) + len(mark) + 1
        rest = sp[1:] + rest
    else:
        width = len(lead) + len(mark) + len(sp)
    return kind, key, start, width, rest


def in_fence(body):
    """Whether these item lines end inside an open code fence."""
    fence = None
    for ln in body:
        m = FENCE.match(ln)
        if fence is None and m:
            fence = m.group(2)
        elif fence is not None and re.match(r"^ {0,3}" + re.escape(fence[0]) + "{%d,}[ \t]*$" % len(fence), ln):
            fence = None
    return fence is not None


def split_row(line):
    s = line.strip()
    if s.startswith("|"):
        s = s[1:]
    if s.endswith("|") and not s.endswith("\\|"):
        s = s[:-1]
    cells, cur, i = [], [], 0
    while i < len(s):
        c = s[i]
        if c == "\\" and i + 1 < len(s) and s[i + 1] == "|":
            cur.append("|")
            i += 2
            continue
        if c == "|":
            cells.append("".join(cur).strip())
            cur = []
        else:
            cur.append(c)
        i += 1
    cells.append("".join(cur).strip())
    return cells


def parse_blocks(lines):
    blocks = []
    i, n = 0, len(lines)
    blank = False

    def add(b):
        nonlocal blank
        b.blank_before = blank
        blank = False
        blocks.append(b)

    while i < n:
        line = expand_tabs(lines[i])
        if not line.strip():
            blank = True
            i += 1
            continue

        m = FENCE.match(line)
        if m:
            ind, fence, info = len(m.group(1)), m.group(2), m.group(3)
            body, i = [], i + 1
            while i < n:
                ln = expand_tabs(lines[i])
                cm = re.match(r"^ {0,3}(" + re.escape(fence[0]) + r"{%d,})[ \t]*$" % len(fence), ln)
                if cm:
                    i += 1
                    break
                k = 0
                while k < ind and k < len(ln) and ln[k] == " ":
                    k += 1
                body.append(ln[k:])
                i += 1
            add(Block("code", lang=unescape_url_text(info), text="\n".join(body) + ("\n" if body else "")))
            continue

        if indent_of(line) >= 4:
            body = []
            while i < n:
                ln = expand_tabs(lines[i])
                if ln.strip() and indent_of(ln) < 4:
                    break
                body.append(ln[4:] if ln.strip() else "")
                i += 1
            while body and not body[-1]:
                body.pop()
            add(Block("code", lang="", text="\n".join(body) + "\n"))
            continue

        m = ATX.match(line)
        if m:
            add(Block("heading", level=len(m.group(1)), text=(m.group(2) or "").strip()))
            i += 1
            continue

        if HR.match(line):
            add(Block("hr"))
            i += 1
            continue

        if HTML_BLOCK.match(line):
            body = []
            while i < n and lines[i].strip():
                body.append(lines[i])
                i += 1
            add(Block("html", text="\n".join(body)))
            continue

        if QUOTE.match(line):
            body = []
            while i < n:
                ln = expand_tabs(lines[i])
                qm = QUOTE.match(ln)
                if qm:
                    body.append(qm.group(1))
                elif ln.strip() and body and body[-1].strip() and not starts_block(ln):
                    body.append(ln)
                else:
                    break
                i += 1
            add(Block("quote", children=parse_blocks(body)))
            continue

        if "|" in line and i + 1 < n and DELIM_ROW.match(lines[i + 1]) and "|" in lines[i + 1] + line:
            head = split_row(line)
            aligns = []
            for c in split_row(lines[i + 1]):
                c = c.strip()
                aligns.append("center" if c.startswith(":") and c.endswith(":") else "right" if c.endswith(":") else "left" if c.startswith(":") else None)
            if len(aligns) == len(head):
                rows, i = [], i + 2
                while i < n and lines[i].strip() and not starts_block(expand_tabs(lines[i])):
                    cells = split_row(lines[i])
                    cells = (cells + [""] * len(head))[: len(head)]
                    rows.append(cells)
                    i += 1
                add(Block("table", head=head, aligns=aligns, rows=rows))
                continue

        lm = list_marker(line)
        if lm:
            kind, key, start, _, _ = lm
            items, loose = [], False
            while i < n:
                ln = expand_tabs(lines[i])
                lm = list_marker(ln)
                if not lm or lm[0] != kind or lm[1] != key or HR.match(ln):
                    break
                _, _, _, width, first = lm
                body, i = [first], i + 1
                while i < n:
                    ln = expand_tabs(lines[i])
                    if not ln.strip():
                        body.append("")
                        i += 1
                        continue
                    if indent_of(ln) >= width:
                        body.append(ln[width:])
                        i += 1
                        continue
                    prev = body[-1]
                    if prev.strip() and not list_marker(ln) and not starts_block(ln) and not in_fence(body) and not ATX.match(prev):
                        body.append(ln.strip())
                        i += 1
                        continue
                    break
                trailing = 0
                while len(body) > 1 and not body[-1].strip():
                    body.pop()
                    trailing += 1
                children = parse_blocks(body)
                if any(c.blank_before for c in children[1:]):
                    loose = True
                items.append(children)
                if trailing and i < n:
                    nxt = list_marker(expand_tabs(lines[i]))
                    if nxt and nxt[0] == kind and nxt[1] == key and not HR.match(expand_tabs(lines[i])):
                        loose = True
            add(Block("list", ordered=kind == "ol", start=start, loose=loose, items=items))
            blank = trailing > 0
            continue

        body = [line.strip()]
        i += 1
        while i < n:
            ln = expand_tabs(lines[i])
            if not ln.strip() or starts_block(ln):
                break
            if "|" in ln and i + 1 < n and DELIM_ROW.match(lines[i + 1]):
                break
            body.append(ln.strip())
            i += 1
        add(Block("para", text="\n".join(body)))
    return blocks


class Delim:
    def __init__(self, char, count, can_open, can_close):
        self.char = char
        self.n = count
        self.orig = count
        self.can_open = can_open
        self.can_close = can_close


def find_code_end(s, i):
    """i at a backtick run; return (end index, content) or None."""
    j = i
    while j < len(s) and s[j] == "`":
        j += 1
    run = j - i
    k = j
    while True:
        k = s.find("`", k)
        if k < 0:
            return None
        e = k
        while e < len(s) and s[e] == "`":
            e += 1
        if e - k == run:
            return e, s[j:k]
        k = e


def find_bracket_end(s, i):
    """i at '['; index of the matching ']' or -1."""
    depth, j = 0, i
    while j < len(s):
        c = s[j]
        if c == "\\":
            j += 2
            continue
        if c == "`":
            r = find_code_end(s, j)
            if r:
                j = r[0]
                continue
            while j < len(s) and s[j] == "`":
                j += 1
            continue
        if c == "[":
            depth += 1
        elif c == "]":
            depth -= 1
            if depth == 0:
                return j
        j += 1
    return -1


def parse_dest(s, i):
    """i just after '('; return (url, title, end index after ')') or None."""
    n = len(s)
    while i < n and s[i] in " \t\n":
        i += 1
    if i < n and s[i] == "<":
        j = s.find(">", i)
        if j < 0:
            return None
        url, i = s[i + 1:j], j + 1
    else:
        depth, j = 0, i
        while j < n:
            c = s[j]
            if c == "\\" and j + 1 < n:
                j += 2
                continue
            if c.isspace():
                break
            if c == "(":
                depth += 1
            elif c == ")":
                if depth == 0:
                    break
                depth -= 1
            j += 1
        url, i = s[i:j], j
    while i < n and s[i] in " \t\n":
        i += 1
    title = None
    if i < n and s[i] in "\"'(":
        close = ")" if s[i] == "(" else s[i]
        j = s.find(close, i + 1)
        if j < 0:
            return None
        title, i = s[i + 1:j], j + 1
        while i < n and s[i] in " \t\n":
            i += 1
    if i < n and s[i] == ")":
        return unescape_url_text(url), title, i + 1
    return None


class Inline:
    def __init__(self, link_hook=None, image_hook=None, wbr=True):
        self.link_hook = link_hook or (lambda u: u)
        self.image_hook = image_hook or (lambda u: u)
        self.wbr = wbr

    def render(self, s, in_link=False):
        nodes, buf = [], []

        def flush():
            if buf:
                nodes.append(esc_text("".join(buf)))
                buf.clear()

        i, n = 0, len(s)
        while i < n:
            c = s[i]
            if c == "\\" and i + 1 < n:
                d = s[i + 1]
                if d == "\n":
                    flush()
                    nodes.append("<br>\n")
                    i += 2
                    continue
                if d in PUNCT:
                    buf.append(d)
                    i += 2
                    continue
                buf.append(c)
                i += 1
                continue
            if c == "`":
                r = find_code_end(s, i)
                if r:
                    end, code = r
                    code = code.replace("\n", " ")
                    if len(code) > 1 and code[0] == " " and code[-1] == " " and code.strip():
                        code = code[1:-1]
                    flush()
                    code = esc(code)
                    if self.wbr:
                        # Long paths and identifiers break after a slash or underscore first.
                        code = re.sub(r"([/_]+)(?=[^/_])", r"\1<wbr>", code)
                    nodes.append("<code>" + code + "</code>")
                    i = end
                    continue
                j = i
                while j < n and s[j] == "`":
                    j += 1
                buf.append(s[i:j])
                i = j
                continue
            if c == "<":
                m = AUTOLINK.match(s, i)
                if m and not in_link:
                    target = m.group(1)
                    href = target if ":" in target else "mailto:" + target
                    flush()
                    nodes.append('<a href="%s">%s</a>' % (esc(self.link_hook(href)), esc(target)))
                    i = m.end()
                    continue
            if (c == "[" or (c == "!" and i + 1 < n and s[i + 1] == "[")):
                start = i + (1 if c == "!" else 0)
                close = find_bracket_end(s, start)
                if close > 0 and close + 1 < n and s[close + 1] == "(" and not (in_link and c == "["):
                    d = parse_dest(s, close + 2)
                    if d:
                        url, title, end = d
                        text = s[start + 1:close]
                        t = ' title="%s"' % esc(title) if title is not None else ""
                        flush()
                        if c == "!":
                            alt = plain_text(self.render(text, True))
                            nodes.append('<img src="%s" alt="%s"%s>' % (esc(self.image_hook(url)), esc(alt), t))
                        else:
                            nodes.append('<a href="%s"%s>%s</a>' % (esc(self.link_hook(url)), t, self.render(text, True)))
                        i = end
                        continue
            if c in "*_":
                j = i
                while j < n and s[j] == c:
                    j += 1
                before = s[i - 1] if i > 0 else ""
                after = s[j] if j < n else ""
                left = not is_space(after) and (not is_punct(after) or is_space(before) or is_punct(before))
                right = not is_space(before) and (not is_punct(before) or is_space(after) or is_punct(after))
                if c == "*":
                    can_open, can_close = left, right
                else:
                    can_open = left and (not right or is_punct(before))
                    can_close = right and (not left or is_punct(after))
                flush()
                nodes.append(Delim(c, j - i, can_open, can_close))
                i = j
                continue
            if c == "\n":
                trail = 0
                while buf and buf[-1] == " ":
                    buf.pop()
                    trail += 1
                flush()
                nodes.append("<br>\n" if trail >= 2 else "\n")
                i += 1
                while i < n and s[i] == " ":
                    i += 1
                continue
            if c in "hw" and not in_link and (i == 0 or not s[i - 1].isalnum()):
                m = BARE_URL.match(s, i)
                if m and (m.group(0).startswith("http") or "." in m.group(0)[4:]):
                    url = m.group(0)
                    while url and url[-1] in "?!.,:*_~'\"":
                        url = url[:-1]
                    while url.endswith(")") and url.count(")") > url.count("("):
                        url = url[:-1]
                    if len(url) > 8:
                        href = url if url.startswith("http") else "http://" + url
                        flush()
                        nodes.append('<a href="%s">%s</a>' % (esc(self.link_hook(href)), esc(url)))
                        i += len(url)
                        continue
            buf.append(c)
            i += 1
        flush()
        return self.emphasis(nodes)

    @staticmethod
    def emphasis(nodes):
        ci = 0
        while ci < len(nodes):
            closer = nodes[ci]
            if not (isinstance(closer, Delim) and closer.can_close and closer.n > 0):
                ci += 1
                continue
            oi, found = ci - 1, None
            while oi >= 0:
                o = nodes[oi]
                if isinstance(o, Delim) and o.char == closer.char and o.can_open and o.n > 0:
                    odd = (o.can_close or closer.can_open) and (o.orig + closer.orig) % 3 == 0 and not (o.orig % 3 == 0 and closer.orig % 3 == 0)
                    if not odd:
                        found = oi
                        break
                oi -= 1
            if found is None:
                ci += 1
                continue
            o = nodes[found]
            use = 2 if o.n >= 2 and closer.n >= 2 else 1
            tag = "strong" if use == 2 else "em"
            inner = "".join(x if isinstance(x, str) else x.char * x.n for x in nodes[found + 1:ci])
            o.n -= use
            closer.n -= use
            nodes[found + 1:ci] = ["<%s>%s</%s>" % (tag, inner, tag)]
            ci = found + 2
            if o.n == 0:
                del nodes[found]
                ci -= 1
            if closer.n == 0:
                del nodes[ci]
        return "".join(x if isinstance(x, str) else x.char * x.n for x in nodes)


def slugify(text, seen):
    s = text.strip().lower()
    s = re.sub(r"[^\w\- ]", "", s)
    s = s.replace(" ", "-")
    base, k = s, 1
    while s in seen:
        s = "%s-%d" % (base, k)
        k += 1
    seen.add(s)
    return s


class Renderer:
    """Turns blocks into HTML. plain=True gives bare CommonMark-style output."""

    def __init__(self, link_hook=None, image_hook=None, html_hook=None, plain=False):
        self.inline = Inline(link_hook, image_hook, wbr=not plain)
        self.html_hook = html_hook or (lambda raw: raw)
        self.plain = plain
        self.headings = []
        self.seen = set()
        self.tables = 0

    def blocks(self, blocks, tight=False):
        out = []
        for b in blocks:
            out.append(self.block(b, tight))
        return "\n".join(o for o in out if o)

    def block(self, b, tight=False):
        k = b.kind
        if k == "para":
            text = self.inline.render(b.text)
            return text if tight else "<p>%s</p>" % text
        if k == "heading":
            inner = self.inline.render(b.text)
            if self.plain:
                return "<h%d>%s</h%d>" % (b.level, inner, b.level)
            hid = slugify(plain_text(inner), self.seen)
            self.headings.append((b.level, hid, inner))
            anchor = '<a class="hl" href="#%s" aria-hidden="true" tabindex="-1">#</a>' % hid if b.level > 1 else ""
            return '<h%d id="%s">%s%s</h%d>' % (b.level, hid, inner, anchor, b.level)
        if k == "code":
            cls = ' class="language-%s"' % esc(b.lang) if b.lang else ""
            pre = "<pre><code%s>%s</code></pre>" % (cls, esc(b.text))
            if self.plain:
                return pre
            label = '<div class="code-bar"><span class="code-lang">%s</span></div>' % esc(b.lang) if b.lang else ""
            return '<div class="code">%s%s</div>' % (label, pre)
        if k == "hr":
            return "<hr>"
        if k == "html":
            return self.html_hook(b.text)
        if k == "quote":
            return "<blockquote>\n%s\n</blockquote>" % self.blocks(b.children)
        if k == "list":
            tag = "ol" if b.ordered else "ul"
            start = ' start="%d"' % b.start if b.ordered and b.start != 1 else ""
            items = []
            for children in b.items:
                inner = self.blocks(children, tight=not b.loose)
                if not b.loose and children and children[0].kind == "para":
                    items.append("<li>%s</li>" % inner)
                else:
                    items.append("<li>\n%s\n</li>" % inner if inner else "<li></li>")
            return "<%s%s>\n%s\n</%s>" % (tag, start, "\n".join(items), tag)
        if k == "table":
            def cell(t, text, a):
                if a:
                    attr = ' align="%s"' % a if self.plain else ' class="al-%s"' % a[0]
                else:
                    attr = ""
                if t == "th" and not self.plain:
                    attr += ' scope="col"'
                return "<%s%s>%s</%s>" % (t, attr, self.inline.render(text), t)

            head = "<tr>\n%s\n</tr>" % "\n".join(cell("th", c, a) for c, a in zip(b.head, b.aligns))
            body = "\n".join("<tr>\n%s\n</tr>" % "\n".join(cell("td", c, a) for c, a in zip(r, b.aligns)) for r in b.rows)
            table = "<table>\n<thead>\n%s\n</thead>\n%s</table>" % (head, "<tbody>\n%s\n</tbody>\n" % body if b.rows else "")
            if self.plain:
                return table
            self.tables += 1
            return '<div class="table-wrap" role="region" tabindex="0" aria-label="Table %d">\n%s\n</div>' % (self.tables, table)
        raise ValueError(k)


def render(text, **kw):
    """Return (html, headings) for a Markdown document."""
    r = Renderer(**kw)
    out = r.blocks(parse_blocks(text.split("\n")))
    return out + "\n", r.headings
