# src/platform — the platform admin console (frontend)

[中文](README.zh.md) | English

One of the two subtrees that **do not depend on each other** (the other is the console, `src/tenant/**`). They share one Vite
project and one deployment artifact, and the login page switches between "tenant login" and "platform login".

## Layout

```
src/
├─ mode.js                   neutral switch: the current mode + which of the two sessions exist (readable from both sides)
├─ AuthGate.vue              thin login shell: [tenant login] ↔ [platform login]
├─ App.vue                   shell: renders AuthGate / MainWindow / PlatformApp from mode + session
└─ platform/                 ★ this subtree (can be lifted out whole)
    ├─ README.md             this file
    ├─ auth.js               platform session (app_flame_mgmt_token / app_flame_mgmt_user)
    ├─ api.js                the /platform/* request layer (unwraps {code,msg,data} uniformly)
    ├─ PlatformApp.vue       console shell (top bar identical to the tenant main window: logo + icon-pill nav + avatar/logout;
    │                        grey content area with white cards; routes by role to AdminConsole / MyTenantsView)
    ├─ assets/pic/flame.png  top-bar logo (a copy of the tenant one, to avoid a cross-subtree import)
    └─ views/
        ├─ PlatformLogin.vue platform login (account + password, no tenant field)
        ├─ AdminConsole.vue  admin page container (renders the view for the top-bar tab; it has no tabs of its own)
        ├─ AdminTenantsView.vue  tenant management (list on the left + detail card on the right)
        ├─ AdminAssetsView.vue   local market ("all assets" on the left + decision card on the right; plus a listing-policy
        │                        tab and an inbound tab)
        ├─ AdminMarketView.vue   cross-server market (peer site on the left + peer catalog on the right; only "I pull others")
        ├─ AdminPluginsView.vue  plugins and capabilities (nav on the left + detail on the right)
        ├─ AdminUsersView.vue    platform users
        ├─ AdminSystemView.vue   system settings (domain nav on the left + domain content on the right)
        ├─ system/               the "domain" components of system settings: one file per domain
        │   ├─ SysQuotaCard.vue        quota ceiling (system row)
        │   ├─ SysFlowConfigsCard.vue  flow runtime parameters (system row)
        │   └─ SysLlmPresetsCard.vue   LLM provider presets (globally shared, full CRUD)
        └─ MyTenantsView.vue the "user" identity: my tenants (read-only: tenant cards on the left + detail on the right)
```

## Boundary rules (bind every change in this subtree)

**Allowed imports**: `vue`, `element-plus`, files in the same directory, `src/mode.js`, and the neutral root modules both subtrees
share — `src/locales/**`, `src/help/**` (help drawer + topic registry; it pulls `src/utils/markdown.js` and
`src/composables/useEscClose.js`, which are neutral for the same reason)

**Forbidden imports**

| Forbidden | Instead |
|---|---|
| `src/tenant/components/**` (tenant pages and Login.vue) | the login page ships its own copy in `views/PlatformLogin.vue` |
| `src/tenant/utils/auth.js` | `auth.js` (this directory); the keys come from `SESSION_KEYS` in `mode.js` |
| `src/tenant/config/**` | `api.js` resolves apiBase itself (the runtime source is still `window.APP_CONFIG.apiBase` / `VITE_API_BASE`) |
| the top bar / content styles of `src/tenant/components/MainWindow.vue` | copied into `PlatformApp.vue` (`.top-nav` / `.nav-menu` / `.content-panel`); a visual change means changing both |
| `src/tenant/assets/pic/flame.png` | the copy is at `assets/pic/flame.png` |

The reverse holds too: `src/tenant/**` must not import `src/platform/**` (the only exception is the shell trio: `App.vue`,
`AuthGate.vue`, `mode.js`).

Those three are what makes "the subtree can be lifted out" true: lifting it means taking `src/platform/**` + `src/mode.js` and
nothing else.

## Identity and data surface

| `role` in the login token | Screens | Endpoints |
|---|---|---|
| `admin` | admin pages (tenants + local market + plugins and capabilities + platform users + system settings) | `/platform/tenants*`, `/platform/assets*`, `/platform/plugins`, `/platform/users*`, `/platform/system/*` |
| `user` | my tenants (self-service start/stop + flow runtime parameters + root account password) | `/platform/me/tenants` (read) + `/me/tenants/:schema/status` + `/me/tenants/:schema/flowconfigs` + `/me/tenants/:schema/primary/password`; the backend always checks `owner_user_id = self` |

Both start by calling `/platform/me`: a suspended or deleted account is invalidated immediately (the backend re-reads the
database on every call).

## Screen structure (the same shape as the tenant main window)

