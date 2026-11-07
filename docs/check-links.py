# -*- coding: utf-8 -*-
"""校验 markdown 内部锚点链接是否真的能跳转。

按 GitHub 的 slug 规则计算标题锚点：
  1. 转小写
  2. 去掉标点（保留字母/数字/中文/连字符/下划线/空格）
  3. 空格换成连字符
"""
import pathlib
import re
import sys

def slug(text: str) -> str:
    text = text.strip().lower()
    # 去掉 markdown 行内标记
    text = re.sub(r'[`\*\~\[\]\(\)]', '', text)
    # GitHub：去掉除 - _ 空格 和字母数字（含中文）之外的字符
    text = re.sub(r'[^\w\s\-]', '', text, flags=re.UNICODE)
    text = text.replace(' ', '-')
    return text

def anchors_of(path: pathlib.Path):
    out = set()
    for line in path.read_text(encoding='utf-8').splitlines():
        m = re.match(r'^(#{1,6})\s+(.*)$', line)
        if m:
            out.add(slug(m.group(2)))
    return out

def main():
    root = pathlib.Path('.')
    md_files = list(root.glob('*.md')) + list((root / 'docs').glob('*.md'))
    cache = {p.resolve(): anchors_of(p) for p in md_files}

    bad = 0
    for p in md_files:
        text = p.read_text(encoding='utf-8')
        for m in re.finditer(r'\]\(([^)]+)\)', text):
            target = m.group(1)
            if target.startswith('http'):
                continue
            file_part, _, anchor = target.partition('#')
            if file_part:
                tpath = (p.parent / file_part).resolve()
            else:
                tpath = p.resolve()
            if not tpath.exists():
                print(f'  [缺文件] {p.name} -> {target}')
                bad += 1
                continue
            if anchor:
                if anchor not in cache.get(tpath, set()):
                    print(f'  [锚点不存在] {p.name} -> {target}')
                    avail = sorted(a for a in cache[tpath] if a)[:50]
                    print(f'        可用锚点里含 "{anchor[:20]}" 的：'
                          f'{[a for a in avail if anchor[:6] in a]}')
                    bad += 1

    print(f'\n共检查 {len(md_files)} 个文件，问题 {bad} 处')
    return 1 if bad else 0

if __name__ == '__main__':
    sys.exit(main())
