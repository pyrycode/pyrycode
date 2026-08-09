#!/usr/bin/env python3
"""Independent cite sweep for #1417 PR #1423.

For every cite in every package file, resolve it to TARGET CONTENT at base and at
HEAD and compare. Three cite forms:

  A explicit   file.go:N        / file.go:N-M
  B bare       (:N)             / (:N-M)      -> carry: last .go file named to the left
  C symbol     Symbol:N-M                     -> file where Symbol is defined

Sites are paired base<->head with difflib over digit-normalised lines, so a cite
whose NUMBER changed still pairs with its base self.
"""
import re, subprocess, sys, difflib, os, glob

BASE = "93b2018"
PKG = "internal/e2e/realclaude"
REPO = os.path.abspath(os.path.join(os.path.dirname(__file__)))

def sh(*a):
    return subprocess.run(a, capture_output=True, text=True, cwd=REPO).stdout

def base_file(path):
    r = subprocess.run(["git", "show", f"{BASE}:{path}"], capture_output=True, text=True, cwd=REPO)
    return r.stdout.split("\n") if r.returncode == 0 else None

def head_file(path):
    p = os.path.join(REPO, path)
    if not os.path.exists(p):
        return None
    with open(p) as f:
        return f.read().split("\n")

FILES = sorted(os.path.basename(p) for p in glob.glob(os.path.join(REPO, PKG, "*.go")))

basecache, headcache = {}, {}
def get(cache, loader, name):
    if name not in cache:
        cache[name] = loader(f"{PKG}/{name}")
    return cache[name]

# ---- symbol -> defining file (at a given tree) -------------------------------
def build_symtab(loader, cache):
    tab = {}
    for fn in FILES:
        lines = get(cache, loader, fn)
        if lines is None:
            continue
        for ln in lines:
            m = re.match(r'^(?:type|func)\s+(?:\([^)]*\)\s*)?([A-Za-z_]\w*)', ln)
            if m:
                tab.setdefault(m.group(1), fn)
            m = re.match(r'^\s*([A-Za-z_]\w*)\s*=\s*"', ln)
            if m:
                tab.setdefault(m.group(1), fn)
    return tab

CITE = re.compile(r'([A-Za-z_][\w.]*\.go|[A-Za-z_]\w*)?:(\d+)(?:-(\d+))?')

def extract(lines, symtab, fname):
    """yield (lineidx, order, rawtext, targetfile, n, m, form)"""
    out = []
    carry = None
    for i, ln in enumerate(lines):
        if not ln.lstrip().startswith("//") and "//" not in ln:
            # still track carry inside code comments only
            pass
        order = 0
        for m in CITE.finditer(ln):
            name, n, mm = m.group(1), int(m.group(2)), m.group(3)
            mm = int(mm) if mm else None
            # skip things that are obviously not cites (times, http, etc.)
            pre = ln[:m.start()]
            if name and name.endswith(".go"):
                carry = name
                tf, form = name, "A"
            elif name:
                if name in symtab:
                    tf, form = symtab[name], "C"
                else:
                    continue
            else:
                # bare - must look like a cite: preceded by ( or , or space
                if not re.search(r'[(,]\s*$|\s$', pre):
                    continue
                if carry is None:
                    continue
                tf, form = carry, "B"
            out.append((i, order, ln.strip(), tf, n, mm, form))
            order += 1
    return out

def content(cache, loader, tf, n, mm):
    lines = get(cache, loader, tf)
    if lines is None:
        return None
    lo, hi = n, (mm if mm else n)
    if lo < 1 or hi > len(lines):
        return "<OOB>"
    seg = "\n".join(lines[lo-1:hi])
    return re.sub(r'\d+', '#', seg).strip()

def norm(s):
    return re.sub(r'\d+', '#', s)

bsym = build_symtab(base_file, basecache)
hsym = build_symtab(head_file, headcache)

problems = []
for fn in FILES:
    bl = get(basecache, base_file, fn)
    hl = get(headcache, head_file, fn)
    if bl is None or hl is None:
        continue
    bc = extract(bl, bsym, fn)
    hc = extract(hl, hsym, fn)
    # pair sites via difflib on normalised lines
    sm = difflib.SequenceMatcher(None, [norm(x) for x in bl], [norm(x) for x in hl], autojunk=False)
    linemap = {}
    for a, b, size in sm.get_matching_blocks():
        for k in range(size):
            linemap[a+k] = b+k
    hidx = {}
    for c in hc:
        hidx.setdefault(c[0], []).append(c)
    for c in bc:
        bi, order, raw, tf, n, mm, form = c
        hi = linemap.get(bi)
        if hi is None:
            continue  # line deleted/changed shape; handled by prose review
        cands = hidx.get(hi, [])
        match = [x for x in cands if x[1] == order]
        if not match:
            continue
        h = match[0]
        bcont = content(basecache, base_file, tf, n, mm)
        hcont = content(headcache, head_file, h[3], h[4], h[5])
        if bcont is None or hcont is None:
            continue
        if bcont != hcont:
            problems.append((fn, bi+1, hi+1, form, f"{tf}:{n}" + (f"-{mm}" if mm else ""),
                             f"{h[3]}:{h[4]}" + (f"-{h[5]}" if h[5] else ""), raw, h[2],
                             bcont.split("\n")[0][:90], hcont.split("\n")[0][:90]))

print(f"=== {len(problems)} cite(s) whose target CONTENT changed base->HEAD ===\n")
for p in problems:
    fn, bl_, hl_, form, bcite, hcite, braw, hraw, bfirst, hfirst = p
    print(f"[{form}] {PKG}/{fn}:{hl_}")
    print(f"    base cite {bcite}  ->  {bfirst}")
    print(f"    head cite {hcite}  ->  {hfirst}")
    print(f"    line: {hraw[:130]}")
    print()
