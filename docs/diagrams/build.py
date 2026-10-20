#!/usr/bin/env python3
"""Generate the animated SVG diagrams used by README.md.

    python docs/diagrams/build.py                     # writes docs/assets/*.svg
    python docs/diagrams/build.py --freeze 6 OUT_DIR  # copies paused at t=6s (for checking frames)

Every diagram is plain SVG with CSS/SMIL animation, so it renders inside a GitHub
README (<img>) with no JavaScript and no web fonts.

Rules every diagram follows:
- The static (un-animated) state is the *complete* picture, so viewers with
  prefers-reduced-motion still see the whole diagram.
- One loop period per diagram, so everything in it stays in sync.
- Diagrams show the target design at the level the phase plan fixes. Anything
  a phase brief still has to decide stays out of them.

Edit the data in this file, never the generated SVGs.
"""
from __future__ import annotations

import math
import sys
from pathlib import Path
from xml.sax.saxutils import escape

OUT = Path(__file__).resolve().parents[1] / "assets"

# roadmap.svg: phases below this are drawn as done, this one pulses as next.
CURRENT_PHASE = 1

FONT = "ui-sans-serif,-apple-system,BlinkMacSystemFont,'Segoe UI',Inter,Roboto,Helvetica,Arial,sans-serif"
MONO = "ui-monospace,SFMono-Regular,'Cascadia Mono','JetBrains Mono',Menlo,Consolas,monospace"

CARD, CARD2 = "#131A2A", "#1B2439"
LINE, LINE2 = "#232D43", "#35405C"
TEXT, MUTED, DIM = "#E8ECF4", "#98A3B9", "#5F6A84"
EMBER, AMBER, CYAN, GREEN, ROSE, VIOLET, BLUE = (
    "#FB923C", "#FBBF24", "#22D3EE", "#34D399", "#FB7185", "#A78BFA", "#60A5FA")
INK = "#0A0F1A"  # wells, tracks, terminal background
LABEL_BG = "#0D1322"

BASE_CSS = f"""
text{{font-family:{FONT};white-space:pre;}}
.m{{font-family:{MONO};}}
.flow{{stroke-dasharray:3 7;animation:flow 1.1s linear infinite;}}
.flow-slow{{stroke-dasharray:2 8;animation:flow 2.4s linear infinite;}}
@keyframes flow{{to{{stroke-dashoffset:-20;}}}}
.blink{{animation:blink 1.06s step-end infinite;}}
@keyframes blink{{50%{{opacity:0;}}}}
.breathe{{animation:breathe 3.6s ease-in-out infinite;}}
@keyframes breathe{{0%,100%{{opacity:.55;}}50%{{opacity:1;}}}}
.ping{{transform-box:fill-box;transform-origin:center;animation:ping 2.2s cubic-bezier(0,0,.2,1) infinite;}}
@keyframes ping{{0%{{transform:scale(1);opacity:.75;}}80%,100%{{transform:scale(2.4);opacity:0;}}}}
.flicker{{transform-box:fill-box;transform-origin:50% 100%;animation:flicker 2.8s ease-in-out infinite;}}
@keyframes flicker{{0%,100%{{transform:scale(1,1);}}30%{{transform:scale(.97,1.05);}}55%{{transform:scale(1.02,.97);}}80%{{transform:scale(.99,1.03);}}}}
@media (prefers-reduced-motion:reduce){{*{{animation:none!important;}}}}
"""

SHARED_DEFS = (
    '<linearGradient id="bg" x1="0" y1="0" x2="0" y2="1">'
    '<stop offset="0" stop-color="#0F1626"/><stop offset="1" stop-color="#090D16"/></linearGradient>'
    '<pattern id="dots" width="22" height="22" patternUnits="userSpaceOnUse">'
    '<circle cx="1.5" cy="1.5" r="1" fill="#1A2338"/></pattern>'
    '<radialGradient id="vig"><stop offset="0" stop-color="#EA580C" stop-opacity=".13"/>'
    '<stop offset="1" stop-color="#EA580C" stop-opacity="0"/></radialGradient>'
    '<filter id="glow" x="-200%" y="-200%" width="500%" height="500%">'
    '<feGaussianBlur stdDeviation="2.6" result="b"/>'
    '<feMerge><feMergeNode in="b"/><feMergeNode in="SourceGraphic"/></feMerge></filter>'
    '<linearGradient id="ember" x1="0" y1="0" x2="1" y2="1">'
    '<stop offset="0" stop-color="#FDBA74"/><stop offset="1" stop-color="#F97316"/></linearGradient>'
)

FLAME = ("M0,-38 C9,-25 24,-14 24,7 C24,24 13,34 0,34 C-13,34 -24,24 -24,7 "
         "C-24,-7 -15,-15 -9,-26 C-7,-17 -3,-13 0,-11 C3,-20 -2,-29 0,-38 Z")
FLAME_CORE = "M0,-4 C5,4 11,10 11,18 C11,26 6,30 0,30 C-6,30 -11,26 -11,18 C-11,10 -5,4 0,-4 Z"


# ── primitives ──────────────────────────────────────────────────────────────

def f(v: float) -> str:
    return f"{v:.2f}".rstrip("0").rstrip(".")


def tw(s: str, size: float, mono: bool = False) -> float:
    """Rough rendered width; good enough for sizing pills and checking overlap."""
    return len(s) * size * (0.6 if mono else 0.56)


def T(x, y, s, size=13, fill=TEXT, weight=400, anchor="start", mono=False, extra=""):
    cls = ' class="m"' if mono else ""
    ex = f" {extra}" if extra else ""
    return (f'<text x="{f(x)}" y="{f(y)}" font-size="{size}" fill="{fill}" font-weight="{weight}" '
            f'text-anchor="{anchor}"{cls}{ex}>{escape(s)}</text>')


def Tp(x, y, parts, size=13, weight=400, anchor="start", mono=False, extra=""):
    """Text made of (string, colour) runs."""
    cls = ' class="m"' if mono else ""
    ex = f" {extra}" if extra else ""
    runs = "".join(f'<tspan fill="{c}">{escape(t)}</tspan>' for t, c in parts)
    return (f'<text x="{f(x)}" y="{f(y)}" font-size="{size}" font-weight="{weight}" '
            f'text-anchor="{anchor}"{cls}{ex}>{runs}</text>')


def R(x, y, w, h, rx=10, fill="none", stroke="none", sw=1, extra=""):
    ex = f" {extra}" if extra else ""
    return (f'<rect x="{f(x)}" y="{f(y)}" width="{f(w)}" height="{f(h)}" rx="{f(rx)}" '
            f'fill="{fill}" stroke="{stroke}" stroke-width="{sw}"{ex}/>')


def P(d, stroke=LINE2, sw=1.5, fill="none", extra=""):
    ex = f" {extra}" if extra else ""
    return f'<path d="{d}" fill="{fill}" stroke="{stroke}" stroke-width="{sw}" stroke-linecap="round"{ex}/>'


def C(cx, cy, r, fill, extra=""):
    ex = f" {extra}" if extra else ""
    return f'<circle cx="{f(cx)}" cy="{f(cy)}" r="{f(r)}" fill="{fill}"{ex}/>'


def head(x, y, d, color, s=4.5):
    k = s * 1.4
    pts = {
        "down": [(x - s, y - k), (x + s, y - k), (x, y)],
        "up": [(x - s, y + k), (x + s, y + k), (x, y)],
        "right": [(x - k, y - s), (x - k, y + s), (x, y)],
        "left": [(x + k, y - s), (x + k, y + s), (x, y)],
    }[d]
    return f'<polygon points="{" ".join(f"{f(a)},{f(b)}" for a, b in pts)}" fill="{color}"/>'


def card(x, y, w, h, title, sub, accent, tsize=13.5):
    o = [R(x, y, w, h, 10, CARD, LINE), R(x, y + 11, 3, h - 22, 1.5, accent)]
    if sub:
        o.append(T(x + 16, y + h / 2 - 3, title, tsize, TEXT, 600))
        o.append(T(x + 16, y + h / 2 + 14, sub, 10.5, MUTED, mono=True))
    else:
        o.append(T(x + 16, y + h / 2 + 5, title, tsize, TEXT, 600))
    return "<g>" + "".join(o) + "</g>"


def pill(cx, cy, label, color, size=11, h=22, mono=True, extra=""):
    w = tw(label, size, mono) + 22
    ex = f" {extra}" if extra else ""
    return (f"<g{ex}>" + R(cx - w / 2, cy - h / 2, w, h, h / 2, color, color, 1,
                           'fill-opacity=".12" stroke-opacity=".55"')
            + T(cx, cy + size * 0.36, label, size, color, 500, "middle", mono) + "</g>")