- **Top bar** (`PlatformApp.vue`): the logo image + italic `flame`; in the middle an icon-pill nav (admin: tenants / platform
  users / plugins and capabilities / system settings / local market / remote market; `user`: my tenants); on the right a round
  avatar (role / display name / account name / number of tenants owned in the `title` tip, **clicking copies the account name**)
  + "back to the console" (only shown when a console session still exists in the browser) + "sign out". There is also "change
  password" (`POST /platform/me/password`: you change your own with the old password; both admins and users can use it).
- **Content area**: the grey `#f0f2f5` background comes from `.main-content` in `PlatformApp`; `.content-panel` only supplies a
  10px inset, and **each page draws its own white card** (the same shape as the main window's left conversation panel / right
  content panel, 12px radius). Tenant management, platform users and my tenants (`user` identity) are all **two cards**
  (`.list-pane` / `.detail-pane`, the right card fixed at 400px): the admin pages use a table on the left, while my tenants uses
  a **tenant card grid** (`.t-grid`: `repeat(auto-fill, minmax(200px, 1fr))`, several per row, each about 200–250px and
  following the row width, the whole card clickable, the selection highlighted). The right card is read-only plus three
  self-service writes (the "suspend/enable" control in the card head, "root account password" in a collapsed card, and "flow
  runtime parameters"), all of which only affect tenants you own. **The page-level toolbar is gone**: counts / search / filter /
  refresh / new all live in the `.lp-bar` row inside the list card (there is no separate header strip — the page is two cards and
  keeps no control row spanning both). Inside the detail card, "tenant profile / quota / flow runtime parameters" and
  "account profile / reset password / tenants owned" are each their own **collapsible card** (`.blk` + `.blk-head` /
  `.blk-body`, open state in `open`); **the four blocks of my tenants start collapsed** (identity and info rows first, expand
  what you need). Card scrollbars are uniformly "**always present, coloured only on hover**" (`.detail-pane` / `.t-list` /
  `.sys-nav` / `.sys-body`): the width is fixed at 6px (`::-webkit-scrollbar: 6px` + `scrollbar-width: thin`) and **the thumb is
  transparent until hovered** — the bar is invisible in normal use and content never shifts because a bar appeared or vanished.
  > **Do not go back to `width: 0` / `scrollbar-width: none`**: that bar takes no space, so hovering reflows the content 6px
  > sideways. Likewise **do not touch `overflow`**: wheel / trackpad / keyboard scrolling stays as it is. Tables inside the list
  > cards use `el-table`'s own scrollbar, which already only appears on hover.
- **System settings page** (`AdminSystemView.vue`): domain nav on the left (220px white card) + **every domain card stacked
  vertically** on the right (reusing the `.blk` collapsible shell, all expanded by default). Clicking a nav item selects it,
  expands the card if it is collapsed, then `scrollIntoView` scrolls to it smoothly. The card head carries only "domain name +
  description + arrow"; **the save button sits inside each domain card** (`.blk-foot`: description on the left, button on the
  right). A domain component is a self-contained card: it owns its loading, validation, save and buttons, and the parent forwards
  no calls. **Adding a domain = a pair of `/platform/system/*` endpoints on the backend + one domain component + one line in
  `DOMAINS` (with `component`)**; domains do not affect each other. A domain component contains a three-column table
  ("config item / fallback / system setting") and lists "old → new" item by item in a confirmation dialog before saving.
- **Counts on the nav tabs**: the rendered view calls `emit('count', n)` → (through `AdminConsole` for admin pages, directly for
  my tenants) → `onCount(key, n)` in `PlatformApp`; `null` means the page has not loaded yet (the tab shows no parentheses, to
  avoid flashing a zero), and after the first load every add or delete refreshes it.

## Contract with the backend

- Success: `{code:200, msg, data}` → `request()` returns `data` directly
- Business failure: `{code:400, msg}` → throws `ApiError(msg)`
- Unauthorized / forbidden: HTTP `401` / `403` + `{error, message}` → throws `ApiError(message, status)`
  - a `401` also clears the local session and the shell returns to the login page
- Conflict: HTTP `409` + `{error:"conflict", message}` → throws `ApiError(message, 409)`
  (a schema taken between check and create goes this way: report it, discover a new value, **keep the dialog open**, and
  provision again once the operator confirms)
- Request prefix: `${apiBase}/platform` (in dev `apiBase=/nexus`, proxied to the backend by vite)

## Still to build

