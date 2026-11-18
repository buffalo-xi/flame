# Nginx sub_filter完整解决方案

## 问题分析

script标签的`src`被正确替换，但link标签的`href`没有被替换�?
## 解决方案

### 方案1：添加完整的sub_filter_types配置

```nginx
server {
    listen 80;
    server_name your-domain.com;

    # large应用
    location /large/ {
        alias /path/to/frontend/large/dist/;
        index index.html;

        # 替换资源路径
        sub_filter '/assets/' '/large/assets/';
        sub_filter_once off;

        # 添加更多类型处理
        sub_filter_types text/html text/css application/javascript application/json;

        # SPA路由支持
        try_files $uri $uri/ /large/index.html;
    }

    location /large/assets/ {
        alias /path/to/frontend/large/dist/assets/;
        expires 1y;
        add_header Cache-Control "public, immutable";
    }
}
```

### 方案2：使用多个sub_filter规则

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

### 方案3：使用正则表达式sub_filter（Nginx 1.9.4+�?
```nginx
server {
    listen 80;
    server_name your-domain.com;

    location /large/ {
        alias /path/to/frontend/large/dist/;
        index index.html;

        # 使用正则表达式替�?        sub_filter 'href="/assets/' 'href="/large/assets/';
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

### 方案4：使用map指令（推荐）

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

### 方案5：使用Lua脚本（需要ngx_http_lua_module�?
```nginx
location /large/ {
    alias /path/to/frontend/large/dist/;
    index index.html;

    header_filter_by_lua_block {
        local content = ngx.arg[1]
        content = ngx.re.gsub(content, "/assets/", "/large/assets/", "jo")
        ngx.arg[1] = content
    }

    try_files $uri $uri/ /large/index.html;
}
```

## Windows本地Nginx配置

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

        # large应用
        location /large/ {
            alias   D:/proj/x-fw/frontend/large/dist/;
            index  index.html;

            # 替换资源路径
            sub_filter '/assets/' '/large/assets/';
            sub_filter_once off;
            sub_filter_types text/html text/css application/javascript;

            # SPA路由支持
            try_files $uri $uri/ /large/index.html;
        }

        # 静态资�?        location /large/assets/ {
            alias   D:/proj/x-fw/frontend/large/dist/assets/;
            expires  1y;
            add_header Cache-Control "public, immutable";
        }
    }
}
```

## 验证sub_filter模块

```bash
# 检查nginx是否包含sub_filter模块
nginx -V 2>&1 | grep -o ngx_http_sub_module

# 或在Windows�?nginx -V | findstr sub_filter
```

如果没有输出，说明nginx编译时没有包含sub_filter模块，需要重新编译nginx或使用预编译版本�?
## 调试sub_filter

### 1. 启用sub_filter日志

```nginx
error_log /var/log/nginx/error.log debug;

location /large/ {
    sub_filter '/assets/' '/large/assets/';
    sub_filter_once off;
    sub_filter_types text/html;

    # 添加调试日志
    add_header X-Sub-Filter "enabled";
}
```

### 2. 检查响应头

```bash
curl -I http://localhost/large/index.html
```

查看是否有`X-Sub-Filter: enabled`头�?
### 3. 查看替换后的HTML

```bash
curl http://localhost/large/index.html | grep -o 'href="[^"]*"'
```

应该看到`href="/large/assets/..."`而不是`href="/assets/..."`�?
## 常见问题

### 1. sub_filter不生�?
**原因**�?- nginx编译时没有包含sub_filter模块
- sub_filter_types配置不正�?- 文件类型不在sub_filter_types�?
**解决**�?```nginx
# 检查模�?nginx -V | grep sub_filter

# 添加所有需要的类型
sub_filter_types text/html text/css application/javascript application/json application/xml;
```

### 2. 部分替换成功

**原因**：sub_filter_once默认为on，只替换第一次出�?
**解决**�?```nginx
sub_filter_once off;
```

### 3. 缓存问题

**原因**：浏览器缓存了旧的HTML文件

**解决**�?```bash
# 清除浏览器缓�?# 或添加版本号
sub_filter '/assets/' '/large/assets/v1/';
```

## 替代方案：使用sed预处�?
如果nginx的sub_filter不可用，可以在部署前预处理HTML文件�?
```bash
# Linux/Mac
sed -i 's|/assets/|/large/assets/|g' dist/index.html

# Windows PowerShell
(Get-Content dist/index.html) -replace '/assets/', '/large/assets/' | Set-Content dist/index.html
```

## 完整的生产配�?
```nginx
server {
    listen 80;
    server_name your-domain.com;

    # Gzip压缩
    gzip on;
    gzip_types text/plain text/css application/json application/javascript text/xml application/xml application/xml+rss text/javascript;

    # large应用
    location /large/ {
        alias /var/www/html/large/dist/;
        index index.html;

        # 替换资源路径
        sub_filter '/assets/' '/large/assets/';
        sub_filter_once off;
        sub_filter_types text/html text/css application/javascript application/json;

        # SPA路由支持
        try_files $uri $uri/ /large/index.html;
    }

    # 静态资�?    location /large/assets/ {
        alias /var/www/html/large/dist/assets/;
        expires 1y;
        add_header Cache-Control "public, immutable";
        access_log off;
    }

    # 安全�?    add_header X-Frame-Options "SAMEORIGIN" always;
    add_header X-Content-Type-Options "nosniff" always;
}
```
