# Nginx sub_filter 完整解决方案

中文 | [English](NGINX_SUB_FILTER_SOLUTION.md)

> 适用范围：这份笔记写于把某个管理台挂到 `/large/` 前缀下的时候（磁盘路径是 `D:/proj/x-fw/...`）。前缀和路径属于那次部署，
> 不是 flame 的——换成你自己的即可。保留它是为了它解决的那个通用问题：`index.html` 里引用的是 `/assets/...`，
> 应用一旦挂到子路径，这些引用也得跟着改。若管理台直接挂在域名根下，根本用不到 sub_filter —— 见
> [DEPLOYMENT.zh.md](DEPLOYMENT.zh.md)。

## 问题分析

script 标签的 `src` 被正确替换，但 link 标签的 `href` 没有被替换。

## 解决方案

### 方案1：添加完整的 sub_filter_types 配置

```nginx
server {
    listen 80;
    server_name your-domain.com;

    # large 应用
    location /large/ {
        alias /path/to/frontend/large/dist/;
        index index.html;

        # 替换资源路径
        sub_filter '/assets/' '/large/assets/';
        sub_filter_once off;

        # 添加更多类型处理
        sub_filter_types text/html text/css application/javascript application/json;

        # SPA 路由支持
        try_files $uri $uri/ /large/index.html;
    }

    location /large/assets/ {
        alias /path/to/frontend/large/dist/assets/;
        expires 1y;
        add_header Cache-Control "public, immutable";
    }
}
```

### 方案2：使用多个 sub_filter 规则

```nginx
server {
    listen 80;
    server_name your-domain.com;

    location /large/ {
        alias /path/to/frontend/large/dist/;
        index index.html;

        # 替换所有可能的路径
        sub_filter '/assets/' '/large/assets/';
        sub_filter_once off;
        sub_filter_types *;

        try_files $uri $uri/ /large/index.html;
    }

    location /large/assets/ {
        alias /path/to/frontend/large/dist/assets/;
        expires 1y;
        add_header Cache-Control "public, immutable";
    }
}
```

### 方案3：把匹配锚定在属性上

```nginx
server {
    listen 80;
    server_name your-domain.com;

    location /large/ {
        alias /path/to/frontend/large/dist/;
        index index.html;

        # 锚定属性，正文里孤立的 "/assets/" 不会被误改
        sub_filter 'href="/assets/' 'href="/large/assets/';
        sub_filter 'src="/assets/' 'src="/large/assets/';
        sub_filter_once off;
        sub_filter_types text/html;

        try_files $uri $uri/ /large/index.html;
    }

    location /large/assets/ {
        alias /path/to/frontend/large/dist/assets/;
        expires 1y;
        add_header Cache-Control "public, immutable";
    }
}
```

### 方案4：使用 map 指令（推荐）

```nginx
http {
    # 定义资源路径映射
    map $request_uri $asset_path {
        default /assets/;
        ~^/large/ /large/assets/;
    }

    server {
        listen 80;
        server_name your-domain.com;

        location /large/ {
            alias /path/to/frontend/large/dist/;
            index index.html;

            # 使用变量替换
            sub_filter '/assets/' $asset_path;
            sub_filter_once off;
            sub_filter_types text/html;

            try_files $uri $uri/ /large/index.html;
        }

        location /large/assets/ {
            alias /path/to/frontend/large/dist/assets/;
            expires 1y;
            add_header Cache-Control "public, immutable";
        }
    }
}
```

### 方案5：使用 Lua 脚本（需要 ngx_http_lua_module）

```nginx
location /large/ {
    alias /path/to/frontend/large/dist/;
    index index.html;

    # 响应体由 body filter 产出，所以替换必须放在 body_filter_by_lua_block 里；
    # header_filter_by_lua_block 在响应体存在之前执行，改不到它。
    body_filter_by_lua_block {
        local chunk = ngx.arg[1]
        local eof = ngx.arg[2]
        if chunk and not eof then
            ngx.arg[1] = ngx.re.gsub(chunk, "/assets/", "/large/assets/", "jo")
        end
    }

    try_files $uri $uri/ /large/index.html;
}
```

