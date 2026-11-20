# Bilingual (Chinese + English) implementation plan

[中文](i18n-plan.zh.md) | English

This document is the construction baseline, not a proposal. It is split into four layers whose costs differ by an order of
magnitude — **do not mix them into one effort**.

## Current state (inventory 2026-11, updated 2026-11-19)

| Layer | State | Scale |
|---|---|---|
| Documentation | **English primary**: `README.md` is the English main version with `README.zh.md` as the Chinese counterpart; technical documents under `nexus/docs/` follow the same shape (`<name>.md` + `<name>.zh.md`), migrated on demand | small |
| Frontend UI | **Shipped**: `vue-i18n` + 14 module bundles each in `desktop/src/locales/{zh-CN,en}/` (including `error`); components use `t()` + `formatError()` uniformly; no Chinese copy is left outside comments (see below) | Chinese comments remain (converted as files are touched, section 7b) |
| Backend copy | **Converted to error codes**: `ErrCode` / `ErrCodeData` are used throughout `nexus`, with zero "`ErrJSON` + Chinese" left in `nexus`, see `error-codes.md` | new code follows the convention |
| Generated content (persisted) | **The language is a generation parameter**: `shared/skeleton` registry + `lang` on the create request, English by default; the definition-domain template is gated in CI | a new language is one registry entry |
| Build and ops scripts | **Diagnostics English**: `scripts/check-locale.ps1`, `scripts/verify-sql.ps1` and `desktop/scripts/check-locales.mjs` report in English; their audits ignore Chinese quoted as data, so a Chinese sample inside an English comment is not a hit | — |
| Code comments | **English in AI-assisted code**: a comment written or rewritten by an assistant is English whatever the user writes in; legacy blocks are converted per touched line, never wholesale | Go at zero and gated; the desktop backlog shrinks by touch (section 7b) |

How much copy is actually left is measured with comments stripped (`scripts/check-locale.ps1`, section 7). Counting "lines
that contain Chinese" did not answer that question: it reported 403 lines for `Workbench.vue` while the copy in that file was
already in the bundle — the rest were design notes, which the plan does not translate (L0). After the 2026-11-20 sweep the
number is **2 lines, both deliberate**:

- the language switcher's endonyms (`中文` in `desktop/src/locales/index.js`) — a language is listed under its own name, and
  translating it would defeat the purpose of the picker;
- one legacy `msg` fallback in `MainWindow.vue` for a backend old enough to have no `error` field.

One caveat about the metric: it finds Chinese that never reached a bundle, not English that was hardcoded instead of keyed. New
screens still have to go through `t()`, and `desktop/scripts/check-locales.mjs` (run as `prebuild`) only catches the reverse —
a key that exists in one language and not the other.

Section 1 of the same script (Chinese comments in Go) is at zero as well. Its last three hits were fixtures quoted inside
English comments — a `系统:` line of a capability probe, a byte offset inside `"中"`, a snapshot excerpt `"百度一下"` — so the
counter now drops quoted spans before matching. A comment *describing* Chinese data is not a comment waiting to be translated.

## Layers and cost

### L1 bilingual documentation (1–1.5 days) — shipped (English primary since 2026-11)

- `README.md` (English primary) and `README.zh.md` (Chinese counterpart) link to each other at the top
- **English is the main version**: new content is written in English first and the Chinese version is aligned afterwards
  (the previously separate English file has been merged into `README.md`)
- technical documents follow the same shape: `<name>.md` (English) plus a `<name>.zh.md` mirror in the same directory,
  translated incrementally — there is no separate `docs/en/` tree
- the shape is not limited to `nexus/docs/`: `desktop/README.md`, `nexus/platform/README.md` and `desktop/src/platform/README.md`
  now follow it too, and so does `desktop/DEPLOYMENT.md`. That last one was **rewritten rather than translated**: it was a
  generic Vite deployment template carrying another project's build numbers, so a faithful translation would have preserved
  fiction; the English version and its mirror both describe this console.
- there is no Chinese-only primary document left: the last two were `desktop/NGINX_SUB_FILTER_SOLUTION.md` and
  `docs/screenshots/README.md`, both now English primary with a mirror. What is still Chinese in a primary document is
  deliberate: the language-switch line, the UI terminology pairs (`上架` / `下架`), and quoted samples (`系统:`, `"张三"`)

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