def tag(cx, cy, text, color, size=10.5):
    """A label sitting on a line: opaque background so the line never cuts through it."""
    w = tw(text, size, True) + 14
    return (R(cx - w / 2, cy - 9, w, 18, 5, LABEL_BG, LINE, 1)
            + T(cx, cy + size * 0.36, text, size, color, 500, "middle", True))


def chips(x, y, maxw, items, color, size=10, h=20, gap=6, rowgap=26):
    out, cx, cy = [], x, y
    for it in items:
        w = tw(it, size, True) + 16
        if cx + w > x + maxw and cx > x:
            cx, cy = x, cy + rowgap
        out.append(R(cx, cy, w, h, h / 2, color, color, 1, 'fill-opacity=".1" stroke-opacity=".45"')
                   + T(cx + w / 2, cy + h / 2 + size * 0.36, it, size, color, 500, "middle", True))
        cx += w + gap
    return "".join(out)


def label(x, y, s, color=DIM):
    return T(x, y, s, 10.5, color, 700, extra='letter-spacing="1.8"')


def packet(path_id, color, dur, begin=0.0, r=3.6, reverse=False):
    """A dot looping along a path forever."""
    kp = ' keyPoints="1;0" keyTimes="0;1" calcMode="linear"' if reverse else ""
    return (f'<circle r="{r}" fill="{color}" filter="url(#glow)" opacity="0">'
            f'<animateMotion dur="{dur}s" begin="{begin}s" repeatCount="indefinite"{kp}>'
            f'<mpath xlink:href="#{path_id}"/></animateMotion>'
            f'<animate attributeName="opacity" values="0;1;1;0" keyTimes="0;.08;.9;1" dur="{dur}s" '
            f'begin="{begin}s" repeatCount="indefinite"/></circle>')


def shot(path_id, color, per, t0, t1, r=3.8):
    """A dot that travels the path once per loop, between t0 and t1."""
    a, b = t0 / per, t1 / per
    e = min(0.02, (b - a) / 4)
    return (f'<circle r="{r}" fill="{color}" filter="url(#glow)" opacity="0">'
            f'<animateMotion dur="{per}s" repeatCount="indefinite" calcMode="linear" '
            f'keyPoints="0;0;1;1" keyTimes="0;{a:.4f};{b:.4f};1"><mpath xlink:href="#{path_id}"/></animateMotion>'
            f'<animate attributeName="opacity" dur="{per}s" repeatCount="indefinite" values="0;0;1;1;0;0" '
            f'keyTimes="0;{a:.4f};{a + e:.4f};{b - e:.4f};{b:.4f};1"/></circle>')


def hex_pts(cx, cy, r):
    return [(cx + r * math.cos(math.radians(-90 + 60 * k)), cy + r * math.sin(math.radians(-90 + 60 * k)))
            for k in range(6)]


def hex_d(cx, cy, r):
    return "M" + " L".join(f"{f(a)},{f(b)}" for a, b in hex_pts(cx, cy, r)) + " Z"


def brand(x, y):
    """Small logo + wordmark, right-aligned at x."""
    return (P(hex_d(x - 72, y - 4, 8.5), EMBER, 1.5, extra='stroke-linejoin="round"')
            + f'<g transform="translate({f(x - 72)} {f(y - 3)}) scale(.16)">'
            + P(FLAME, "none", 0, EMBER) + "</g>"
            + T(x, y + 1, "forgeops", 12.5, DIM, 650, "end"))


# ── document ────────────────────────────────────────────────────────────────

class SVG:
    def __init__(self, w, h, title, desc):
        self.w, self.h, self.title, self.desc = w, h, title, desc
        self.css: list[str] = []
        self.defs: list[str] = []
        self.body: list[str] = []
        self._n = 0

    def add(self, *parts):
        self.body.extend(parts)

    def anim(self, period, pts, timing="linear", extra=""):
        """Register keyframes [(seconds, css)] on a shared loop; return a style attribute."""
        self._n += 1
        name = f"k{self._n}"
        frames = "".join(f"{max(0.0, min(100.0, t / period * 100)):.3f}%{{{css}}}"
                         for t, css in sorted(pts, key=lambda p: p[0]))
        self.css.append(f"@keyframes {name}{{{frames}}}")
        return f'style="animation:{name} {period}s {timing} infinite;{extra}"'

    def win(self, per, a, b, ramp=0.25, lo="0", hi="1", prop="opacity"):
        """Visible (prop=hi) between a and b seconds of the loop, lo otherwise. a < 0 = visible from the start."""
        if a < 0:
            pts = [(0, f"{prop}:{hi}")]
        else:
            pts = [(0, f"{prop}:{lo}")]
            if a > 0:
                pts.append((a, f"{prop}:{lo}"))
            pts.append((min(a + ramp, per), f"{prop}:{hi}"))
        pts += [(b, f"{prop}:{hi}"), (min(b + ramp, per), f"{prop}:{lo}")]
        if b + ramp < per:
            pts.append((per, f"{prop}:{lo}"))
        return self.anim(per, pts)

    def header(self, eyebrow, title, show_brand=True):
        self.add(T(36, 46, eyebrow, 11, EMBER, 700, extra='letter-spacing="2.2"'))
        self.add(T(36, 75, title, 21, TEXT, 650))
        if show_brand:
            self.add(brand(self.w - 36, 46))

    def render(self, extra_css=""):
        w, h = self.w, self.h
        out = [
            f'<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" xml:space="preserve" '
            f'width="{w}" height="{h}" viewBox="0 0 {w} {h}" role="img" aria-labelledby="title desc">',
            f'<title id="title">{escape(self.title)}</title><desc id="desc">{escape(self.desc)}</desc>',
            "<style>" + BASE_CSS + "".join(self.css) + extra_css + "</style>",
            "<defs>" + SHARED_DEFS
            + f'<clipPath id="frame"><rect x="0" y="0" width="{w}" height="{h}" rx="18"/></clipPath>'
            + "".join(self.defs) + "</defs>",
            '<g clip-path="url(#frame)">',
            R(0, 0, w, h, 18, "url(#bg)"),
            R(0, 0, w, h, 18, "url(#dots)", extra='opacity=".55"'),
            f'<ellipse cx="{f(w * 0.16)}" cy="0" rx="{f(w * 0.55)}" ry="{f(h * 0.6)}" fill="url(#vig)"/>',
            "</g>",
            R(0.5, 0.5, w - 1, h - 1, 18, "none", "#1B2436"),
            *self.body,
            "</svg>\n",
        ]
        return "".join(out)


# ── banner ──────────────────────────────────────────────────────────────────

