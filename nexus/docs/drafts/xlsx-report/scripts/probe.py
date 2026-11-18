#!/usr/bin/env python3
"""沙箱环境自检。

用途：动手写处理脚本之前，先确认「这台沙箱里到底有什么」——python 版本、可用库、
挂载点、import 根。不要靠猜，也不要试 pip install（沙箱断网，必然失败）。

用法：
    python probe.py

退出码恒为 0：缺库是「报告结果」，不是「脚本失败」。
"""
import importlib
import platform
import sys
from pathlib import Path

# 表格处理常用库；csv / zipfile / json 是标准库对照位（标准库都缺说明解释器有问题）
MODULES = ["openpyxl", "xlrd", "pandas", "numpy", "csv", "zipfile", "json"]

MOUNTS = ["/workspace", "/workspace/out", "/agent", "/skill"]


def describe_mounts():
    print("挂载点（mode=none 下本机为空属正常：沙箱内路径会被翻译回宿主）:")
    for p in MOUNTS:
        path = Path(p)
        if path.is_dir():
            try:
                names = sorted(q.name for q in path.iterdir())[:20]
            except OSError as e:
                names = [f"(读取失败: {e})"]
            print(f"  ok      {p:16} {names}")
        else:
            print(f"  MISSING {p:16}")


def describe_modules():
    missing = []
    print("模块:")
    for name in MODULES:
        try:
            mod = importlib.import_module(name)
        except Exception as e:
            missing.append(name)
            print(f"  MISS    {name:10} ({type(e).__name__}: {e})")
        else:
            ver = getattr(mod, "__version__", "")
            print(f"  ok      {name:10} {ver}".rstrip())
    return missing


def main():
    print(f"python      : {sys.version.split()[0]}")
    print(f"executable  : {sys.executable}")
    print(f"platform    : {platform.platform()}")
    print(f"cwd         : {Path.cwd()}")
    # sys.path[0] 决定 import 的根：直接执行脚本时是「脚本所在目录」——
    # 这就是「附属模块必须用绝对 import」那条约定的由来。
    path0 = sys.path[0] if sys.path else "(空)"
    print(f"sys.path[0] : {path0}")
    print()

    describe_mounts()
    print()
    missing = describe_modules()
    print()

    if missing:
        joined = ", ".join(missing)
        print(f"结论: 缺少 {joined} —— 如实报告，不要 pip install（沙箱断网）")
    else:
        print("结论: 依赖齐全")
    return 0


if __name__ == "__main__":
    sys.exit(main())