Done by 2026-11-20 for copy: section 7 of `scripts/check-locale.ps1` reports no Chinese outside comments in the desktop
sources. What is left in those files is Chinese comments, which are converted line by line as files are touched (L0).

### L0 comment language — English for what you touch, no wholesale migration

Comments never reach the UI and the model never reads them, so their language moves no acceptance criterion — which is why the
whole backlog is not translated, and why translating it would drift (whoever edits the code will not sync the English comment).

What is required is narrower and cheaper: **a comment written or rewritten by an assistant is English** — Go, `.vue` / `.js` and
SQL alike, whatever language the user writes in. A file half in each language is worse than a file in either, and the assistant
is the one introducing the text, so it has no excuse.

This is not a translation project. Touch three lines of a file that carries 200 Chinese comment lines and you leave the 200
alone: Go is at zero and gated, the desktop backlog grows smaller by touch and is only reported (section 7b).

## Content language policy (customer-visible / persisted)

The four layers above are about *where the work is*. This section is about *which language a given string is allowed to be*,
and it is the part that cannot be decided per file: a string put in the wrong class either flickers with the UI, freezes in
the wrong language, or silently forks agent behaviour.

| # | Class | Read by | Language | Mechanism |
|---|---|---|---|---|
| 1 | UI copy (labels, buttons, tabs, empty states, status values, dialogs) | the user, in the UI only | the UI locale | `desktop/src/locales/{zh-CN,en}/**` through `t()` / `$t()` |
| 2 | Business copy the backend returns (errors) | the user | the UI locale, resolved on the client | `ErrCode` / `ErrCodeData` send `error` + `params`; `msg` is English and only reaches operators and `curl` (`error-codes.md`) |
| 3 | Model-facing product copy: definition-domain files, built-in tool `Description` / `Label` / argument schemas | the model | **English only** | `nexus/platform/provision/template/files/**`, `ToolMeta`; gated by `TestSystemEntityFilesAreEnglish` |
| 4 | Text a run hands back (grant reasons, resolver warnings, stop messages) | the model, then the user | **English**, because nothing carries a language at that point | prose, no code — see below |
| 5 | Generated-and-persisted content (agent / skill skeletons, default flow node labels, version snapshots, asset packs) | the user, as editable files | fixed **at generation time** | language is a request parameter (`lang`), see below |

Decision rule: a new string goes into class 1 unless it must survive a language switch, be relayed by the model, or be read
by a model at all. "It is easier to build the string on the server" is not a reason for class 2.

### Persisted content: the language is an input, not a lookup

A skeleton, a default node label or a released version is written once and from then on belongs to the user: it is edited,
versioned, exported as an asset pack and installed into another tenant. Translating it at read time would make the version
snapshot, the asset pack and the rendered page disagree about the same file, and would make "the same entity" mean different
things on two machines.

So the language is chosen once, at creation, and never re-resolved:

- the create request carries `lang` (`admin/agents` and `admin/skills` bind it; the UI sends `currentLocale()`), and
  `shared/skeleton.Normalize` maps any tag onto the registry (`en`, `zh-CN`), with **English as the default** — an unknown
  language gets a readable English skeleton rather than an empty file or a 500;
- adding a language is one entry in `agentSkeletons` / `skillSkeletons`; the handlers do not change;
- the same rule covers default flow node labels: `desktop/src/tenant/utils/labels.js` reads the fallback label from the
  bundle, so the diagram persists whatever language the creating UI was in;
- history is not rewritten. An existing `AGENT.md`, a version snapshot or an installed asset keeps the language it was
  created in — only the template (`template/files/**`, class 3) is gated to English, because every new tenant materialises
  it and it goes straight into the model context.

### When no language input exists, the default is English

A stop message, a grant reason, a resolver warning and a backfilled version note are all produced where no language is in
hand: a stop arrives as a Redis signal on whichever instance holds the run, grant resolution happens inside a tool call, and
`cmd/backfillversions` runs per schema from an operator's shell. There is nothing to resolve against, so they take the same
default as `skeleton.Default`: **English**.

