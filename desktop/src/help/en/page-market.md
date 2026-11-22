# Asset marketplace

Four tabs: local market, remote market, my assets, install history.

## What installing an asset does

"Install" starts with a **pre-check** that shows you three things before you commit:

- whether an entity with that name already exists (overwrite, or rename per the suggestion)
- the dependency list, grouped by type: what is missing, what ships with the package
- the content fingerprint and any notes

Then you pick the install name, optionally the state a custom tool should have after install, and you can
tick "release a version immediately". A name collision asks for overwrite confirmation; a conflicting name
is filled back into the input with a suggestion, so you can retry in place.

The result is a **result page** that does not close itself: what was installed, what was overwritten,
which version was released, which files were skipped. Close it when you are done.

## Listing your own assets

Top right, "List asset": pick a type (skill / custom tool / agent / flow), then the object.

- A private skill cannot be listed on its own.
- Tenants exempt from review list immediately; others go to "pending review" and only appear in other
  tenants' local market once the platform approves them.
- Listing and delisting are marketplace actions: **delisting does not affect copies already installed**.

## My assets

Lists what you have listed. Pending entries can be withdrawn; unlisted ones can be deleted; **a listed
entry can only be delisted by the platform**, you cannot delete it yourself.

## The remote market

Grouped by peer site, with a tag on each site saying whether pulling from it is immediately usable or still
needs platform review. When the platform has not opened the remote market for this tenant, the tab is
empty and explains why.

## You cannot see the asset you want

Tell the two cases apart: an empty local market usually means "either it is already installed, or the
provider has not passed platform review yet"; an empty remote market means it is not opened, or that site
has nothing to pull.
