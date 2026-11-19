# Bilingual (Chinese + English) implementation plan

[中文](i18n-plan.zh.md) | English

This document is the construction baseline, not a proposal. It is split into four layers whose costs differ by an order of
magnitude — **do not mix them into one effort**.

## Current state (inventory 2026-11, updated 2026-11-19)

| Layer | State | Scale |
|---|---|---|
| Documentation | **English primary**: `README.md` is the English main version with `README.zh.md` as the Chinese counterpart; technical documents under `nexus/docs/` follow the same shape (`<name>.md` + `<name>.zh.md`), migrated on demand | small |
| Frontend UI | **Shipped**: `vue-i18n` + 14 module bundles each in `desktop/src/locales/{zh-CN,en}/` (including `error`); components use `t()` + `formatError()` uniformly; long design explanations are still Chinese (see pitfall 5) | scattered hard-coded strings remain |
| Backend copy | **Converted to error codes**: `ErrCode` / `ErrCodeData` are used throughout `nexus`, with zero "`ErrJSON` + Chinese" left in `nexus`, see `error-codes.md` | new code follows the convention |
| Code comments | All Chinese | tens of thousands of lines (not translated) |

Hot files: `Workbench.vue` (403 lines containing Chinese), `Flows.vue` (348), `AdminAssetsView.vue` (340),
`AdminTenantsView.vue` (315), `MainWindow.vue` (242).

## Layers and cost

### L1 bilingual documentation (1–1.5 days) — shipped (English primary since 2026-11)

- `README.md` (English primary) and `README.zh.md` (Chinese counterpart) link to each other at the top
- **English is the main version**: new content is written in English first and the Chinese version is aligned afterwards
  (the previously separate English file has been merged into `README.md`)
- technical documents follow the same shape: `<name>.md` (English) plus a `<name>.zh.md` mirror in the same directory,
  translated incrementally — there is no separate `docs/en/` tree

Rationale: zero regression risk, and it directly improves GitHub discoverability. A foreign visitor reads the README first
and only looks at the UI once it runs.

### L2 backend copy as error codes (2–3 days) — prerequisite for a bilingual UI

Today `code` has only three values, `200 / 400 / 507`, which cannot carry semantics. Translating only the frontend leaves
users looking at an "English UI with Chinese errors".

The plan is in `nexus/docs/error-codes.md`. Key points:

- `msg` degrades to "code + parameters"; the backend does not care about language
- **do not** take the `Accept-Language` route for copy (it maintains the same copy twice)
- it can be rolled out module by module, hot paths first: login / tenant / endpoint / quota / flow runs

### L3 frontend i18n skeleton (4–6 days)

- introduce `vue-i18n@9`
- Element Plus locale through `<el-config-provider :locale="elLocale">`, loading `zhCn` / `en` on demand
- keep the dayjs locale in sync
- language switcher + `localStorage` persistence + updating `<html lang>`
- **the first batch is only 5 screens**: login (`AuthGate.vue`), shell (`MainWindow.vue` / `PlatformApp.vue`), workbench
  (`Workbench.vue`), runtime ledger (`Running.vue`), run history (`RunHistory.vue`)

Locale bundles are split by module:

```
desktop/src/locales/
  index.js
  zh-CN/{common,tenant,platform}.json
  en/{common,tenant,platform}.json
```

**Use semantic names for keys, never Chinese as the key** (`t('common.save')`, not `t('保存')`), otherwise changing copy means
editing every bundle.

### L4 migrating all screens (8–12 days)

Batch by module, one PR per batch. Do not do one big rewrite: files of 300+ lines such as `Flows.vue` and
`AdminAssetsView.vue` have a very high conflict probability.

### L0 translating code comments — not doing it

Tens of thousands of lines of comments would inevitably drift from the code (whoever edits code will not sync the English
comment). The maintenance cost exceeds the benefit and it does not help adoption.

## Six pitfalls (worth reading before the effort estimate)

### 1. English blows up the layout

Chinese uses 2–4 characters ("保存", "新建租户") where English needs `Save` / `Create tenant` — 2–3× the length. Every fixed
`width`, `white-space: nowrap` and fixed-width `el-button` will overflow or truncate. **L3 must budget separate time for
visual regression**; it is not something to do on the side.

### 2. Chinese used as a business value

```19:19:desktop/src/tenant/utils/labels.js
  const wanted = (base || '').trim() || i18n.global.t('flow.node.fallbackLabel.node')
```

(That literal used to be `'节点'` and has since been routed through i18n.)

The same pattern exists where labels are used for deduplication or where branches compare Chinese status strings. **These are
not copy, they are business constants**; moving them into a locale bundle changes behaviour. Before migrating, search for
places where a Chinese literal takes part in a comparison / concatenation / persistence, and turn them into constants or
enums.

Samples already handled: `Workbench.vue` / `SkillFileEditor.vue` / `AgentFileEditor.vue` used to compare
`body.msg === 'already exists'`; after the backend introduced `agent.file_exists` / `skill.file_exists`, the frontend compares
`body.error === '...'`. `MainWindow.vue`'s "no default model configured" also moved from a bilingual regex to preferring
`llm.model_not_found` (falling back to copy matching only when the `error` field is absent, for old backends).
**New conditions must key on the `error` code, never on `msg`.**

### 3. String concatenation vs plurals

Concatenations like `${n} 个节点` have no plural form in Chinese but do in English (`1 node` / `2 nodes`). They must go through
vue-i18n pluralization: `t('flow.nodes', { n }, n)`, otherwise English is always one `s` short.

### 4. Do not translate definition-domain templates

`AGENT.md` and `SKILL.md` under `nexus/platform/provision/template/files/` are **prompts read by the model** — data, not UI.
Translating them changes agent behaviour and puts them out of sync with existing tenants (`nexus/data/tenants/t0/`).

### 5. Long developer-facing explanations stay untranslated for now

Whole paragraphs in `.editor-tip` / `el-alert` (e.g. "授予时会自动补齐起步套件…") **stay Chinese for now**.

Rationale: this text is design explanation; a literal translation reads as unreadable English, and it does not affect the
operating path — users want labels and buttons.

Handling rules:

- **short labels must be translated**: field names, buttons, tabs, empty states, status values, confirmation dialogs;
- **long paragraphs on demand**: when someone really needs them in the English UI, translate paragraph by paragraph by hand —
  do not dump them into a bundle first;
- a half-finished translation is worse than the Chinese original: English readers think the capability is missing, Chinese
  readers think something broke.

The "调用授权" tab of `AgentSettingsPanel.vue` is the first sample of this rule: the label and the empty state are translated,
the four explanatory paragraphs stay Chinese.

### 6. Punctuation and typography

`，。：（）` must become `, . : ()` in an English context. Write the English bundle as `en-US` and never commit machine
translation as-is.

## Order of execution

```
done     L1 bilingual documentation
done     L2 error-code convention + repo-wide conversion (error-codes.md)
done     L3 skeleton + 14 module bundles + language switcher
ongoing  L4 remaining hard-coded strings, long design notes translated on demand
skipped  L0 comment translation
```

## Cheap alternative

If the only goal is "show foreigners an English UI once": translate the shell only (navigation, buttons, login) and keep the
content area in Chinese, noting `UI partially translated` in the README. About 2 days — enough to validate demand before
deciding on L4.

## Acceptance

- after switching the language and refreshing, the choice persists (`localStorage`)
- `<html lang>` matches the actual language
- Element Plus built-in copy (pagination, date picker, empty state) follows the switch
- main screens show no overflow or truncation at `1280×720`
- backend business errors render in English in the English UI