def banner():
    W, H, PER = 1000, 320, 12.0
    s = SVG(W, H, "ForgeOps: a self-hostable PaaS written in Go",
            "The ForgeOps logo next to a deployment card that moves from queued to building, "
            "deploying and ready, ending on a live HTTPS URL.")
    s.defs += [
        '<linearGradient id="word" x1="0" y1="0" x2="1" y2="0">'
        '<stop offset="0" stop-color="#FFFFFF"/><stop offset=".6" stop-color="#FFEAD5"/>'
        '<stop offset="1" stop-color="#FDBA74"/></linearGradient>',
        '<radialGradient id="halo"><stop offset="0" stop-color="#EA580C" stop-opacity=".40"/>'
        '<stop offset="1" stop-color="#EA580C" stop-opacity="0"/></radialGradient>',
        '<linearGradient id="flame" x1="0" y1="1" x2="0" y2="0">'
        '<stop offset="0" stop-color="#F97316"/><stop offset=".6" stop-color="#FB923C"/>'
        '<stop offset="1" stop-color="#FDE68A"/></linearGradient>',
    ]

    # logo: a hexagon (the sandbox) that draws itself around a flame (the forge)
    cx, cy, r = 112, 160, 80
    per = 6 * r
    s.add(f'<circle cx="{cx}" cy="{cy}" r="128" fill="url(#halo)" class="breathe"/>')
    s.add(P(hex_d(cx, cy, r), EMBER, 1, "#EA580C", 'fill-opacity=".07" stroke-opacity="0"'))
    draw = s.anim(PER, [(0, f"opacity:0;stroke-dashoffset:{per}"), (0.05, f"opacity:1;stroke-dashoffset:{per}"),
                        (1.5, "opacity:1;stroke-dashoffset:0"), (11.2, "opacity:1;stroke-dashoffset:0"),
                        (11.9, f"opacity:1;stroke-dashoffset:{per}"), (PER, f"opacity:0;stroke-dashoffset:{per}")],
                  "ease-in-out")
    s.add(P(hex_d(cx, cy, r), "url(#ember)", 3, extra=f'stroke-linejoin="round" stroke-dasharray="{per}" {draw}'))
    s.add(P(hex_d(cx, cy, r - 12), LINE2, 1, extra='stroke-linejoin="round" stroke-dasharray="2 5"'))
    s.add(f'<g transform="translate({cx} {cy + 4}) scale(1.22)">'
          + P(FLAME, "none", 0, "url(#flame)", 'class="flicker"')
          + P(FLAME_CORE, "none", 0, "#FFF7ED", 'opacity=".85" class="flicker" style="animation-delay:-.9s"')
          + "</g>")

    # wordmark + tagline + chips
    s.add(T(196, 146, "ForgeOps", 66, "url(#word)", 800, extra='letter-spacing="-2"'))
    s.add(T(199, 186, "A self-hostable PaaS, written in Go.", 16.5, MUTED))
    rows = [["git push → live URL", "gVisor sandbox", "1-write rollback"],
            ["SHA-256 build cache", "SSE logs", "auto TLS"]]
    cols = [EMBER, ROSE, GREEN, CYAN, AMBER, BLUE]
    i = 0
    for k, row in enumerate(rows):
        x = 199
        for chip in row:
            w = tw(chip, 11, True) + 22
            s.add(pill(x + w / 2, 230 + k * 32, chip, cols[i]))
            x += w + 8
            i += 1

    # deployment card
    tx, ty, cw, ch = 612, 48, 356, 226
    s.add(R(tx, ty, cw, ch, 14, CARD, LINE2))
    s.add(label(tx + 20, ty + 30, "DEPLOYMENT"))
    A = [0.6, 2.0, 5.2, 7.4]   # when each stage becomes active
    END = 11.2
    stages = [("Queued", DIM), ("Building", AMBER), ("Deploying", CYAN), ("Ready", GREEN)]
    for k, (name, c) in enumerate(stages):
        b = A[k + 1] if k < 3 else END
        st = s.win(PER, A[k], b, 0.15)
        w = tw(name, 10.5, True) + 22
        s.add(pill(tx + cw - 20 - w / 2, ty + 26, name, c, 10.5, 20,
                   extra=f'opacity="{1 if k == 3 else 0}" {st}'))
    s.add(Tp(tx + 20, ty + 60, [("main", EMBER), ("  a1f3c9e  ", MUTED), ("feat: pricing page", TEXT)], 12, mono=True))

    xs = [tx + 46 + k * 88 for k in range(4)]
    ny = ty + 106
    L = xs[-1] - xs[0]
    d = L / 3
    s.add(P(f"M{xs[0]},{ny} H{xs[-1]}", LINE2, 2))
    rail = [(0, f"stroke-dashoffset:{L}"), (A[1] - 0.4, f"stroke-dashoffset:{L}"), (A[1], f"stroke-dashoffset:{f(L - d)}"),
            (A[2] - 0.4, f"stroke-dashoffset:{f(L - d)}"), (A[2], f"stroke-dashoffset:{f(L - 2 * d)}"),
            (A[3] - 0.4, f"stroke-dashoffset:{f(L - 2 * d)}"), (A[3], "stroke-dashoffset:0"),
            (END, "stroke-dashoffset:0"), (END + 0.6, f"stroke-dashoffset:{L}"), (PER, f"stroke-dashoffset:{L}")]
    s.add(P(f"M{xs[0]},{ny} H{xs[-1]}", GREEN, 2, extra=f'stroke-dasharray="{f(L)}" {s.anim(PER, rail, "ease-in-out")}'))
    for k, ((name, c), x) in enumerate(zip(stages, xs)):
        done = A[k + 1] if k < 3 else A[3] + 0.3
        s.add(C(x, ny, 11, "#0B1020", f'stroke="{LINE2}" stroke-width="1.5"'))
        act = s.win(PER, A[k], done, 0.15)
        s.add(f'<g opacity="0" {act}>' + C(x, ny, 11, EMBER, 'class="ping"') + C(x, ny, 11, EMBER) + "</g>")
        ok = s.win(PER, done, END, 0.2)
        s.add(f'<g {ok}>' + C(x, ny, 11, GREEN)
              + P(f"M{x - 4.5},{ny} l3,3.5 l6,-7", "#062B1F", 2.2, extra='stroke-linejoin="round"') + "</g>")
        s.add(T(x, ny + 30, ["Queued", "Build", "Deploy", "Ready"][k], 11, MUTED, 500, "middle"))

    logs = ["› signature ok · build queued", "› cloning a1f3c9e · building in gVisor",
            "› starting container · health check", "› routed · TLS certificate ready"]
    for k, line in enumerate(logs):
        b = A[k + 1] if k < 3 else END
        st = s.win(PER, A[k], b, 0.15)
        s.add(T(tx + 20, ty + 166, line, 11.5, MUTED, 400, mono=True, extra=f'opacity="{1 if k == 3 else 0}" {st}'))

    url = "https://a1f3c9e.forgeops.dev"
    uw = tw(url, 12, True) + 46
    st = s.win(PER, A[3] + 0.2, END, 0.3)
    s.add(f"<g {st}>" + R(tx + 20, ty + 184, uw, 28, 14, GREEN, GREEN, 1, 'fill-opacity=".1" stroke-opacity=".5"')
          + C(tx + 38, ty + 198, 4, GREEN) + C(tx + 38, ty + 198, 4, GREEN, 'class="ping"')
          + T(tx + 52, ty + 202, url, 12, GREEN, 500, mono=True) + "</g>")
    s.add(T(tx + cw / 2, 298, "Illustrative: the target flow (Phase 5)", 10.5, DIM, 400, "middle"))
    return s


# ── architecture ────────────────────────────────────────────────────────────

