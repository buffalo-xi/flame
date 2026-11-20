# Screenshots

[中文](README.zh.md) | English

This directory holds the screenshots the README links to. The point is that someone opening the repository for the first time
can see what the system looks like and which screens it has **without running it**.

## Which ones to take

Pick by "one screen demonstrates one capability"; there is no need to capture everything:

| File | Screen | What it shows |
|---|---|---|
| `01-runtime.png` | Runs → Running | the cross-instance run ledger: what is running, for how long, what can be stopped |
| `02-runtime-history.png` | Runs → History | finished runs, searchable by time range and status, exportable |
| `03-flow-editor.png` | Workflows → Edit | visual orchestration (nodes / edges / variable panel) |
| `04-agent.png` | Agents | the assembly surface: prompt / model / tools / skills |
| `05-endpoints.png` | Endpoints | exposing an agent or a workflow as MCP / RPC |
| `06-capabilities.png` | Tools | tool catalog / plugins / custom tools |

## Specs

- **Size**: 1440×900 or 1600×1000 — large enough to read, not so large that the repository bloats
- **Format**: PNG; do not mix the light and dark theme across screenshots of the same screen
- **Weight**: compress each one to **under 300 KB** (TinyPNG / `pngquant`), otherwise cloning gets slow
- **Naming**: `NN-screen.png`; the number decides the order in the README

## Clean up before capturing

- Replace tenant and account names with demo values (`demo` / `acme`)
- Never show a real model key, token, internal address or customer name
- If the run history holds real error messages, pick a clean record

## How they are referenced

Drop the images here, then reference them from the "screens" block of the root `README.md` with a relative path:

```markdown
![run ledger](docs/screenshots/01-runtime.png)
```

GitHub renders relative PNG paths directly.
