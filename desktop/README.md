# desktop

[ÖÐÎÄ](README.zh.md) | English

The admin console for flame: Vue 3 + Vite + Element Plus. The tenant side and the platform side are shipped from one build.

## Development

```bash
npm install
npm run dev      # http://localhost:5173
```

The dev server proxies `/nexus` to `http://localhost:27006` (see `vite.config.js`), so a local nexus must be running first.

Environment: copy `.env.example` to `.env` and change `VITE_API_BASE` as needed.

## Build

```bash
npm run build          # output to dist/
npm run preview        # preview the build locally
npm run check:locales  # locale bundle self-check (also runs before build, see below)
```

A production build emits no sourcemap (`build.sourcemap: false` in `vite.config.js`) ¡ª `dist/` is published as one package, and
shipping the `.map` files ships the source with it.

`npm run build` runs `prebuild` ¡ú `check:locales` (`scripts/check-locales.mjs`) first: with `zh-CN` as the baseline it compares
file sets, key sets, `{placeholders}` and empty values across languages. vue-i18n falls back to `en` silently on a missing key,
so without that gate the only detector for "Chinese shows up in the English interface" is a human eye.

## Layout

```
src/
  tenant/        tenant side: agents / skills / tools / flows / runs / endpoints / settings / asset market
  platform/      platform side: tenants / users / market / system
  title.js       window title (carries the tenant once signed in)
```

## Conventions

- API base URLs go through `src/tenant/config/admin.js`; do not build the base by hand inside a component.
- The auth header comes from `getAuthHeader()` in `src/tenant/utils/auth.js`.
- Status polling always passes `silent`, so a timer refresh does not throw a loading mask in the user's face.
- Time display goes through `formatDateTime()` in `src/utils/datetime.js`, never `toLocaleString()` directly: with no argument
  it follows the *system* language, so the dates stay in the system format after the interface switches to English.
- Backend errors are judged by their `error` code (`formatError()` / `body.error === 'xxx'`), never by comparing `msg` copy.
- The language switcher is `src/LocaleMenu.vue` (shared by the three shells); the button shows `currentLocaleShort()`. Adding a
  language only means editing `LOCALES` in `src/locales/index.js` and adding a directory.
