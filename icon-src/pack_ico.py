# -*- coding: utf-8 -*-
"""把 build-icons.ps1 渲染出的 1024px PNG 打包成多尺寸 ICO。

用法：python pack_ico.py <work_dir> <assets_dir>
"""
import pathlib
import struct
import sys

from PIL import Image

TRAY_SIZES = [16, 20, 24, 32, 40, 48, 64]
APP_SIZES = [16, 20, 24, 32, 40, 48, 64, 128, 256]


def write_ico(master, sizes, path):
    """手工拼 ICO。

    不能直接用 Pillow 的 save(sizes=[...])：那样只会写进第一档，
    实测托盘会退化成 1 个尺寸、Windows 还可能挑中模糊的那张。
    这里每档单独写一条 32bpp BMP 记录。
    """
    entries, blobs = [], []
    for s in sizes:
        im = master.resize((s, s), Image.LANCZOS)

        bgra = bytearray()
        for y in range(s - 1, -1, -1):                 # BMP 自下而上
            for x in range(s):
                r, g, b, a = im.getpixel((x, y))
                bgra += bytes((b, g, r, a))

        mask_row = ((s + 31) // 32) * 4                 # AND mask 行 4 字节对齐
        mask = bytes(mask_row * s)                      # 全 0：以 alpha 为准

        header = struct.pack("<IiiHHIIiiII", 40, s, s * 2, 1, 32, 0, 0, 0, 0, 0, 0)
        blob = header + bytes(bgra) + mask

        entries.append((s, len(blob)))
        blobs.append(blob)

    offset = 6 + 16 * len(entries)
    out = struct.pack("<HHH", 0, 1, len(entries))
    for (s, size), blob in zip(entries, blobs):
        w = 0 if s >= 256 else s                        # 0 表示 256
        out += struct.pack("<BBBBHHII", w, w, 0, 0, 1, 32, size, offset)
        offset += size

    path.write_bytes(out + b"".join(blobs))
    return path


def main():
    if len(sys.argv) != 3:
        print(__doc__)
        return 2

    work = pathlib.Path(sys.argv[1])
    assets = pathlib.Path(sys.argv[2])
    assets.mkdir(parents=True, exist_ok=True)

    for name in ("running", "stopped", "busy", "app"):
        png = work / f"{name}.png"
        if not png.exists():
            print(f"缺少 {png}")
            return 1
        master = Image.open(png).convert("RGBA").crop((0, 0, 1024, 1024))

        if name == "app":
            out = write_ico(master, APP_SIZES, assets.parent / "app.ico")
            print(f"app.ico        {sorted(APP_SIZES)}")
        else:
            out = write_ico(master, TRAY_SIZES, assets / f"{name}.ico")
            print(f"{name}.ico  {sorted(TRAY_SIZES)}")

    return 0


if __name__ == "__main__":
    sys.exit(main())