def architecture():
    W, H = 1000, 792
    s = SVG(W, H, "ForgeOps architecture",
            "GitHub webhooks and API clients reach the control plane, visitors reach the router. "
            "The control plane, executor and router coordinate only through Redis; builds run in "
            "gVisor sandboxes and apps run as tenant containers.")
    s.header("ARCHITECTURE", "Three processes, one repo, coordinated through Redis")
    dash = 'stroke-dasharray="4 5"'

    # outside the host
    s.add(R(32, 100, 616, 108, 14, "#0C1220", LINE2, 1, dash), label(52, 124, "DEVELOPERS"))
    s.add(R(668, 100, 300, 108, 14, "#0C1220", LINE2, 1, dash), label(688, 124, "INTERNET"))
    s.add(card(52, 136, 200, 56, "GitHub", "git push · webhooks", VIOLET),
          card(268, 136, 360, 56, "Dashboard · REST API", "React (Phase 12) · curl · CI", BLUE),
          card(688, 136, 260, 56, "Visitors", "https://<deploy>.forgeops.dev", GREEN))

    s.add(R(32, 232, 936, 504, 14, "#0B1120", LINE2, 1, dash), label(52, 255, "FORGEOPS HOST"))

    # entry edges
    s.defs += ['<path id="aHook" d="M152,192 V270"/>',
               '<path id="aApi" d="M448,192 C448,238 290,224 290,270"/>',
               '<path id="aVisit" d="M818,192 V270"/>']
    for d, c in (("M152,192 V270", VIOLET), ("M448,192 C448,238 290,224 290,270", BLUE), ("M818,192 V270", GREEN)):
        s.add(P(d, LINE2, 1.5), P(d, c, 1.5, extra='class="flow" opacity=".75"'))
    s.add(packet("aHook", VIOLET, 2.2), packet("aApi", BLUE, 2.4, 0.6), packet("aApi", BLUE, 2.4, 1.6, 3, True),
          packet("aVisit", GREEN, 1.6), packet("aVisit", GREEN, 1.6, 0.8))
    s.add(tag(152, 221, "webhook · HMAC-SHA256", VIOLET), tag(372, 221, "REST · SSE", BLUE),
          tag(818, 221, "HTTPS · :443", GREEN))

    # services
    svcs = [(52, 280, "controlplane", ":8080 · chi API", EMBER,
             ["webhooks", "projects", "deploys", "env · secrets", "SSE logs"]),
            (356, 288, "executor", "ops :8081 · build worker", AMBER,
             ["claim", "clone", "detect", "build", "cache"]),
            (668, 280, "router", ":443 proxy · ops :8082", BLUE,
             ["ACME TLS", "host → backend", "Redis only"])]
    for x, w, name, sub, c, items in svcs:
        s.add(R(x, 270, w, 120, 12, CARD, LINE2), R(x, 282, 3, 96, 1.5, c))
        s.add(T(x + 18, 298, name, 16, TEXT, 700), T(x + 18, 316, sub, 11, MUTED, 400, mono=True))
        s.add(chips(x + 18, 330, w - 36, items, c))

    # stores under the control plane
    s.add(card(120, 430, 212, 52, "PostgreSQL 17", "state · :5433", BLUE),
          card(120, 496, 212, 52, "MinIO · S3", "layer cache · :9010", AMBER))
    s.add(P("M226,390 V430", LINE2, 1.5), head(226, 430, "down", LINE2))
    s.add(P("M372,390 V522 H338", LINE2, 1.5), P("M372,456 H338", LINE2, 1.5),
          head(333, 456, "left", LINE2), head(333, 522, "left", LINE2))
    s.defs.append('<path id="aCache" d="M372,390 V522 H334"/>')
    s.add(packet("aCache", AMBER, 2.8, 0.4))

    # build sandboxes
    s.add(R(420, 430, 224, 120, 12, "#160F0A", EMBER, 1, f'stroke-opacity=".45" {dash}'))
    s.add(label(436, 452, "BUILD SANDBOXES", EMBER), T(628, 452, "runsc", 10.5, DIM, 500, "end", True))
    for k, (job, end) in enumerate([("a1f3c9e · acme/web", 3.2), ("7d20b1e · beta/site", 4.4)]):
        y = 470 + k * 30
        s.add(T(436, y + 4, job, 10.5, TEXT, 500, mono=True))
        s.add(R(436, y + 10, 192, 5, 2.5, INK, LINE))
        per = 5.0
        st = s.anim(per, [(0, "transform:scaleX(0)"), (end, "transform:scaleX(1)"), (4.7, "transform:scaleX(1)"),
                          (per, "transform:scaleX(1);opacity:0")], "ease-in-out",
                    "transform-box:fill-box;transform-origin:0 50%")
        s.add(R(436, y + 10, 192, 5, 2.5, EMBER, extra=st))
    s.add(T(436, 538, "no host FS · no internal net · caps", 9.5, DIM, 400, mono=True))
    s.add(P("M532,390 V430", LINE2, 1.5))
    s.defs.append('<path id="aBuild" d="M532,390 V430"/>')
    s.add(P("M532,390 V430", EMBER, 1.5, extra='class="flow" opacity=".8"'), packet("aBuild", EMBER, 1.4, 0.2))

    # tenant deployments
    s.add(R(708, 430, 240, 120, 12, "#0E1B1A", GREEN, 1, f'stroke-opacity=".45" {dash}'))
    s.add(label(724, 452, "TENANT DEPLOYMENTS", GREEN))
    for k, (app, ver) in enumerate([("acme/web", "v3"), ("beta/site", "v2"), ("acme/api", "v7")]):
        y = 476 + k * 24
        s.add(C(732, y - 4, 3.6, GREEN), C(732, y - 4, 3.6, GREEN, f'class="ping" style="animation-delay:{k * 0.7}s"'))
        s.add(T(746, y, app, 11.5, TEXT, 500, mono=True), T(928, y, ver, 11, MUTED, 400, "end", True))
    s.defs.append('<path id="aRoute" d="M828,390 V430"/>')
    s.add(P("M828,390 V430", LINE2, 1.5), P("M828,390 V430", GREEN, 1.5, extra='class="flow" opacity=".8"'),
          packet("aRoute", GREEN, 1.6, 0.4), packet("aRoute", GREEN, 1.6, 1.2))

    # Redis bus + taps
    s.add(R(52, 584, 896, 48, 12, "#0A1A20", CYAN, 1, 'stroke-opacity=".5"'))
    s.add(Tp(500, 613, [("Redis 7", TEXT), ("   build queue · deploy status · router registry · log pub/sub · :6380", MUTED)],
             12, 500, "middle", True))
    for x, txt in [(80, "enqueue · publish"), (396, "claim jobs"), (684, "read registry")]:
        s.add(P(f"M{x},390 V584", LINE2, 1.5), P(f"M{x},390 V584", CYAN, 1.5, extra='class="flow-slow" opacity=".55"'))
        s.add(T(x + 8, 574, txt, 10, CYAN, 500, mono=True, extra='opacity=".8"'))
    s.defs += ['<path id="aJob" d="M80,390 V608 H396 V390"/>', '<path id="aReg" d="M80,390 V608 H684 V390"/>']
    s.add(packet("aJob", VIOLET, 4.2, 0), packet("aJob", VIOLET, 4.2, 2.1),
          packet("aReg", CYAN, 5.2, 1.0))

    # host band
    s.add(R(52, 656, 896, 44, 10, "#170F0A", EMBER, 1, 'stroke-opacity=".3"'))
    s.add(T(72, 683, "HOST", 10.5, EMBER, 700, extra='letter-spacing="1.8"'))
    feats = [("Docker Engine", BLUE), ("gVisor runsc", EMBER), ("cgroup v2", AMBER), ("netns", CYAN),
             ("seccomp", GREEN), ("overlayfs", VIOLET)]
    widths = [tw(n, 11, True) + 20 for n, _ in feats]
    gap = (928 - 140 - sum(widths)) / (len(feats) - 1)
    x, per = 140, len(feats) * 0.8 + 1.2
    for k, ((name, c), w) in enumerate(zip(feats, widths)):
        t0 = k * 0.8
        st = s.anim(per, [(0, "fill-opacity:.08;stroke-opacity:.35"), (t0, "fill-opacity:.08;stroke-opacity:.35"),
                          (t0 + 0.3, "fill-opacity:.34;stroke-opacity:1"), (t0 + 0.8, "fill-opacity:.34;stroke-opacity:1"),
                          (t0 + 1.2, "fill-opacity:.08;stroke-opacity:.35"), (per, "fill-opacity:.08;stroke-opacity:.35")],
                    "ease-in-out")
        s.add(R(x, 667, w, 22, 11, c, c, 1, f'fill-opacity=".08" stroke-opacity=".35" {st}'),
              T(x + w / 2, 682, name, 11, c, 500, "middle", True))
        x += w + gap

    s.add(T(500, 766, "Target architecture. Today (Phase 0) the three services boot, connect to Postgres and Redis, "
                      "and pass health checks.", 11.5, DIM, 400, "middle"))
    return s


# ── today (phase 0) ─────────────────────────────────────────────────────────

