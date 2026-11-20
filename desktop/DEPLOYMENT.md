# Deployment

[ÖÐÎÄ](DEPLOYMENT.zh.md) | English

The admin console is a static bundle: `npm run build` produces `dist/`, and `dist/` is what gets published. Nothing on this
page is specific to a hosting provider ¡ª pick whichever of the options below matches what you already run.

> This file used to be a generic Vite deployment template with the build numbers of another project baked in (a 0.46 kB
> `index.html`, a 64 kB bundle, a docker tag `large-frontend`). The numbers were wrong for this repository and the advice was
> not about this console at all, so it was rewritten instead of translated. The old text is not kept as a mirror: a stale
> mirror is worse than no mirror.

## Build

```bash
cd desktop
npm install
npm run build          # -> dist/
npm run preview        # serve dist/ locally to check it
```

- `prebuild` runs `check:locales` (`scripts/check-locales.mjs`), which compares the locale bundles key by key; a build fails
  rather than shipping a language with a missing key.
- Production builds emit **no sourcemaps** (`build.sourcemap: false` in `vite.config.js`). `dist/` is published as one package,
  so a `.map` file would ship the source with it.
- Assets under `dist/assets/` carry a content hash, which is what makes the long `Cache-Control` in the example below safe.
  `index.html` must not be cached: it is the only file that knows the current hashes.

## Runtime configuration

The frontend resolves its API base in this order: `window.APP_CONFIG.apiBase`, then `VITE_API_BASE`, then the default `/nexus`.

Two ways to use that:

- **Build-time**: set `VITE_API_BASE` before `npm run build` (e.g. in `.env.production`) and rebuild per environment;
- **Runtime**: ship the same `dist/` everywhere and inject `window.APP_CONFIG` from a small `config.js` next to `index.html`.
  One artifact, many environments ¡ª no rebuild, no per-environment pipeline.

```html
<!-- index.html, before the bundle -->
<script>window.APP_CONFIG = { apiBase: 'https://api.example.com/nexus' }</script>
```

## Static hosting

Any server that can serve files and fall back to `index.html` works. `try_files` matters: the console uses history routing, so
a deep link such as `/platform/tenants` must return `index.html`, not 404.

```nginx
server {
    listen 80;
    server_name admin.example.com;
    root /path/to/dist;
    index index.html;

    location / {
        try_files $uri $uri/ /index.html;
    }

    # Hashed assets: safe to cache for a year
    location /assets/ {
        expires 1y;
        add_header Cache-Control "public, immutable";
    }

    # Never cache the entry document
    location = /index.html {
        add_header Cache-Control "no-store";
    }
}
```

If the API is served from the same origin, proxy the API prefix instead of relying on CORS:

```nginx
location /nexus/ {
    proxy_pass http://nexus-backend:27006/nexus/;
    proxy_set_header Host $host;
}
```

## Container

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

## Checklist

- [ ] `npm run build` passed (which means `check:locales` passed)
- [ ] `dist/` contains no `*.map`
- [ ] the API base is reachable from the browser (same origin proxy, or CORS configured)
- [ ] `try_files` / history fallback is in place, deep links work
- [ ] `index.html` is served with `no-store`, hashed assets with a long cache
- [ ] HTTPS terminates in front of it (the console talks to the API from the browser, so mixed content blocks it)
- [ ] gzip or brotli enabled for `.js` / `.css`
