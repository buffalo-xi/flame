# The Tenants screen

The left column is a list of tenant cards, the right column has six collapsible cards for the selected
tenant. **Every card has its own save button**; there is no global save.

## The left column

Each card has three lines: display name + status, schema · owner, and three usage figures (used / cap).
At the top you can search by schema or display name (**Enter triggers the search**) and filter by status;
the bottom is server-side pagination.

A tenant without an owner shows "managed by an admin".

## The six cards

| Card | What it changes |
|---|---|
| Tenant profile | Display name, owner, remark |
| Quota | The five quotas' **raw values for this tenant** |
| Plugins | The platform's plugin disables for this tenant |
| Capability grants | Whether exec / shell / code / browser / egress are opened for this tenant |
| Flow runtime parameters | Tenant values for the safety valves (timeout, recursion depth…); the system level is shown read-only |
| Agent runtime parameters | Same, plus an "effective" column |

**The plugin and capability cards point in opposite directions**: plugins are allowed by default, so
turning a switch on disables that tenant; capabilities are denied by default, so turning a switch on opens
it. To drop a per-tenant setting, plugins use "Reset to default (allow)" and capabilities use "Reset to not
opened".

## Creating a tenant

The schema is discovered automatically (from t0 upwards, first free slot; +1 each time), cannot be edited,
and can be re-discovered. The login name is fixed to `primary`; the initial password is prefilled but
editable and required. The plan is only a label.

Creation happens in a **single transaction**: create the schema, run the template DDL, create the tenant's
first admin, write the registry row.

Two common failures:

- **The schema was taken**: the dialog reports it and re-discovers automatically, stays open, and you
  confirm the new value and press Create again.
- A pre-check failed: the schema has not been discovered yet, or the password is empty.

## Transferring the owner

Change the owner dropdown in the tenant profile card (it only lists accounts with the user role and a
normal status). Saving asks for confirmation, because **this transfers the tenant**: the new owner sees and
manages it under "My tenants" immediately, and the old owner loses the entry.

## Suspend / enable / delete

Suspending blocks sign-in to the console and rejects MCP / RPC calls immediately; enabling only restores it.

Deleting requires the tenant to be **suspended first** (the button is greyed out while the tenant is
active — a precondition, not a permission problem). Deleting wipes the tenant's schema and file directory
and cannot be undone.

## How quotas and runtime parameters are resolved

- The input holds the **tenant's raw value**: empty = not set (fall back to the system level), `-1` =
  unlimited.
- **Effective = min(tenant value, system cap)**, computed by the backend; the frontend does not recalculate.
- A platform admin entering a value above the cap has it stored but clamped, with a warning at the bottom of
  the card; a tenant raising its own value above the cap is rejected outright.
- The agent group has no `-1` (they are safety valves, ≥ 1); the flow group allows `-1`.
- Session space is entered in MiB.

## The remark is visible to the tenant

The remark is not an internal field: the tenant sees the same text under "My tenants". Assume it will be
read by them.