def today():
    W, H, PER = 1000, 600, 12.0
    s = SVG(W, H, "What runs today",
            "Phase 0: three services on their ports, the compose stack they depend on, and a Redis "
            "outage that turns /readyz to 503 while /healthz stays 200, then recovers with no restart.")
    s.header("TODAY · PHASE 0", "What runs right now, and what happens when Redis dies")
    OFF, ON = 3.6, 7.6   # Redis is stopped between these two times
    dash = 'stroke-dasharray="4 5"'

    svcs = [(116, "controlplane", ":8080 · chi · needs Postgres + Redis", EMBER, True),
            (240, "executor", "ops :8081 · needs Postgres + Redis · idle until Phase 4", AMBER, True),
            (364, "router", ":8000 proxy (404 for now) · ops :8082 · Redis only", BLUE, False)]
    for y, name, sub, c, _ in svcs:
        s.add(R(36, y, 404, 104, 12, CARD, LINE2), R(36, y + 12, 3, 80, 1.5, c))
        s.add(T(54, y + 28, name, 15, TEXT, 700), T(54, y + 46, sub, 10.5, MUTED, 400, mono=True))
        s.add(pill(54 + 58, y + 76, "/healthz 200", GREEN, 10.5, 22))
        ok = s.win(PER, -1, OFF, 0.2)
        bad = s.win(PER, OFF + 0.4, ON + 0.4, 0.2)
        back = s.win(PER, ON + 0.4, 11.4, 0.2)
        s.add(pill(54 + 186, y + 76, "/readyz 200", GREEN, 10.5, 22, extra=f'opacity="0" {ok}'))
        s.add(pill(54 + 186, y + 76, "/readyz 503", ROSE, 10.5, 22, extra=f'opacity="0" {bad}'))
        s.add(pill(54 + 186, y + 76, "/readyz 200", GREEN, 10.5, 22, extra=back))

    s.add(R(588, 100, 376, 372, 14, "#0C1220", LINE2, 1, dash), label(608, 124, "DOCKER COMPOSE · PROJECT forgeops"))
    s.add(card(608, 140, 336, 64, "PostgreSQL 17", "localhost:5433", BLUE))
    s.add(card(608, 252, 336, 64, "Redis 7", "localhost:6380 · append-only file on", CYAN))
    down = s.win(PER, OFF, ON, 0.2)
    s.add(f'<g opacity="0" {down}>' + R(608, 252, 336, 64, 10, ROSE, ROSE, 1.5, 'fill-opacity=".1"')
          + pill(890, 284, "stopped", ROSE, 10.5, 20) + "</g>")
    s.add(f'<g opacity=".55">' + card(608, 364, 336, 64, "MinIO (S3)", ":9010 API · :9011 console · unused until Phase 7", AMBER) + "</g>")

    # wiring: service → store
    edges = [("M440,158 C520,158 528,172 608,172", 0), ("M440,170 C524,170 524,284 608,284", 1),
             ("M440,282 C520,282 528,180 608,180", 0), ("M440,294 C520,294 528,284 608,284", 1),
             ("M440,406 C524,406 524,292 608,292", 1)]
    for k, (d, to_redis) in enumerate(edges):
        s.add(P(d, LINE2, 1.5))
        if to_redis:
            # the flow class owns the path's animation, so visibility windows go on a wrapping <g>
            s.add(f'<g opacity="0" {s.win(PER, -1, OFF, 0.2)}>' + P(d, CYAN, 1.5, extra='class="flow-slow" opacity=".7"') + "</g>")
            s.add(f'<g {s.win(PER, ON, 11.4, 0.2)}>' + P(d, CYAN, 1.5, extra='class="flow-slow" opacity=".7"') + "</g>")
            s.add(P(d, ROSE, 1.5, extra=f'stroke-dasharray="3 4" opacity="0" {s.win(PER, OFF, ON, 0.2, "0", ".9")}'))
        else:
            s.add(P(d, BLUE, 1.5, extra='class="flow-slow" opacity=".7"'))
    # the edge that is deliberately missing
    s.add(P("M440,394 C540,394 560,214 608,200", ROSE, 1.2, extra='stroke-dasharray="2 5" opacity=".55"'))
    s.add(R(470, 312, 112, 22, 11, LABEL_BG, ROSE, 1, 'stroke-opacity=".5"'),
          T(526, 327, "✕ no DB creds", 10.5, ROSE, 600, "middle", True))

    # timeline
    tx0, tx1, ty = 120, 880, 508
    s.add(T(36, ty + 4, "TIMELINE", 10, DIM, 700, extra='letter-spacing="1.6"'))
    s.add(R(tx0, ty - 3, tx1 - tx0, 6, 3, INK, LINE))
    xo = tx0 + (tx1 - tx0) * OFF / 11.4
    xn = tx0 + (tx1 - tx0) * ON / 11.4
    s.add(R(xo, ty - 3, xn - xo, 6, 3, ROSE, extra='opacity=".55"'))
    for x, txt, c in [((tx0 + xo) / 2, "healthy", GREEN), ((xo + xn) / 2, "docker compose stop redis", ROSE),
                      ((xn + tx1) / 2, "start redis · recovers, no restart", GREEN)]:
        s.add(T(x, ty - 12, txt, 10.5, c, 500, "middle", True))
    head_st = s.anim(PER, [(0, "transform:translateX(0px)"), (11.4, f"transform:translateX({tx1 - tx0}px)"),
                           (PER, f"transform:translateX({tx1 - tx0}px)")])
    s.add(f'<g {head_st}>' + C(tx0, ty, 6, TEXT, 'filter="url(#glow)"') + "</g>")

    # the real probe output (captured in Phase 0)
    s.add(R(36, 532, 928, 48, 10, INK, LINE))
    okline = [("$ curl :8082/readyz  ", MUTED), ("200 ", GREEN), ('{"status":"ready","service":"router","checks":{"redis":"ok"}}', TEXT)]
    badline = [("$ curl :8082/readyz  ", MUTED), ("503 ", ROSE), ('{"status":"not_ready","service":"router","checks":{"redis":"fail"}}', TEXT)]
    s.add(Tp(56, 561, okline, 12, mono=True, extra=f'opacity="1" {s.win(PER, -1, OFF + 0.4, 0.15)}'))
    s.add(Tp(56, 561, badline, 12, mono=True, extra=f'opacity="0" {s.win(PER, OFF + 0.4, ON + 0.4, 0.15)}'))
    s.add(Tp(56, 561, okline, 12, mono=True, extra=f'opacity="0" {s.win(PER, ON + 0.4, 11.6, 0.15)}'))
    return s


# ── push to live ────────────────────────────────────────────────────────────

def push_to_live():
    W, H, PER = 1000, 720, 16.0
    s = SVG(W, H, "From git push to a live URL",
            "Sequence: GitHub sends a signed webhook, the control plane verifies and enqueues a build, the "
            "executor claims and builds it in gVisor, the control plane starts the container and writes the "
            "route, and the router picks it up over pub/sub.")
    s.header("PUSH TO LIVE · PHASES 3–5", "From git push to a live URL")
    fade = [(15.0, "opacity:1"), (15.6, "opacity:0"), (PER, "opacity:0")]

    lanes = [(100, "GitHub", "repo · webhooks", VIOLET), (260, "controlplane", "API · deploys", EMBER),
             (420, "Redis", "queue · pub/sub", CYAN), (580, "executor", "build worker", AMBER),
             (740, "tenant container", "your app", GREEN), (900, "router", "edge proxy", BLUE)]
    for x, name, sub, c in lanes:
        s.add(P(f"M{x},154 V640", LINE2, 1.2, extra='stroke-dasharray="3 5"'))
        s.add(R(x - 70, 104, 140, 50, 10, CARD, LINE), R(x - 40, 104, 80, 2.5, 1.25, c),
              T(x, 126, name, 13, TEXT, 600, "middle"), T(x, 143, sub, 10.5, MUTED, 400, "middle", True))
    X = {k: lanes[i][0] for i, k in enumerate(["gh", "cp", "rd", "ex", "ct", "rt"])}
    COL = {k: lanes[i][3] for i, k in enumerate(["gh", "cp", "rd", "ex", "ct", "rt"])}

    msgs = [("arrow", "gh", "cp", "push event + HMAC sig"),
            ("note", "cp", "verify HMAC-SHA256 · bad → 401", ROSE),
            ("arrow", "cp", "rd", "enqueue(project, sha)"),
            ("arrow", "ex", "rd", "claim job (atomic)"),
            ("note", "ex", "clone sha · detect · build in gVisor", EMBER),
            ("arrow", "ex", "rd", "status + log lines"),
            ("arrow", "rd", "cp", "build ok · image ref"),
            ("arrow", "cp", "ct", "run image · health check"),
            ("arrow", "cp", "rd", "set host → backend"),
            ("arrow", "rd", "rt", "pub/sub: invalidate"),
            ("arrow", "rt", "ct", "proxy request")]
    for k, m in enumerate(msgs):
        y = 192 + k * 42
        t = 0.5 + k * 1.2
        if m[0] == "note":
            _, lane, text, c = m
            w = tw(text, 10.5, True) + 22
            st = s.anim(PER, [(0, "opacity:0"), (t, "opacity:0"), (t + 0.35, "opacity:1")] + fade)
            s.add(f"<g {st}>" + R(X[lane] - w / 2, y - 13, w, 26, 7, CARD2, c, 1, 'stroke-opacity=".6"')
                  + T(X[lane], y + 4, text, 10.5, c, 500, "middle", True) + "</g>")
            continue
        _, a, b, text = m
        x1, x2 = X[a], X[b]
        d = 1 if x2 > x1 else -1
        xs, xe = x1 + d * 5, x2 - d * 5
        ln = abs(xe - d * 6 - xs)
        c = COL[a]
        st = s.anim(PER, [(0, f"stroke-dashoffset:{ln}"), (t, f"stroke-dashoffset:{ln}"), (t + 0.5, "stroke-dashoffset:0"),
                          (15.0, "stroke-dashoffset:0"), (15.6, f"stroke-dashoffset:{ln}"), (PER, f"stroke-dashoffset:{ln}")],
                    "ease-in-out")
        hst = s.anim(PER, [(0, "opacity:0"), (t + 0.45, "opacity:0"), (t + 0.55, "opacity:1")] + fade)
        lst = s.anim(PER, [(0, "opacity:0"), (t + 0.1, "opacity:0"), (t + 0.4, "opacity:1")] + fade)
        s.add(f"<g {lst}>" + C(x1, y, 3.2, c) + "</g>")
        s.add(P(f"M{xs},{y} H{xe - d * 6}", c, 1.6, extra=f'stroke-dasharray="{ln}" {st}'),
              f"<g {hst}>" + head(xe, y, "right" if d > 0 else "left", c) + "</g>")
        s.add(f"<g {lst}>" + tag((x1 + x2) / 2, y - 12, text, MUTED if b == "rd" or a == "rd" else TEXT) + "</g>")

    msg = "live at https://a1f3c9e.forgeops.dev · the router never restarted"
    w = tw(msg, 12, True) + 52
    st = s.anim(PER, [(0, "opacity:0"), (13.8, "opacity:0"), (14.1, "opacity:1")] + fade)
    s.add(f"<g {st}>" + R(500 - w / 2, 660, w, 32, 16, GREEN, GREEN, 1, 'fill-opacity=".1" stroke-opacity=".5"')
          + C(500 - w / 2 + 20, 676, 4, GREEN) + T(500 - w / 2 + 34, 680, msg, 12, GREEN, 500, mono=True) + "</g>")
    return s


