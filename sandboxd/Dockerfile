# syntax=docker/dockerfile:1

# flame 沙箱运行时镜像
#
# 用途：sandboxd mode=docker 的镜像 rootfs（sandboxd/config.yaml 的 docker.image）。
#
# 契约（改动前先读 sandboxd/internal/daemon/worker.go 与 nexus/docs/sandbox-design.md §9）：
#   - 必须含 langs.<lang>.command 用到的每个解释器：lua / python / node；
#   - 用户脚本与 skill 脚本依赖的 Python 包必须预装（沙箱 --network none，
#     运行期 pip install 必然失败，见 nexus/docs/drafts/xlsx-report/SKILL.md）；
#   - 非 root 运行：config.yaml 的 docker.user = 1000:1000；
#   - rootfs 只读（docker.read_only: true）：除 /tmp 与挂载点外一律不可写，
#     因此禁用 Python 写 __pycache__（PYTHONDONTWRITEBYTECODE）；
#   - 常驻命令：`docker run -d ... <image> sleep infinity` → 需要 sleep 常驻；
#   - 杀 codehost 补刀：需要 procps；
#   - 镜像需含 codehost 二进制（/usr/local/bin/codehost，见下方 COPY）；
#   - 不设 ENTRYPOINT：sandboxd 总是显式给 argv（codehost 追加在镜像命令之后）；
#   - no browser in this image: the browser is a separate service (browserd) and runs Chromium on the host, so nothing
#     inside the sandbox ever launches one. The CJK fonts stay because script output (charts, PDFs) may still need them.
#
# base 选型：
#   node:24-trixie-slim —— node 官方镜像自带 node/npm 与 uid=gid=1000 的 node 用户，
#   血统为 Debian 13 (trixie)：python3 3.13 / lua5.4 5.4.7 / pandas 2.2.3 / numpy 2.2.4
#   / openpyxl 3.1.5 / xlrd 2.0.1，比 bookworm 整体新一代。
#   不用 alpine：musl 会引入第三套运行时语义（本机 Windows、生产 Linux 都是 glibc）。
#   ⚠️ 若 `node:24-trixie-slim` 不存在，仅把下一行改为 node:24-bookworm-slim，其余不变
#      （版本表随之回到 python 3.11 / pandas 1.5.3 那一档）。
#
# npm / luarocks 的定位（重要）：
#   只在「构建期」有意义 —— 沙箱运行期断网且 rootfs 只读，装不了包。
#   以后加包就在这里新增一行 RUN 再重建镜像，例如：
#     RUN npm install -g --omit=dev <pkg>
#     RUN luarocks --lua-version=5.4 --tree=/usr/local install <pkg>
#
# 构建：
#   docker build -t flame/sandbox:latest -f sandboxd/Dockerfile sandboxd

FROM node:24-trixie-slim

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    HOME=/tmp \
    PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    NODE_NO_WARNINGS=1 \
    NODE_PATH=/usr/local/lib/node_modules \
    LUA_PATH="/usr/local/share/lua/5.4/?.lua;/usr/local/share/lua/5.4/?/init.lua;/usr/share/lua/5.4/?.lua;/usr/share/lua/5.4/?/init.lua;;" \
    LUA_CPATH="/usr/local/lib/lua/5.4/?.so;/usr/lib/x86_64-linux-gnu/lua/5.4/?.so;/usr/lib/lua/5.4/?.so;;"

RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        ca-certificates \
        bash \
        coreutils \
        procps \
        passwd \
        python3 \
        python3-openpyxl \
        python3-xlrd \
        python3-pandas \
        python3-numpy \
        lua5.4 \
        luarocks \
        fonts-liberation \
        fonts-noto-cjk; \
    # 统一解释器入口名，与 config.yaml 的 langs.*.command 对齐：
    # Debian 只提供 lua5.4 / python3，lua 与 python 这两个名字都不存在。
    ln -sf /usr/bin/python3 /usr/local/bin/python; \
    ln -sf /usr/bin/lua5.4 /usr/local/bin/lua; \
    # 构建期预编译字节码（契约第 4 条的正面配套）：运行期 rootfs 只读 + PYTHONDONTWRITEBYTECODE=1，
    # 写不了 __pycache__，只能吃构建期的产物。apt 的 python3-pandas / python3-numpy 已由
    # dh_python3 预编译，这里 -f 再编一遍是兜住「base 换版本 / 包被替换」时 pyc 与源码不匹配；
    # -B 只影响 import 时的自动写盘，compileall 是显式编译，不受它约束。
    # 个别包自带坏语法的测试文件会让 compileall 返回非 0，不能因此让镜像构建失败。
    python -m compileall -q -f /usr/lib/python3/dist-packages >/dev/null || true; \
    # /tmp 是 worker 的可写挂载点与 cwd（挂载后由宿主侧 chmod 0777 覆盖）
    chmod 1777 /tmp; \
    # 非 root 运行账号：node 官方镜像已有 uid/gid 1000（node 用户），有则复用，无则创建
    if ! getent group 1000 >/dev/null; then groupadd -g 1000 sandbox; fi; \
    if ! getent passwd 1000 >/dev/null; then useradd -u 1000 -g 1000 -M -s /bin/bash sandbox; fi; \
    getent passwd 1000 >/dev/null; \
    apt-get clean; \
    rm -rf /var/lib/apt/lists/*

# 构建期自检：环境缺件在构建时失败，优于 agent 跑脚本时才暴露。
# Python 依赖清单与 nexus/docs/drafts/xlsx-report/scripts/probe.py 的 MODULES 对齐。
RUN set -eux; \
    lua -v; \
    python --version; \
    python3 --version; \
    node --version; \
    npm --version; \
    luarocks --version; \
    sh -c 'echo sh ok'; \
    sleep --version >/dev/null; \
    command -v pkill pgrep ps; \
    fc-list | grep -qi "noto sans cjk" && echo "cjk fonts ok"; \
    python -c 'import json, struct, sys, csv, zipfile; print("stdlib ok")'; \
    python -c 'import openpyxl, xlrd, pandas, numpy; print("py-deps ok:", openpyxl.__version__, pandas.__version__)'; \
    # 每次 execute 都是全新解释器，大包 import 的时间直接落在用户脚本前面。
    # 这里把 pandas 的 import 耗时打进构建日志，方便对比「预编译 pyc 前后」的差距
    # （构建机上的一次性参考值，别拿它当运行时承诺）。
    python -X importtime -c 'import pandas' 2>&1 | tail -n 3; \
    python -c 'import openpyxl, tempfile, os; p=os.path.join(tempfile.gettempdir(), "_sc.xlsx"); wb=openpyxl.Workbook(); wb.active["A1"]="ok"; wb.save(p); assert openpyxl.load_workbook(p).active["A1"].value=="ok"; os.remove(p); print("openpyxl round-trip ok")'; \
    node -e 'require("readline"); console.log("node ok")'; \
    lua -e 'print("lua ok")'; \
    test -w /tmp

# 把实际版本落成文件：运行期一条 `docker exec <id> cat /etc/flame-versions` 可查，
# 与 probe.py 互补（后者要挂 skill 目录，这个不用）。只读 rootfs 下也能读。
RUN set -eux; \
    { echo "python: $(python --version 2>&1)"; \
      echo "node:   $(node --version)"; \
      echo "npm:    $(npm --version)"; \
      echo "lua:    $(lua -v 2>&1)"; \
      echo "libs:   $(python -c 'import openpyxl,xlrd,pandas,numpy;print("openpyxl",openpyxl.__version__,"xlrd",xlrd.__version__,"pandas",pandas.__version__,"numpy",numpy.__version__)')"; \
    } > /etc/flame-versions; \
    cat /etc/flame-versions

# codehost 宿主二进制（D1 起 docker 也走 codehost，需预编译 Linux 版放入构建上下文）：
#   cd sandboxd && GOOS=linux GOARCH=amd64 go build -o bin/codehost-linux ./cmd/codehost
COPY --chmod=0755 bin/codehost-linux /usr/local/bin/codehost

# 默认非 root；docker run --user 会覆盖它（值相同，见 config.yaml）
USER 1000:1000

WORKDIR /tmp

# 常驻默认命令（codehost 由 sandboxd 显式给 argv，这条 CMD 仅作兜底常驻）。
CMD ["sleep", "infinity"]