- Nothing. The console already covers: tenant management (**table on the left + detail card on the right**: search / status
  filter / a provisioning wizard (**schema auto-discovered, not editable**, `GET /platform/tenant-schemas/next`; the first
  tenant admin's login name is the fixed `primary` with an initial password prefilled as `12345678` (changeable); a second check
  before writing, and a 409 when taken → discover again + wait for the operator) / inline profile editing (including transferring
  the owner) / inline adjustment of the three quota kinds (usage vs limit + a progress bar) / inline adjustment of the flow
  runtime parameters (tenant row, with the system level read-only for comparison) / start and stop; the list uses real
  server-side paging `page`/`page_size`, and the top-bar badge uses the `total` the endpoint returns);
  the local market (**"all assets" on the left + decision card on the right**: blocks by status + kind/origin filters + search
  (`GET /platform/assets`, unpaged and naturally "pending first, then newest"; the detail shows the content (entry / file count /
  size), the dependency declaration (skills / private_skills not shipped with it / tools+grant_kind / requires / net.egress), how
  many tenants installed it, and the previous review note; the decision buttons follow the state machine: `pending` alone can be
  approved (note optional) / rejected (**reason required** — it is the sentence the tenant sees under "my assets"), `listed` alone
  can be delisted; visibility all ↔ allowlist (replaced wholesale, the tenant dropdown reads all of `/platform/tenants`); the
  top-bar badge is the **pending count**, not the total, and only refreshes while no status filter is applied, so it does not jump
  with the filter; the detail card's "export package" zips the reviewed snapshot (`GET /platform/assets/:id/package`), and the
  toolbar's "import asset package" uploads a zip exported on another machine (`POST /platform/asset-imports`, multipart
  `file`/`origin`/`force`); an import is always pending and lists its warnings — carrying assets across servers is in
  `nexus/docs/asset-sharing.md` §12); plus a "listing policy" tab (whether a tenant listing needs platform review) and an
  "inbound access" tab (the anonymous-access switch `market.open` + egress tokens: someone else uses the token to read this
  machine's reviewed assets, the plaintext is shown once, tokens can be enabled/disabled/deleted — both are "who may pull from
  me", the opposite direction of the remote market's "I pull others", which is why they live in the local market);
  the remote market (**peer site on the left + peer catalog on the right**: a peer is the side I pull from (address + the
  `flmt_…` token it gave me + whether to auto-approve); after selecting one, "fetch catalog" (`GET /platform/peers/:id/catalog`,
  proxied to the peer, marking what is already imported / the local version), then "import / overwrite import" per row
  (`POST /platform/peers/:id/import`; `force` overwrites and bumps the local version by 1);
  platform users (**table on the left + detail card on the right**: search / role / status filters / a new-user dialog (display
  name optional, falling back to the account name; the initial password prefilled as `12345678`, changeable); the right card's
  three collapsible blocks = account profile (display name / role / status saved inline, the account name read-only) + reset
  password (inline, replacing the old dialog) + tenants owned (read-only, `GET /platform/users/:id/tenants`; not shown for
  `admin`); the "suspend/enable" and "save profile" controls in the card head are disabled for **yourself** and for the
  **protected admin** (the `protected` flag the backend returns, i.e. the fixed `plat`) — the backend refuses them as well; the
  list also uses real server-side paging `page`/`page_size`);
  my tenants (`user` identity: left = a **tenant card grid** (several per row; display name + schema on one line / status tag /
  plan / a usage summary (over-limit in red) / a note (at most two lines; when there is none, the fallback copy
  "the creator was too lazy to write anything!" is shown); local search + status filter, no paging — `/platform/me/tenants`
  returns everything at once) + right = a detail card with four collapsible blocks = usage and quota (read-only progress bars) +
  **root account password** (new password + confirmation, resets `primary` without touching role or status) + **flow runtime
  parameters** (editable: config item / system level read-only for comparison / tenant value, with the frontend enforcing "not
  above the system level" first) + tenant information (read-only key/value); the four blocks **all start collapsed**; there are
  **three self-service writes**: the card head's "suspend/enable" (`PUT /platform/me/tenants/:schema/status`), saving the flow
  runtime parameters (`PUT /platform/me/tenants/:schema/flowconfigs`) and the root account password
  (`POST /platform/me/tenants/:schema/primary/password`) — all confined to tenants you own (403 otherwise), all with
  confirmation/validation and all taking effect immediately; the tenant count is reported up to the top-bar tab as well);
  system settings (four domains: quota ceiling / flow runtime parameters / agent runtime parameters / LLM provider presets — the
  first three write the system row `tenant_id = ''`, the fourth is full CRUD on `platform.llm_provider_presets`). The quota card
  binds the **raw row** (`GET /tenants/:schema/quota`) while the row itself shows the **effective value**
  `min(tenant value, system ceiling)`, with an orange hint when the raw value is above the ceiling — the list endpoint returns
  the effective value and **must not be used as an edit form**.