# ── security model ──────────────────────────────────────────────────────────

def isolation():
    W, H, PER = 1000, 664, 14.0
    s = SVG(W, H, "ForgeOps security model",
            "A hostile build sits inside four rings: the gVisor Sentry, cgroup limits, a network policy and "
            "the host kernel. Seven escape attempts each hit the ring that contains them.")
    s.header("SECURITY MODEL · PHASE 6", "Assume every build is hostile. Contain it anyway.")

    rings = {"host": (36, 112, 440, 500, "HOST KERNEL", VIOLET),
             "net": (60, 150, 392, 436, "NETWORK POLICY", BLUE),
             "cg": (84, 188, 344, 360, "CGROUP LIMITS", AMBER),
             "gv": (108, 226, 296, 284, "gVISOR SENTRY", EMBER)}
    attacks = [("Read host files (/etc/shadow, docker.sock)", "no host mounts · FS goes through the gofer", "gv", (180, 226)),
               ("Call the control plane, Redis, Postgres", "egress to internal ranges denied", "net", (330, 150)),
               ("Reach another tenant's container", "no east-west traffic between sandboxes", "net", (170, 586)),
               ("Fork bomb", "pids.max caps the process count", "cg", (350, 548)),
               ("Allocate 10 GB of RAM", "memory.max → OOM-killed, exit 137", "cg", (84, 300)),
               ("Never exit", "build timeout → container killed", "cg", (428, 300)),
               ("Exploit a kernel syscall bug", "syscalls served by the Sentry, not the host", "gv", (404, 430))]
    times = [0.5 + i * 1.85 for i in range(len(attacks))]

    for key, (x, y, w, h, name, c) in rings.items():
        hits = [times[i] for i, a in enumerate(attacks) if a[2] == key]
        pts = [(0, "stroke-opacity:.45")]
        for t in hits:
            pts += [(t + 0.55, "stroke-opacity:.45"), (t + 0.7, "stroke-opacity:1"), (t + 1.5, "stroke-opacity:.45")]
        pts.append((PER, "stroke-opacity:.45"))
        s.add(R(x, y, w, h, 16, c, c, 1.5,
                f'fill-opacity=".035" stroke-opacity=".45" {s.anim(PER, pts)}'))
        s.add(T(x + 16, y + 22, name, 10.5, c, 700, extra='letter-spacing="1.8"'))

    cx, cy = 256, 372
    for i, (_, _, key, (px, py)) in enumerate(attacks):
        t = times[i]
        pid = f"atk{i}"
        s.defs.append(f'<path id="{pid}" d="M{cx},{cy} L{px},{py}"/>')
        s.add(P(f"M{cx},{cy} L{px},{py}", ROSE, 1, extra=f'stroke-dasharray="2 4" opacity=".18"'))
        s.add(shot(pid, ROSE, PER, t, t + 0.6))
        st = s.win(PER, t + 0.55, t + 1.6, 0.1)
        s.add(f'<g {st}>' + C(px, py, 9, ROSE, 'class="ping"') + C(px, py, 9, "#2A0F16", f'stroke="{ROSE}" stroke-width="1.5"')
              + P(f"M{px - 3.5},{py - 3.5} l7,7 M{px + 3.5},{py - 3.5} l-7,7", ROSE, 1.8) + "</g>")

    s.add(R(cx - 100, cy - 36, 200, 72, 12, "#1F0D12", ROSE, 1.2, 'stroke-opacity=".7"'))
    s.add(T(cx, cy - 8, "untrusted build", 14, TEXT, 650, "middle"),
          T(cx, cy + 12, "npm install · node build.js", 10.5, MUTED, 400, "middle", True),
          C(cx - 68, cy - 12.5, 3.5, ROSE, 'class="blink"'))

    s.add(label(500, 124, "ESCAPE SUITE · A DELIBERATELY HOSTILE REPO", ROSE))
    for i, (title, how, key, _) in enumerate(attacks):
        y = 138 + i * 64
        t = times[i]
        c = rings[key][5]
        s.add(R(500, y, 464, 56, 10, CARD, LINE))
        s.add(R(500, y, 464, 56, 10, "#1F0D12", ROSE, 1, f'stroke-opacity=".7" opacity="0" {s.win(PER, t, t + 1.6, 0.15)}'))
        s.add(C(524, y + 28, 12, "#0B1020", f'stroke="{LINE2}" stroke-width="1.5"'),
              T(524, y + 32.5, str(i + 1), 11.5, MUTED, 700, "middle"))
        s.add(T(546, y + 24, title, 13, TEXT, 600), Tp(546, y + 42, [("→ ", DIM), (how, c)], 10.5, mono=True))
        s.add(pill(924, y + 28, "planned", DIM, 9.5, 18))

    s.add(T(500, 640, "Target: 7/7 contained, each one a test against a hostile repo (Phase 6). Nothing is measured yet.",
            11.5, DIM, 400, "middle"))
    return s


# ── routing + rollback ──────────────────────────────────────────────────────

