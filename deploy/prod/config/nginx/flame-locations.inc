# The business locations, shared by the plain (27001) and the TLS (27443) server block. Included inside a
# `server { }`, so it must not contain `map` or anything else that only exists in the http context.

# Readiness: nginx is up AND nexus is ready (nexus answers 503 until postgres, redis, litellm and a sandbox
# capability snapshot are all in place, and the body names the failing key).
location = /health {
    proxy_pass http://nexus:27006/health;
    access_log off;
}

# The same check under the name the container health checks use.
location = /healthz {
    proxy_pass http://nexus:27006/health;
    access_log off;
}

# The API. The desktop bundle calls it through the relative prefix /nexus and the prefix is *stripped* here:
# nexus serves /admin, /platform, /service, /ws and /debug at its root. The trailing slash in proxy_pass is
# what performs the strip - drop it and every call becomes 404.
location /nexus/ {
    proxy_pass http://nexus:27006/;

    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection $connection_upgrade;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $fwd_proto;

    proxy_buffering off;
    # A debug channel sits idle while a flow runs; the 60s default would cut it.
    proxy_read_timeout 3600s;
    proxy_send_timeout 3600s;
}

# /nexus without the trailing slash: proxy_pass would strip the prefix from an empty path.
location = /nexus {
    return 301 /nexus/;
}

# The tenant service API: external callers present an API key and the tenant is a path segment
# (/service/mcp/<schema>, /service/rpc/<schema>). This prefix is *not* stripped - proxy_pass has no trailing
# slash, which is what feeds nexus exactly /service/... No upgrade headers: neither protocol keeps a
# websocket open.
location /service/ {
    proxy_pass http://nexus:27006;

    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $fwd_proto;

    proxy_buffering off;
    proxy_read_timeout 3600s;
    proxy_send_timeout 3600s;
}

# Downloads: nexus answers X-Accel-Redirect: /__files/<path under storage.root_dir> and nginx serves the bytes
# from disk (sendfile, Range supported), so a 100 MB file never enters the Go process. `internal` is what
# makes it safe: the path cannot be requested directly, only reached through that redirect.
location /__files/ {
    internal;
    alias /srv/flame/data/;
}

# The SPA: unknown paths are client-side routes, not 404s.
location / {
    try_files $uri $uri/ /index.html;
}
