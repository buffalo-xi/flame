# 部署说明

中文 | [English](DEPLOYMENT.md)

管理台是一个静态包：`npm run build` 产出 `dist/`，发布的就是 `dist/`。本文不绑定任何托管厂商——下面哪种方式跟你现有的
运维方式一致，就用哪种。

> 这份文件原本是一份通用 Vite 部署模板，里面还混着另一个项目的构建数字（`index.html` 0.46 kB、包体 64 kB、镜像名
> `large-frontend`）。那些数字对本仓库是错的，建议也完全不针对这个管理台，所以是**重写**而不是翻译。
> 旧文本没有保留中文镜像：一份过时的镜像比没有镜像更糟。

## 构建

```bash
cd desktop
npm install
npm run build          # -> dist/
npm run preview        # 本地起 dist/ 检查一下
```

- `prebuild` 会跑 `check:locales`（`scripts/check-locales.mjs`），逐键比对语言包；缺键时构建直接失败，而不是把缺文案的
  语言发出去。
- 生产构建**不产出 sourcemap**（`vite.config.js` 的 `build.sourcemap: false`）。`dist/` 是整包发布的，带 `.map` 等于把
  源码一起发出去。
- `dist/assets/` 下的文件名带内容哈希，这正是下面示例敢给一年缓存的原因。`index.html` 不能缓存：只有它知道当前的哈希。

## 运行时配置

前端解析接口地址的顺序是：`window.APP_CONFIG.apiBase` → `VITE_API_BASE` → 默认 `/nexus`。

两种用法：

- **构建期**：`npm run build` 前设好 `VITE_API_BASE`（例如写在 `.env.production` 里），按环境分别构建；
- **运行期**：所有环境发同一份 `dist/`，由 `index.html` 旁边一个小的 `config.js` 注入 `window.APP_CONFIG`。
  一个产物、多套环境——不用重建，也不用按环境走不同流水线。

```html
<!-- index.html，放在 bundle 之前 -->
<script>window.APP_CONFIG = { apiBase: 'https://api.example.com/nexus' }</script>
```

## 静态托管

任何能发文件、并支持回退到 `index.html` 的服务器都行。`try_files` 是关键：管理台用的是 history 路由，
像 `/platform/tenants` 这样的深链必须返回 `index.html`，而不是 404。

```nginx
server {
    listen 80;
    server_name admin.example.com;
    root /path/to/dist;
    index index.html;

    location / {
        try_files $uri $uri/ /index.html;
    }

    # 带哈希的资源：缓存一年是安全的
    location /assets/ {
        expires 1y;
        add_header Cache-Control "public, immutable";
    }

    # 入口文档永不缓存
    location = /index.html {
        add_header Cache-Control "no-store";
    }
}
```

如果接口与页面同源，直接把接口前缀反代过去，别依赖 CORS：

```nginx
location /nexus/ {
    proxy_pass http://nexus-backend:27006/nexus/;
    proxy_set_header Host $host;
}
```

## 容器

```dockerfile
FROM nginx:alpine
COPY dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf
EXPOSE 80
CMD ["nginx", "-g", "daemon off;"]
```

```bash
docker build -t flame-desktop .
docker run -p 8080:80 flame-desktop
```

## 检查清单

- [ ] `npm run build` 通过（也就意味着 `check:locales` 通过）
- [ ] `dist/` 里没有 `*.map`
- [ ] 浏览器能访问到接口地址（同源反代，或配好 CORS）
- [ ] `try_files` / history 回退到位，深链可用
- [ ] `index.html` 用 `no-store`，带哈希的资源长缓存
- [ ] 前面有 HTTPS 终结（管理台是在浏览器里调接口的，混合内容会被拦）
- [ ] `.js` / `.css` 开了 gzip 或 brotli