def routing():
    W, H, PER = 1000, 600, 12.0
    s = SVG(W, H, "Routing and instant rollback",
            "Requests reach the router, which terminates TLS, looks the host up in an in-memory map backed "
            "by Redis, and proxies to the current deployment. A rollback writes one registry entry and "
            "publishes an invalidation; traffic moves from v3 to v2 with no rebuild.")
    s.header("ROUTING · PHASE 5", "Immutable deployments, one-write rollback")
    CLICK, WRITE, PUB, FLIP, RESET = 5.0, 5.6, 6.1, 6.5, 11.3

    s.add(card(36, 150, 172, 60, "Visitor", "acme-web.forgeops.dev", GREEN))
    s.add(R(250, 112, 330, 318, 14, CARD, LINE2), R(250, 124, 3, 294, 1.5, BLUE))
    s.add(T(270, 140, "router", 16, TEXT, 700), T(338, 140, ":443", 11, MUTED, 400, mono=True))
    steps = [("TLS termination", "autocert · a certificate per host (SNI)"),
             ("Host lookup", "in-memory map · filled from Redis"),
             ("Reverse proxy", "httputil.ReverseProxy · streams bodies")]
    for k, (a, b) in enumerate(steps):
        y = 158 + k * 58
        s.add(R(270, y, 290, 48, 9, CARD2, LINE))
        s.add(C(290, y + 24, 10, "#0B1020", f'stroke="{BLUE}" stroke-width="1.2"'),
              T(290, y + 28, str(k + 1), 10.5, BLUE, 700, "middle"))
        s.add(T(310, y + 20, a, 12.5, TEXT, 600), T(310, y + 37, b, 10, MUTED, 400, mono=True))
    s.add(R(270, 340, 290, 70, 9, INK, LINE))
    s.add(T(286, 360, "LOCAL MAP", 9.5, DIM, 700, extra='letter-spacing="1.6"'))
    s.add(T(286, 386, "acme-web.forgeops.dev", 11.5, TEXT, 500, mono=True))
    s.add(T(286 + tw("acme-web.forgeops.dev  ", 11.5, True), 386, "→ v3", 11.5, EMBER, 600, mono=True,
            extra=f'opacity="0" {s.win(PER, -1, FLIP, 0.12)}'))
    s.add(T(286 + tw("acme-web.forgeops.dev  ", 11.5, True), 386, "→ v2", 11.5, GREEN, 600, mono=True,
            extra=s.win(PER, FLIP, RESET, 0.12)))
    s.add(R(270, 340, 290, 70, 9, "none", GREEN, 1.5, f'opacity="0" {s.win(PER, FLIP - 0.1, FLIP + 0.9, 0.15)}'))
    s.add(T(286, 402, "rebuilt on every invalidation", 9.5, DIM, 400, mono=True))

    s.add(R(250, 470, 330, 64, 12, "#0A1A20", CYAN, 1, 'stroke-opacity=".5"'))
    s.add(T(270, 496, "Redis registry", 13.5, TEXT, 600), T(270, 514, "host → deployment · the source of truth", 10.5, MUTED, 400, mono=True))
    s.add(R(250, 470, 330, 64, 12, "none", CYAN, 1.5, f'opacity="0" {s.win(PER, WRITE + 0.3, PUB + 0.4, 0.12)}'))
    s.defs.append('<path id="rPub" d="M415,470 V430"/>')
    s.add(P("M415,470 V436", CYAN, 1.5, extra='stroke-dasharray="3 3"'), head(415, 432, "up", CYAN))
    s.add(T(424, 456, "pub/sub", 10, CYAN, 500, mono=True))
    s.add(shot("rPub", CYAN, PER, PUB, FLIP))

    s.add(label(640, 132, "DEPLOYMENTS · IMMUTABLE"))
    deps = [(148, "v3", "9c1e2f0 · fix checkout bug", "running"), (236, "v2", "4b7a8d1 · new pricing page", "running"),
            (324, "v1", "e02f6c9 · initial import", "image kept")]
    for y, v, msg, state in deps:
        s.add(R(640, y, 324, 72, 12, CARD, LINE))
        s.add(T(660, y + 30, v, 18, TEXT, 700), T(694, y + 30, msg, 11, MUTED, 400, mono=True))
        c = GREEN if state == "running" else DIM
        s.add(C(666, y + 52, 3.5, c), T(678, y + 56, state, 10.5, c, 500, mono=True))
    # "current" badge: on v3, then on v2
    s.add(R(640, 148, 324, 72, 12, "none", EMBER, 1.5, f'opacity="0" {s.win(PER, -1, FLIP, 0.2)}'),
          pill(914, 172, "current", EMBER, 10, 20, extra=f'opacity="0" {s.win(PER, -1, FLIP, 0.2)}'))
    s.add(R(640, 236, 324, 72, 12, "none", GREEN, 1.5, s.win(PER, FLIP, RESET, 0.2)),
          pill(914, 260, "current", GREEN, 10, 20, extra=s.win(PER, FLIP, RESET, 0.2)))

    s.add(R(640, 440, 324, 94, 12, CARD, LINE))
    s.add(T(660, 466, "dashboard / API", 12.5, TEXT, 600), T(660, 484, "roll back acme-web to v2", 10.5, MUTED, 400, mono=True))
    s.add(R(660, 496, 128, 26, 13, ROSE, ROSE, 1, 'fill-opacity=".1" stroke-opacity=".6"'),
          T(724, 513, "Rollback", 11.5, ROSE, 600, "middle"))
    s.add(R(660, 496, 128, 26, 13, ROSE, extra=f'opacity="0" {s.win(PER, CLICK, CLICK + 0.5, 0.1, "0", ".45")}'))
    s.add(T(804, 513, "no rebuild", 10.5, DIM, 400, mono=True))

    # traffic
    s.defs += ['<path id="rIn" d="M208,180 H250"/>',
               '<path id="r3" d="M580,262 C612,262 606,184 640,184"/>',
               '<path id="r2" d="M580,262 C612,262 610,272 640,272"/>',
               '<path id="rRb" d="M640,484 C606,484 612,502 580,502"/>']
    s.add(P("M208,180 H250", GREEN, 1.5, extra='class="flow"'))
    s.add(P("M580,262 C612,262 606,184 640,184", LINE2, 1.5), P("M580,262 C612,262 610,272 640,272", LINE2, 1.5))
    s.add(f'<g opacity="0" {s.win(PER, -1, FLIP, 0.2)}>' + P("M580,262 C612,262 606,184 640,184", EMBER, 1.5, extra='class="flow"') + "</g>")
    s.add(f'<g {s.win(PER, FLIP, RESET, 0.2)}>' + P("M580,262 C612,262 610,272 640,272", GREEN, 1.5, extra='class="flow"') + "</g>")
    s.add(P("M640,484 C606,484 612,502 580,502", ROSE, 1.3, extra='stroke-dasharray="3 3" opacity=".7"'))
    s.add(packet("rIn", GREEN, 1.2), packet("rIn", GREEN, 1.2, 0.6))
    s.add(f'<g {s.win(PER, -1, FLIP, 0.2)}>' + packet("r3", EMBER, 1.2) + packet("r3", EMBER, 1.2, 0.6) + "</g>")
    s.add(f'<g opacity="0" {s.win(PER, FLIP, RESET, 0.2)}>' + packet("r2", GREEN, 1.2) + packet("r2", GREEN, 1.2, 0.6) + "</g>")
    s.add(shot("rRb", ROSE, PER, WRITE, WRITE + 0.5))

    s.add(T(500, 574, "Rollback is one registry write plus one pub/sub message: no rebuild, no redeploy, no proxy restart.",
            11.5, DIM, 400, "middle"))
    return s


# ── build cache ─────────────────────────────────────────────────────────────

def build_cache():
    W, H, PER = 1000, 620, 14.0
    s = SVG(W, H, "Content-addressed build cache",
            "Three tenants build nine layers. Each layer is hashed with SHA-256; a layer whose hash already "
            "exists only gains a reference, so nine layers become six stored blobs.")
    s.header("BUILD CACHE · PHASE 7", "Identical layers are stored once, across every tenant")
    END = 13.2

    tenants = [("acme/web", "lockfile 7e3a…", [("node:22-alpine", "4f2a91", BLUE), ("node_modules", "9b7e05", CYAN), ("app source", "a0d4c2", GREEN)]),
               ("beta/site", "lockfile 7e3a…", [("node:22-alpine", "4f2a91", BLUE), ("node_modules", "9b7e05", CYAN), ("app source", "51fe8b", GREEN)]),
               ("gamma/api", "lockfile c41d…", [("node:22-alpine", "4f2a91", BLUE), ("node_modules", "e81c3d", CYAN), ("app source", "c77b10", GREEN)])]
    store = [("4f2a91", "node:22-alpine base"), ("9b7e05", "node_modules · lockfile 7e3a"), ("a0d4c2", "acme/web source"),
             ("51fe8b", "beta/site source"), ("e81c3d", "node_modules · lockfile c41d"), ("c77b10", "gamma/api source")]
    row_of = {dg: r for r, (dg, _) in enumerate(store)}

    s.add(label(600, 132, "BLOBS/SHA256/ · CONTENT-ADDRESSED", GREEN))
    ry = lambda r: 146 + r * 56  # noqa: E731
    seen: dict[str, list[float]] = {}
    order = []
    for ti, (_, _, layers) in enumerate(tenants):
        for li, (_, dg, _) in enumerate(layers):
            order.append((ti, li, dg))
    times = [0.5 + i * 1.3 for i in range(len(order))]
    for i, (_, _, dg) in enumerate(order):
        seen.setdefault(dg, []).append(times[i] + 0.7)

    for ti, (name, lock, layers) in enumerate(tenants):
        y0 = 112 + ti * 152
        s.add(T(36, y0 + 14, name, 13.5, TEXT, 650), T(416, y0 + 14, lock, 10.5, DIM, 400, "end", True))
        for li, (lname, dg, c) in enumerate(layers):
            ly = y0 + 26 + li * 36
            i = order.index((ti, li, dg))
            t = times[i]
            s.add(R(36, ly, 380, 30, 7, c, c, 1, 'fill-opacity=".08" stroke-opacity=".4"'))
            s.add(R(36, ly, 380, 30, 7, "none", EMBER, 1.5, f'opacity="0" {s.win(PER, t - 0.1, t + 0.8, 0.12)}'))
            s.add(T(52, ly + 19, lname, 12, TEXT, 500), T(402, ly + 19, f"sha256:{dg}…", 11, c, 500, "end", True))
            pid = f"lay{i}"
            r = row_of[dg]
            s.defs.append(f'<path id="{pid}" d="M416,{ly + 15} C510,{ly + 15} 506,{ry(r) + 23} 600,{ry(r) + 23}"/>')
            s.add(P(f"M416,{ly + 15} C510,{ly + 15} 506,{ry(r) + 23} 600,{ry(r) + 23}", LINE2, 1, extra='opacity=".6"'))
            s.add(shot(pid, EMBER, PER, t, t + 0.7, 3.4))

    s.add(pill(508, 352, "sha256(layer)", EMBER, 10.5, 22))

    for r, (dg, desc) in enumerate(store):
        y = ry(r)
        arrivals = seen[dg]
        born = arrivals[0]
        s.add(f'<g {s.win(PER, born - 0.05, END, 0.25)}>')
        s.add(R(600, y, 364, 46, 9, CARD, LINE))
        s.add(T(618, y + 20, f"sha256:{dg}…", 12, TEXT, 600, mono=True), T(618, y + 37, desc, 10.5, MUTED))
        for k, t in enumerate(arrivals):
            b = arrivals[k + 1] if k + 1 < len(arrivals) else END
            last = k == len(arrivals) - 1
            st = s.win(PER, t, b, 0.05)
            s.add(T(946, y + 28, f"refs {k + 1}", 12, GREEN if k else MUTED, 600, "end", True,
                    extra=f'opacity="{1 if last else 0}" {st}'))
            if k:
                s.add(R(600, y, 364, 46, 9, "none", GREEN, 1.5, f'opacity="0" {s.win(PER, t, t + 0.9, 0.1)}'))
                s.add(T(880, y + 28, "dedup hit", 10, GREEN, 600, "end", True, extra=f'opacity="0" {s.win(PER, t, t + 0.9, 0.1)}'))
        s.add("</g>")

    st = s.win(PER, 12.0, END, 0.3)
    s.add(f"<g {st}>" + R(600, 488, 364, 64, 12, GREEN, GREEN, 1, 'fill-opacity=".08" stroke-opacity=".45"')
          + T(620, 514, "9 layers in → 6 blobs stored", 14, TEXT, 650)
          + T(620, 534, "the base image is written once, shared three ways", 10.5, MUTED) + "</g>")
    s.add(T(500, 596, "Engine reused from HashVault: SHA-256 content addressing + reference counting. "
                      "Cold vs warm build times are measured in Phase 7.", 11.5, DIM, 400, "middle"))
    return s


