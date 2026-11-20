# Terminology: the UI, the code and the database must use the same word

[中文](terminology.zh.md) | English

Read this table before changing code. Each domain has its own set of action verbs — **do not borrow across domains**, and do
not rephrase at one layer just to sound smoother. The asset market already paid for this once: the UI said "publish", the
endpoint domain also said "publish", and the code carried a third set of `publish` — nobody could tell which one was meant.

## 1. Asset market (cross-tenant distribution)

| Concept | UI | Code | Database |
|---|---|---|---|
| List (action) | List | `share.CreateListing`, `listingPlan` | — |
| Listing record | My assets | `listing*`, `POST /admin/share/listings` | one row in `platform.shared_assets` |
| Listed (state) | Listed | `sharedassets.StatusListed` | `status = 'listed'` |
| Delist (action) | Delist | `action: 'delist'` | — |
| Delisted (state) | Delisted | `sharedassets.StatusDelisted` | `status = 'delisted'` |
| Listed by | Provider | `Asset.ListedBy/ListedByName` | `listed_by` / `listed_by_name` |
| Listing review policy | Listing policy | `shared/listingpolicy`, `/platform/listing-policy` | `platform.listing_settings` / `listing_policies` |

Full state machine: `draft → pending → listed | rejected → delisted` (withdrawing is `pending → draft`).

### Snapshot number `build`

| Concept | UI | Code | Database |
|---|---|---|---|
| Which packaging run this is | Snapshot v2 | `Asset.Build`, `PackageOrigin.Build` | `shared_assets.build` |
| Which snapshot is installed | Installed v2 | `Install.Build` | `shared_asset_installs.build` |

`build` is the **packaging sequence number**: every listing produces one snapshot directory `platform-assets/<id>/<build>/`
and increments the number, regardless of how many times the entity content changed. It is not the entity's version number
(entity revisions are not shipped yet; they will need their own word — do not reuse `build`).

**Do not use**: `publish` / `published` / `offline` / `publisher`; `version` on the asset side (the old `shared_assets.version`
was renamed to `build`, see `nexus/platform/sql/asset_build_rename.sql`).

## 2. Endpoints (external interface)

| Concept | UI | Code | Database |
|---|---|---|---|
| Expose | Expose | `Expose`, `SvcEndpoint.Exposed` | `svc_endpoints.exposed` |
| Toggling a single endpoint | switch | `UpdateExposed(bool)` | the same column |
| Retract | Retract | `Retract`, `RetractByTarget` / `RetractSvcEndpoints` | the same column set to `false` |
| Mirror JSON key | — | `EndpointProtocol.Exposed` | `{"exposed":true}` inside `agents.endpoints` / `flows.endpoints` |

The mirror column is a derived copy of `svc_endpoints`; renaming the key requires changing both sides, otherwise the UI shows
"not exposed".

**Do not use**: `publish` / `published` / `unpublish` (the endpoint domain has been fully converted); `revoke` (reserved for
grant revocation, section 4); `hide` (that is visibility — an endpoint is about callability).

## 3. Version / deployment (partially shipped)

| Concept | Wording |
|---|---|
| Push a version to production | **release** (`release` / `deploy`) |
| Switch production to a historical version (draft untouched) | **rollback** (`rollback`) |
| Load a historical version back into the draft (production untouched) | **load** (`apply`) |
| Canary | canary |

Workflow versions are already shipped with this wording: `ReleaseFlowVersion` / `ErrNoReleasedVersion`, and the flag in the
database is `is_current` (the single pointer to the production version). Deleting a historical version
(`DeleteFlowVersion`) only clears history and leaves production alone; the current version refuses deletion — stopping
service is a different action, do not smuggle it into delete.

### Entity versions (agents / skills / custom tools)

Three rules that differ from workflows; align before changing code:

1. **The version number follows the "entity root directory", not the subdirectories.** A private skill has no version of its
   own: it lives under `agents/<a>/skills/**`, so its version number is the owning agent's. Only **tenant-level** skills have
   independent versions in the skill domain (`skill_versions`).
2. **The draft domain and the version domain are siblings, not nested.** Drafts live in `agents|skills|tools/<name>/`,
   versions in `versions/{agents|skills|tools}/<name>/v<n>/` (one level under the tenant root, no leading dot — it is real
   data and must survive a backup). Putting it inside the entity directory would break the sentence "the draft directory
   holds drafts only", and snapshots would nest inside each other.
