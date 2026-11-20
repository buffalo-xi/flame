# nginx sub_filter: serving an SPA under a sub-path

[ÖÐÎÄ](NGINX_SUB_FILTER_SOLUTION.zh.md) | English

> Scope: this note was written while mounting a console under a `/large/` prefix on another host (`D:/proj/x-fw/...`). The
> prefix and the disk paths are that deployment's, not flame's ¡ª substitute your own. It is kept because the problem it solves
> is generic: `index.html` references `/assets/...`, and if the app is mounted under a sub-path those references have to move
> too. If your console is served from the domain root, you do not need sub_filter at all ¡ª see
> [DEPLOYMENT.md](DEPLOYMENT.md).

## Problem

`script` tags get their `src` rewritten, but `link` tags do not get their `href` rewritten.

## Solutions

### 1. Add a complete `sub_filter_types`

```nginx
server {
    listen 80;
    server_name your-domain.com;

    # the app
    location /large/ {
        alias /path/to/frontend/large/dist/;
        index index.html;

        # rewrite the asset paths
        sub_filter '/assets/' '/large/assets/';
        sub_filter_once off;

        # handle more types
        sub_filter_types text/html text/css application/javascript application/json;

        # SPA routing
        try_files $uri $uri/ /large/index.html;
    }

    location /large/assets/ {
        alias /path/to/frontend/large/dist/assets/;
        expires 1y;
        add_header Cache-Control "public, immutable";
    }
}
```

### 2. Use several `sub_filter` rules

```nginx
server {
    listen 80;
    server_name your-domain.com;

    location /large/ {
        alias /path/to/frontend/large/dist/;
        index index.html;

        # rewrite every possible path
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

### 3. Anchor the match (nginx 1.9.4+ syntax with a literal prefix)

```nginx
server {
    listen 80;
    server_name your-domain.com;

    location /large/ {
        alias /path/to/frontend/large/dist/;
        index index.html;

        # anchor on the attribute, so a bare "/assets/" in text is left alone
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

### 4. Use a `map` (recommended)

```nginx
http {
    # define the asset path mapping
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

            # rewrite through a variable
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

### 5. Use Lua (needs `ngx_http_lua_module`)

```nginx
location /large/ {
    alias /path/to/frontend/large/dist/;
    index index.html;

    # The body is produced by a body filter, so the rewrite must happen in body_filter_by_lua_block;
    # header_filter_by_lua_block runs before the body exists and cannot touch it.
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

## Local nginx on Windows

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

        # the app
        location /large/ {
            alias   D:/proj/x-fw/frontend/large/dist/;
            index  index.html;

            # rewrite the asset paths
            sub_filter '/assets/' '/large/assets/';
            sub_filter_once off;
            sub_filter_types text/html text/css application/javascript;

            # SPA routing
            try_files $uri $uri/ /large/index.html;
        }

        # static assets
        location /large/assets/ {
            alias   D:/proj/x-fw/frontend/large/dist/assets/;
            expires  1y;
            add_header Cache-Control "public, immutable";
        }
    }
}
```

## Checking the sub_filter module

```bash
# does this nginx build include sub_filter?
nginx -V 2>&1 | grep -o ngx_http_sub_module

# or on Windows
nginx -V | findstr sub_filter
```

No output means nginx was compiled without the sub_filter module: recompile it or use a prebuilt package that includes it.

## Debugging sub_filter

### 1. Turn on the sub_filter log

```nginx
error_log /var/log/nginx/error.log debug;

location /large/ {
    sub_filter '/assets/' '/large/assets/';
    sub_filter_once off;
    sub_filter_types text/html;

    # mark the response so you can tell the location was hit
    add_header X-Sub-Filter "enabled";
}
```

### 2. Check the response headers

```bash
curl -I http://localhost/large/index.html
```

Look for `X-Sub-Filter: enabled`.

### 3. Check the rewritten HTML

```bash
curl http://localhost/large/index.html | grep -o 'href="[^"]*"'
```

You should see `href="/large/assets/..."`, not `href="/assets/..."`.

## Common problems

### 1. sub_filter does nothing

**Causes**
- nginx was compiled without the sub_filter module
- `sub_filter_types` is configured incorrectly
- the file's type is not listed in `sub_filter_types`

**Fix**

```nginx
# check the module
nginx -V | grep sub_filter

# list every type you need
sub_filter_types text/html text/css application/javascript application/json application/xml;
```

### 2. Only part of the content is rewritten

**Cause**: `sub_filter_once` defaults to `on`, so only the first occurrence is replaced.

**Fix**

```nginx
sub_filter_once off;
```

### 3. Caching

**Cause**: the browser cached the old HTML.

**Fix**

```bash
# clear the browser cache
# or bump the version in the path
sub_filter '/assets/' '/large/assets/v1/';
```

## Alternative: preprocess with sed

If sub_filter is not available, rewrite the HTML before deploying:

```bash
# Linux / macOS
sed -i 's|/assets/|/large/assets/|g' dist/index.html

# Windows PowerShell
(Get-Content dist/index.html) -replace '/assets/', '/large/assets/' | Set-Content dist/index.html
```

## Full production configuration

```nginx
server {
    listen 80;
    server_name your-domain.com;

    # gzip
    gzip on;
    gzip_types text/plain text/css application/json application/javascript text/xml application/xml application/xml+rss text/javascript;

    # the app
    location /large/ {
        alias /var/www/html/large/dist/;
        index index.html;

        # rewrite the asset paths
        sub_filter '/assets/' '/large/assets/';
        sub_filter_once off;
        sub_filter_types text/html text/css application/javascript application/json;

        # SPA routing
        try_files $uri $uri/ /large/index.html;
    }

    # static assets
    location /large/assets/ {
        alias /var/www/html/large/dist/assets/;
        expires 1y;
        add_header Cache-Control "public, immutable";
        access_log off;
    }

    # security headers
    add_header X-Frame-Options "SAMEORIGIN" always;
    add_header X-Content-Type-Options "nosniff" always;
}
```
