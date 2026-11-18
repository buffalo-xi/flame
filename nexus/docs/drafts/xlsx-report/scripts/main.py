#!/usr/bin/env python3
"""xlsx-report 技能入口：读工作区里的输入表，按条件筛选并写出汇总。

本文件同时是「怎么写」的参考实现：骨架、错误处理、自检都照这套来，只换 process()。

与 SKILL.md 同源的约定（改动请同步两边）：
  1. 输入只读，绝不写回；产物只写 OUT_DIR
  2. 附属模块放 lib/，用绝对 import（from lib.x import y），禁止 relative import
  3. 失败退非 0，可读原因写 stderr
  4. 幂等：同一输入重跑结果一致
  5. 写完后立刻回读自检

路径一律相对**当前工作目录**（沙箱内即 /workspace）。不写死 /workspace 的原因：
exec 会把 cwd 设为工作区根，而 mode=none 下只有 argv 与 cwd 会被翻译回宿主路径，
脚本正文里的绝对路径不会被翻译 —— 写死会在本机开发时直接失效。
"""
import argparse
import sys
from pathlib import Path

IN_DIR = Path.cwd()
OUT_DIR = IN_DIR / "out"
TABLE_SUFFIXES = {".xlsx", ".xlsm", ".csv"}


def fail(msg):
    print(f"[xlsx-report] 失败: {msg}", file=sys.stderr)
    sys.exit(1)


def note(msg):
    print(f"[xlsx-report] {msg}", file=sys.stderr)


def build_parser():
    p = argparse.ArgumentParser(description="按金额阈值筛选并汇总")
    p.add_argument("--input", default="", help="相对工作区根的输入文件名；留空时自动取唯一的表格文件")
    p.add_argument("--sheet", default="", help="工作表名；留空取第一个")
    p.add_argument("--amount-col", default="金额", help="用于筛选/汇总的数值列名")
    p.add_argument("--min-amount", type=float, default=0.0, help="保留 >= 该值的行（默认 0）")
    p.add_argument("--out", default="out.xlsx", help="输出文件名（写入 out/ 下）")
    return p


def pick_input(explicit):
    """确定输入文件：显式优先，否则要求工作区根下只有一个表格文件。"""
    if explicit:
        p = IN_DIR / explicit
        if not p.is_file():
            fail(f"输入文件不存在: {p}")
        return p
    cands = sorted(q for q in IN_DIR.iterdir()
                   if q.is_file() and q.suffix.lower() in TABLE_SUFFIXES)
    if len(cands) != 1:
        names = [q.name for q in cands]
        fail(f"无法自动确定输入文件，请用 --input 指定；工作区下找到 {len(cands)} 个: {names}")
    return cands[0]


def read_rows(src, sheet):
    """返回 (header, rows)。

    注意：这里一次性把所有行读进内存，便于做全量汇总。输入很大时改用流式
    （read_only=True 且边迭代边处理 / pandas 的 chunksize），见 SKILL.md 注意事项。
    """
    from openpyxl import load_workbook

    wb = load_workbook(src, read_only=True, data_only=True)
    if sheet and sheet not in wb.sheetnames:
        fail(f"工作表不存在: {sheet}；实际工作表: {wb.sheetnames}")
    ws = wb[sheet] if sheet else wb[wb.sheetnames[0]]
    it = ws.iter_rows(values_only=True)
    for row in it:
        header = [("" if c is None else str(c).strip()) for c in row]
        return header, list(it)
    fail("输入表是空的（没有表头行）")


def to_number(v):
    """尽量把单元格值转成 float，转不了返回 None（由调用方决定跳过还是报错）。"""
    if v is None or v == "":
        return None
    if isinstance(v, (int, float)):
        return float(v)
    try:
        return float(str(v).replace(",", "").strip())
    except ValueError:
        return None


def process(src, sheet, amount_col, min_amount, dst):
    """骨架的处理段：筛选 + 汇总。返回保留的数据行数。"""
    from openpyxl import Workbook

    header, rows = read_rows(src, sheet)
    if amount_col not in header:
        fail(f"输入表缺少列 {amount_col}；实际列名: {header}")
    idx = header.index(amount_col)

    kept = []
    skipped = 0
    for r in rows:
        if all(c is None for c in r):
            continue
        n = to_number(r[idx] if idx < len(r) else None)
        if n is None:
            skipped += 1
            continue
        if n >= min_amount:
            kept.append(r)

    if skipped:
        note(f"有 {skipped} 行的 {amount_col} 无法解析为数字，已跳过")

    wb = Workbook()
    ws = wb.active
    ws.append(header)
    for r in kept:
        ws.append(list(r))

    total_row = [None] * len(header)
    total_row[0] = f"合计（{len(kept)} 行）"
    total_row[idx] = sum(to_number(r[idx]) or 0.0 for r in kept)
    ws.append(total_row)

    OUT_DIR.mkdir(parents=True, exist_ok=True)
    wb.save(dst)
    return len(kept)


def self_check(dst, kept):
    """产物回读自检：不通过就退非 0，别让用户打开文件才发现不对。"""
    from openpyxl import load_workbook

    if not dst.is_file() or dst.stat().st_size == 0:
        fail(f"产物未生成或为空: {dst}")

    wb = load_workbook(dst, read_only=True, data_only=True)
    ws = wb[wb.sheetnames[0]]
    # read_only 下 max_row 依赖文件声明的 dimension，不可靠 —— 实际数一遍
    n = sum(1 for _ in ws.iter_rows(values_only=True))
    expect = kept + 2
    if n != expect:
        fail(f"自检不通过：产物应有 {expect} 行（表头+数据+合计），实际 {n} 行")


def main():
    args = build_parser().parse_args()

    src = pick_input(args.input)
    dst = OUT_DIR / args.out
    note(f"输入 {src} -> 输出 {dst}")

    kept = process(src, args.sheet, args.amount_col, args.min_amount, dst)
    self_check(dst, kept)
    note(f"完成：{dst}（{kept} 行）")
    return 0


if __name__ == "__main__":
    sys.exit(main())