3. **Grants are never frozen, declarations are.** What a version freezes is the definition (body + description / visibility /
   tool dependency declarations / the agent's two switches `tools_unrestricted` and `skill_discovery` plus its default reply
   language `default_language` — they set the runtime contract, so they are definition); grants such as `agent_tool_grants` /
   `agent_skill_bindings` / `agent_relations` always read the current row — `revoke` must take effect immediately, so
   `rollback` leaves them alone and `apply` does not write them back.
   Corollary: production runs read those frozen attributes from the **current version**
   (`versioning.AgentCurrentAttributes`), not from the current value in the `agents` table; loading an old version writes those
   columns back according to that version.

   Custom tools behave the same: the production tool catalog and contract (`TOOL.json`: arguments / entry / language /
   timeout / egress) come from the current version (`customtoolstore.ListReleased / Released`), and only `status` and
   `configs` come from the current table row; a tool with no current version **does not enter the production catalog** and
   does not fall back to the draft definition. The draft view exists for admin screens and editor debugging only.

   Skills likewise: the production resident catalog lists only skills present in the current version
   (`skillMetaReader.runtimeMeta`), the same source as the `read_skill` / `use_skill` read path and the `/skill` mount of
   `run_skill_script`; a skill missing from the version does not appear (its body and scripts are not in the version
   directory either).
4. **Rollback is a pure pointer operation, separate from loading.** `rollback` only flips `is_current` to the target version
   (effective in production immediately, no new version, **draft untouched**); `apply` writes that version back into the
   draft (**production untouched**). The four domains share this semantics (`RollbackFlowVersion` /
   `RollbackAgentVersion` / `RollbackSkillVersion` / `RollbackToolVersion`). Without rollback, going back would mean "load
   into the draft → release again", which both overwrites the draft being edited and invents an extra version number.

Avoid `Publish` in code: the asset domain uses `listing`, the endpoint domain uses `expose`, the version domain uses
`release` / `deploy`. Exception: `published` in a workflow test run (draft trial) is the external value of the debug scope
(`draft` / `published`) — an existing contract, leave it alone.

## 4. Grants / credentials (unchanged)

| Concept | Wording |
|---|---|
| Revoke a tool or call grant | **revoke** (`revoke_agent_tool` / `revoke_agent_call`) |
| Revoke a market token | **revoke** |

`revoke` is for grants and credentials only; never use it for "taking an endpoint back from outside".

## 5. Variables (three scopes, do not mix)

Same root, different scope: `<scope>_vars`. All three are called "… variables"; the scope narrows from left to right.

| Concept | UI | Code / table | Scope | Mutable at runtime |
|---|---|---|---|---|
| Global variable | Global variable | `global_vars` / `shared/globalvars` / node input `source_type: "global"` / docs `${global_vars.X}` | whole tenant | no (run snapshot) |
| Environment variable | Environment variable | `flows.env_vars` / `EnvVars` / node input `source_type: "env"`; type is only string / number / secret | one flow | no (start snapshot) |
| Flow variable | Flow variable | `flows.flow_vars` / `FlowVars` | one flow | yes (written by nodes) |

- **Global means global within this tenant, never across tenants.** Each tenant has its own schema, and values never leave
  it.
- Exporting an asset (workflow / agent / skill) carries only the **reference name**, never the value: a workflow writes
  `{source_type: "global", source_key: "name"}` in node `config.inputs` and `{{ .input.name }}` in templates; definition-domain
  documents (`AGENT.md` / `SKILL.md`) write `${global_vars.name}`.
- **Two forms are retired**, do not write them: the template-level `{{.global_vars.X}}` (a flow fails on load, see
  `executors.rejectLegacyGlobalRefs`), and an environment variable pointing at a global one with `{type: "global", ...}`
  (runtime reports "deprecated type global", see `core.GetEnvVar`). Global and environment variables are now **siblings**,
  both sources of node input variables, neither routed through the other.
- A missing reference **fails hard**, with the variable name in the error; do not degrade to an empty string — empty strings
  turn "it runs even when unconfigured" into production reality, and debugging costs far more than a direct error.
- `secret` values are resolved at runtime only and are always masked by the API; never write them into `AGENT.md` /
  `SKILL.md` (they would enter the model context and the run records).
- If an existing asset package still contains `{type: "global"}`, `assetpack.StripSecrets` **keeps** its value and logs a
  migration hint (clearing it would erase what it pointed at). That is compatibility with existing packages, not support for
  the form.

**Do not use**: `tenant_variables` (the scope is already expressed by the schema), `shared_vars` (`share` means cross-tenant
distribution, it would collide), `vars` as a table name (it does not align with the `env_vars` / `flow_vars` root).

## 6. Scope of application

- New copy, APIs and tables take their words from the table above;
- if an existing identifier conflicts with the table, the table wins; change it together with a migration script;
- state values (`'listed'` and friends) are part of DB CHECK constraints; changing one requires updating
  `nexus/platform/sql/shared_assets.sql`, the migration scripts and both the frontend and the backend.

## 7. Comment and diagnostic wording

Comments, internal error strings, `msg` diagnostics and log lines are **English** (2026-11 decision). User-facing copy is
**not** written here at all: it comes from the `error` code plus the frontend locale bundles, so the same backend sentence
never decides what a Chinese or an English user reads.

Inside comments, keep the action verbs above intact: `list` / `delist`, `expose` / `retract`,
`release` / `deploy` / `rollback` / `apply`, `revoke`. Never translate them into `publish` / `hide`, and never translate a code
sample, a SQL fragment, a path or an identifier that appears inside a comment — those are references, not prose.