## Windows 本地 Nginx 配置

```nginx
events {
    worker_connections 1024;
}

http {
    include       mime.types;
    default_type  application/octet-stream;

    sendfile        on;
    keepalive_timeout  65;

    server {
        listen       8080;
        server_name  localhost;

        # large 应用
        location /large/ {
            alias   D:/proj/x-fw/frontend/large/dist/;
            index  index.html;

            # 替换资源路径
            sub_filter '/assets/' '/large/assets/';
            sub_filter_once off;
            sub_filter_types text/html text/css application/javascript;

            # SPA 路由支持
            try_files $uri $uri/ /large/index.html;
        }

        # 静态资源
        location /large/assets/ {
            alias   D:/proj/x-fw/frontend/large/dist/assets/;
            expires  1y;
            add_header Cache-Control "public, immutable";
        }
    }
}
```

## 验证 sub_filter 模块

```bash
# 检查 nginx 是否包含 sub_filter 模块
nginx -V 2>&1 | grep -o ngx_http_sub_module

# 或在 Windows 下
nginx -V | findstr sub_filter
```

如果没有输出，说明 nginx 编译时没有包含 sub_filter 模块，需要重新编译 nginx 或使用预编译版本。

## 调试 sub_filter

### 1. 启用 sub_filter 日志

```nginx
error_log /var/log/nginx/error.log debug;

location /large/ {
    sub_filter '/assets/' '/large/assets/';
    sub_filter_once off;
    sub_filter_types text/html;

    # 加一个标记，确认请求确实落到这个 location
    add_header X-Sub-Filter "enabled";
}
```

### 2. 检查响应头

```bash
curl -I http://localhost/large/index.html
```

查看是否有 `X-Sub-Filter: enabled` 头。

### 3. 查看替换后的 HTML

```bash
curl http://localhost/large/index.html | grep -o 'href="[^"]*"'
```

应该看到 `href="/large/assets/..."` 而不是 `href="/assets/..."`。

## 常见问题

### 1. sub_filter 不生效

**原因**
- nginx 编译时没有包含 sub_filter 模块
- `sub_filter_types` 配置不正确
- 文件类型不在 `sub_filter_types` 里

**解决**

```nginx
# 检查模块
nginx -V | grep sub_filter

# 添加所有需要的类型
sub_filter_types text/html text/css application/javascript application/json application/xml;
```

### 2. 部分替换成功

**原因**：`sub_filter_once` 默认为 `on`，只替换第一次出现。

**解决**

```nginx
sub_filter_once off;
```

### 3. 缓存问题

**原因**：浏览器缓存了旧的 HTML 文件。

**解决**

```bash
# 清除浏览器缓存
# 或添加版本号
sub_filter '/assets/' '/large/assets/v1/';
```

## 替代方案：使用 sed 预处理

如果 nginx 的 sub_filter 不可用，可以在部署前预处理 HTML 文件：

```bash
# Linux / macOS
sed -i 's|/assets/|/large/assets/|g' dist/index.html

# Windows PowerShell
(Get-Content dist/index.html) -replace '/assets/', '/large/assets/' | Set-Content dist/index.html
```

## 完整的生产配置

```nginx
server {
    listen 80;
    server_name your-domain.com;

    # Gzip 压缩
    gzip on;
    gzip_types text/plain text/css application/json application/javascript text/xml application/xml application/xml+rss text/javascript;

    # large 应用
    location /large/ {
        alias /var/www/html/large/dist/;
        index index.html;

        # 替换资源路径
        sub_filter '/assets/' '/large/assets/';
        sub_filter_once off;
        sub_filter_types text/html text/css application/javascript application/json;

        # SPA 路由支持
        try_files $uri $uri/ /large/index.html;
    }

    # 静态资源
    location /large/assets/ {
        alias /var/www/html/large/dist/assets/;
        expires 1y;
        add_header Cache-Control "public, immutable";
        access_log off;
    }

    # 安全头
    add_header X-Frame-Options "SAMEORIGIN" always;
    add_header X-Content-Type-Options "nosniff" always;
}
```