# ── log streaming ───────────────────────────────────────────────────────────

def logs():
    W, H, PER = 1000, 480, 8.0
    s = SVG(W, H, "Live log streaming",
            "The executor publishes log lines to a Redis channel; every control-plane replica subscribed to it "
            "streams the lines to its viewers over Server-Sent Events.")
    s.header("LOG STREAMING · PHASE 8", "One build log, many viewers, any replica")

    s.add(card(36, 148, 196, 64, "executor", "build a1f3c9e", AMBER))
    s.add(card(288, 148, 212, 64, "Redis pub/sub", "channel logs:a1f3c9e", CYAN))
    s.add(card(560, 108, 184, 60, "controlplane #1", "SSE · http.Flusher", EMBER))
    s.add(card(560, 196, 184, 60, "controlplane #2", "SSE · http.Flusher", EMBER))
    for y, name in [(96, "Dashboard"), (152, "curl -N"), (228, "Dashboard · tab 2")]:
        s.add(card(800, y, 164, 44, name, None, BLUE, 12.5))
    paths = [("lA", "M232,180 H288", AMBER), ("lB", "M500,180 C530,180 530,138 560,138", CYAN),
             ("lC", "M500,180 C530,180 530,226 560,226", CYAN), ("lD", "M744,138 C772,138 772,118 800,118", EMBER),
             ("lE", "M744,138 C772,138 772,174 800,174", EMBER), ("lF", "M744,226 C772,226 772,250 800,250", EMBER)]
    for pid, d, c in paths:
        s.defs.append(f'<path id="{pid}" d="{d}"/>')
        s.add(P(d, LINE2, 1.5), P(d, c, 1.5, extra='class="flow" opacity=".7"'))
        s.add(packet(pid, c, 1.6), packet(pid, c, 1.6, 0.8))
    s.add(T(500, 290, "text/event-stream, one direction. Pub/sub means a viewer can land on any replica.",
            11.5, DIM, 400, "middle"))

    s.add(R(36, 314, 928, 140, 12, INK, LINE2))
    s.add(C(58, 336, 4, ROSE, 'class="blink"'), T(70, 340, "build a1f3c9e · live", 11, MUTED, 500, mono=True))
    s.add(T(944, 340, "illustrative output", 10, DIM, 400, "end"))
    s.add(P("M36,352 H964", LINE, 1))
    lines = [[("[clone]   ", AMBER), ("a1f3c9e from github.com/acme/web", TEXT)],
             [("[detect]  ", AMBER), ("node · package.json", TEXT)],
             [("[build]   ", AMBER), ("npm ci && npm run build   ", TEXT), ("(inside gVisor)", DIM)],
             [("[image]   ", AMBER), ("2 layers cached · 1 new", TEXT)],
             [("[deploy]  ", GREEN), ("health check passed · routed", TEXT)]]
    for k, parts in enumerate(lines):
        t = 0.6 + k * 1.2
        st = s.anim(PER, [(0, "opacity:0"), (t, "opacity:0"), (t + 0.15, "opacity:1"), (7.2, "opacity:1"),
                          (7.7, "opacity:0"), (PER, "opacity:0")])
        s.add(Tp(56, 374 + k * 17, parts, 11.5, mono=True, extra=st))
    return s


# ── roadmap ─────────────────────────────────────────────────────────────────

PHASES = ["Foundation", "Domain model", "API + tenancy", "Git + HMAC", "Build executor", "Deploy + router",
          "gVisor sandbox", "Build cache", "Log streaming", "Quotas + domains", "Observability",
          "Load + chaos", "Dashboard", "Ship + docs"]
CENTREPIECE = 6


def roadmap():
    W, H = 1000, 350
    s = SVG(W, H, "ForgeOps roadmap", f"Fourteen phases from foundation to a self-hostable release; "
                                       f"phases below {CURRENT_PHASE} are done, phase {CURRENT_PHASE} is next, "
                                       f"phase {CENTREPIECE} (gVisor isolation) is the centrepiece.")
    s.header("ROADMAP", "Fourteen phases to a demo-ready PaaS, January 2027", show_brand=False)
    status = f"Phase {CURRENT_PHASE - 1} done · Phase {CURRENT_PHASE} next"
    s.add(pill(W - 36 - (tw(status, 11, True) + 22) / 2, 68, status, EMBER))

    y1, y2 = 160, 270
    xs1 = [96 + k * 134 for k in range(7)]
    pos = [(x, y1) for x in xs1] + [(x, y2) for x in reversed(xs1)]
    road = f"M96,{y1} H900 C956,{y1} 956,{y2} 900,{y2} H96"
    s.add(P(road, LINE2, 2), P(road, DIM, 2, extra='class="flow-slow" opacity=".6"'))
    if CURRENT_PHASE > 0:
        x, y = pos[CURRENT_PHASE]
        done = f"M96,{y1} H{x}" if y == y1 else f"M96,{y1} H900 C956,{y1} 956,{y2} 900,{y2} H{x}"
        s.add(P(done, GREEN, 2.5))
    for k, ((x, y), name) in enumerate(zip(pos, PHASES)):
        if k == CENTREPIECE:
            s.add(C(x, y, 22, "none", f'stroke="{EMBER}" stroke-width="1.2" stroke-dasharray="3 3" class="breathe"'))
        if k < CURRENT_PHASE:
            s.add(C(x, y, 15, GREEN), P(f"M{x - 6},{y} l4,4.5 l8,-9", "#062B1F", 2.4, extra='stroke-linejoin="round"'))
            tc = MUTED
        elif k == CURRENT_PHASE:
            s.add(C(x, y, 15, EMBER, 'class="ping"'), C(x, y, 15, EMBER, 'filter="url(#glow)"'),
                  T(x, y + 4.5, str(k), 12, "#2A1204", 800, "middle"))
            tc = TEXT
        else:
            s.add(C(x, y, 15, "#0B1020", f'stroke="{EMBER if k == CENTREPIECE else LINE2}" stroke-width="1.5"'),
                  T(x, y + 4.5, str(k), 12, EMBER if k == CENTREPIECE else MUTED, 700, "middle"))
            tc = MUTED
        s.add(T(x, y + 40, name, 11.5, tc, 600 if k == CURRENT_PHASE else 500, "middle"))
        if k == CENTREPIECE:
            s.add(T(x, y + 56, "centrepiece", 10, EMBER, 600, "middle", extra='letter-spacing="1"'))
    return s


DIAGRAMS = [("banner", banner), ("architecture", architecture), ("today", today), ("push-to-live", push_to_live),
            ("isolation", isolation), ("routing", routing), ("build-cache", build_cache), ("logs", logs),
            ("roadmap", roadmap)]


def main():
    if len(sys.argv) == 4 and sys.argv[1] == "--freeze":
        # Debug aid: every CSS animation paused at t seconds (SMIL dots still move).
        t, out = float(sys.argv[2]), Path(sys.argv[3])
        css = f"*{{animation-delay:-{t}s!important;animation-play-state:paused!important;}}"
        out.mkdir(parents=True, exist_ok=True)
        for name, fn in DIAGRAMS:
            (out / f"{name}.svg").write_text(fn().render(css), encoding="utf-8", newline="\n")
        print(f"wrote {len(DIAGRAMS)} frozen diagrams to {out}")
        return
    OUT.mkdir(parents=True, exist_ok=True)
    for name, fn in DIAGRAMS:
        path = OUT / f"{name}.svg"
        path.write_text(fn().render(), encoding="utf-8", newline="\n")
        print(f"wrote {path.relative_to(OUT.parents[1])}  ({path.stat().st_size / 1024:.1f} KB)")


if __name__ == "__main__":
    main()