This is not "translate the backend copy twice", it is the same rule as class 5 — the language follows what the producer
knows. The user does not read raw English prose either: `responseLanguageRule` (the last line of the system prompt) makes the
model restate tool output in the language the user writes. Where the interface renders such a value directly it is *data*,
not copy — a version note in the release history keeps the language it was written in, exactly like a skeleton.

Two exceptions stay bilingual on purpose, and both are content rather than copy:

- `responseLanguageRule` itself is written in both languages, so it does not depend on the model reading the other one and
  stays legible to a non-Chinese author of `AGENT.md`;
- the `zh-CN` bodies in `shared/skeleton` are class 5 content (generated for a Chinese-language tenant), not product copy.

If a screen ever has to render one of these strings **as copy**, it must arrive as a `code` + `params` resolved from the
locale bundle — the same shape as the `error` envelope. None does today; `AgentGrantView.Note` and
`ResolveResult.Warnings` say so in their field comments, so the next person changing them sees the constraint.

### Pinning the output language of a call (MCP / RPC)

A programmatic call carries no language of its own: the input is a JSON argument map, and the reply-language rule ("reply in
the language the user writes in") has nothing to read. So the caller may pin it:

```json
{"name": "writer", "arguments": { "...": "..." }, "_meta": {"language": "zh-CN"}}
```

- `params._meta.language`, BCP-47, optional — the same slot as `progressToken` / `session_id` / `call_chain`, because it
  describes how the call should be answered and not what the input is (`core.MetaLanguageKey`);
- it becomes one English sentence appended right after the reply-language rule. Precedence: **`AGENT.md` pinned language >
  `_meta.language` > `agents.default_language` > the product default (English)**. An agent that only ever serves one
  language (a tutor for one language) must not be overridden by a caller;
- when nothing is specified the prompt says so: *"no output language was specified: reply in the language the input is written
  in, and when it carries no language signal, answer in English"*. Saying nothing was the previous behaviour, and with a
  Chinese-first rule over an English-first prompt the model simply guessed — the same call could come back in either language;
- `agents.default_language` is the middle step: a definition column (a release freezes it into `agent_versions`, so a
  production run reads the version's copy — see `versioning.AgentCurrentAttributes`), written by the normal agent update API
  **or by the super agent's `update_agent(default_language=...)`** (a single-column write, never a full-row write;
  `read_agent` echoes it), and validated like any other tag (a bad value falls through instead of being echoed);
- a malformed tag is rejected on the way in (`update_agent` returns an error instead of storing it) and ignored on the way
  out (`NormalizeOutputLanguage` lets it fall through to the next step), because the value ends up inside a system prompt;
- it follows the chain, but **only a pinned value travels**: passing a resolved default down would turn it into a pin for the
  callee, and a Chinese conversation would hand `en` to every child agent;
- it constrains prose only. Error copy still comes from the `error` code plus the bundle, and generated-and-persisted content
  still takes its language from the `lang` parameter (class 5) — a call-level hint must not silently retarget a skeleton.

RPC and MCP read the same field from `params._meta`. The value also reaches workflows, on the same footing as
`session_id`:

- a flow endpoint (RPC / MCP) takes `_meta.language` exactly like `_meta.session_id`, and every flow run carries it as
  the builtin variable `language` (`flow.ExecContext.Language`, `builtin.language` in a node). Empty means "nobody
  specified one", so a node can branch on it or drop it into a prompt without a second flag;
- it follows the chain in both directions. A flow started by an agent inherits the language that agent actually decided —
  a pin, or its configured `default_language`; the bare product fallback is **not** passed on, because a flow reading
  `language: en` would take it for an intent. It travels through ctx (`core.WithLanguageHint`), and the outbound RPC
  client puts a **pinned** value back into `params._meta.language`, so flow → agent and flow → sub-flow keep it. A
  resolved default still stops at the process boundary: it is visible to the flow and to anyone that reads ctx, but it is
  never forwarded as a pin — otherwise this process's default would override the next callee's own default
  (`core.LanguagePinnedFrom` is what draws that line);
- this has two carriers, one meaning each: `params._meta.language` (on the wire) is a pin, `core.WithLanguageHint` (in
  process) is the fact. A flow re-sends only the pin it received.

By design: a flow's own LLM node does **not** append the language rule on its own. Whether a prompt says "answer in X" is
the flow author's decision, so the node only gets the value: declare an input variable with `source_type: "builtin"` and
`source_key: "language"`, then use it in the prompt. An agent node inside a flow is covered, because the flow's pin travels
with the call and the agent applies its own precedence.

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

### 5. Long developer-facing explanations: translate a whole paragraph, never leave it in the source

Whole paragraphs in `.editor-tip` / `el-alert` (e.g. "授予时会自动补齐起步套件…") used to sit in the template in Chinese. They
are now ordinary bundle keys (`agentSettings.supervisorGrantHint`), translated as whole paragraphs — the "调用授权" tab of
`AgentSettingsPanel.vue`, cited here as the sample for leaving them Chinese, is fully translated today.

What the batching rule still says is *when* to spend the effort, not whether the paragraph may stay in the source: the text is
design explanation, it does not sit on the operating path (users want labels and buttons), and a literal gloss reads as
unreadable English.

Handling rules:

- **short labels come first**: field names, buttons, tabs, empty states, status values, confirmation dialogs;
- **long paragraphs are translated whole, by hand, when the screen is touched** — never machine-translated and never half a
  paragraph;
- a half-finished translation is worse than the Chinese original: English readers think the capability is missing, Chinese
  readers think something broke;
- what must not survive is Chinese prose in a template: `scripts/check-locale.ps1` section 7 reports it, and it is at zero.

### 6. Punctuation and typography

`，。：（）` must become `, . : ()` in an English context. Write the English bundle as `en-US` and never commit machine
translation as-is.

## Order of execution

```
done     L1 bilingual documentation
done     L2 error-code convention + repo-wide conversion (error-codes.md)
done     L3 skeleton + 14 module bundles + language switcher
done     content language = generation parameter (shared/skeleton, lang on create, CI gate)
done     L4 copy: no Chinese outside comments in the desktop sources (check-locale section 7)
ratchet  L0 comment language: the lines an assistant touches are English (backlog: section 7b)
```

## Cheap alternative

If the only goal is "show foreigners an English UI once": translate the shell only (navigation, buttons, login) and keep the
content area in Chinese, noting `UI partially translated` in the README. About 2 days — enough to validate demand before
deciding on L4.

## Enforcement

`scripts/check-locale.ps1` prints the audits described above. `-Gate` turns the decidable ones into an exit code, and
`scripts/run-tests.ps1` runs it that way after the tests, so the standard "run everything" command reports both verdicts at
once (`-SkipCopyCheck` drops the step). The gate always scans all four modules, even when `-Module` narrows the test run.

- Chinese in a comment — quoted samples excluded, because `系统:`, `"中"` and `"百度一下"` are data being described, not
  prose waiting to be translated;
- Chinese outside comments in a Go file that is not a `_test.go` (fixtures are data, see AGENTS.md);
- Chinese copy in the desktop sources that never reached a bundle;
- `ErrJSON` (removed in 2026-11, it must not come back), and a `msg` containing Chinese on an `ErrCode` line.

The only exceptions are two allowlists at the bottom of the script: the `zh-CN` bodies in `shared/skeleton`, the bilingual
`responseLanguageRule`, the language picker's endonyms and one legacy `msg` fallback. Adding an entry is a review decision,
not a build fix.

What the gate deliberately leaves alone, because rewriting it would cost more than living with it: test fixtures; SQL
comments and `COMMENT ON` text (a migration is history — `nexus/sql/system_entity_desc_en.sql` is the sanctioned way to move
seeded copy to English, and it says why the entity row *and* the `is_current` version row must both change); and the Chinese
comments in `.vue` / `.js` files, reported by section 7b rather than gated, since the rule there is "the lines you touch are
English" (L0).

## Acceptance

- after switching the language and refreshing, the choice persists (`localStorage`)
- `<html lang>` matches the actual language
- Element Plus built-in copy (pagination, date picker, empty state) follows the switch
- main screens show no overflow or truncation at `1280×720`
- backend business errors render in English in the English UI
