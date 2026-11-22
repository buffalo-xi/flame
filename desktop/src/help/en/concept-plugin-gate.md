# The two-layer switch for plugins and capabilities

The platform console groups two things under "Plugins and capabilities", and their **defaults point in
opposite directions**. Hold on to that one fact; everything else follows from it.

## The one-line difference

- **Plugin**: if the implementation is on disk, it can be used. The platform only **disables it for a
  particular tenant** when it has to. Default = allowed.
- **Capability** (exec / long-lived shell / code snippets / browser / egress): this means "the tenant may
  run its own code and reach the network", so the platform has to **open it per tenant**. Default = denied.

## Three layers, outermost first

| Layer | Where | Notes |
|---|---|---|
| ① Global gate | Top of the detail pane: "Global disable (kill switch)" / "Global hard gate" | Passes by default. Pulling it takes effect for every tenant immediately and **overrides** each tenant's own setting |
| ② Platform per tenant | The switch in each table row, then that row's Save | Plugin: on = disable this tenant only. Capability: on = open for this tenant only |
| ③ The tenant itself | Tenant console → Tools | For a plugin the tenant has to enable it and pick an implementation; for a capability the tenant also has to grant it to a specific agent |

## When something is actually usable

All of them have to hold: the global gate is not blocking, the platform has not disabled this tenant
(or has opened the capability), the tenant enabled it, and for capabilities it was granted to a
specific agent.

When one of them fails, the tool is greyed out in the catalog with a reason, and the **execution layer
rejects the call** — it does not rely on the prompt to talk the model out of it.

## Reading the three states

| You see | It means |
|---|---|
| Not set (allowed by default / not opened by default) | This tenant has no row of its own; the default applies |
| Set for this tenant | This tenant has a row of its own |
| Global gate active | The gate is blocking. Each tenant's original setting is **kept** and comes back when the gate is lifted |

## Egress is not a per-domain firewall

Egress is a cross-channel outbound policy: it decides whether exec / shell / skill scripts can reach the
network, not which hosts they may reach.

Two preconditions: at least one of exec or shell is open (with both closed the switch is greyed out and the
backend rejects it too), and egress itself is open. With it closed, commands still run inside the sandbox —
they just cannot reach the network.

**To control outbound access by domain, use the flow's http node**, not this switch.

## The cost sits on the platform side

A browser keeps a Chromium alive and the exec family leaves long-lived processes; those resources are paid
for by the platform, not the tenant. That is why these capabilities are denied by default and opened one by
one.

## Where it is easy to get backwards

- **The plugin switch is inverted**: on = disable this tenant, not allow. The labels on the switch
  ("Disable / Pass") exist to remind you of that; the capability switch is the normal way round
  (on = open).
- **The kill switch does not clear grants**: it stacks one layer on top, and lifting it restores what
  each tenant had before.
- **"The tenant enabled it" does not mean it works**: true for plugins (when the platform has no
  setting), not true for capabilities — those are denied by default, and until the platform opens one
  the tenant's own switch is greyed out.
