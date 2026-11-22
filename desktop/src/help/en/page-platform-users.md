# The Platform users screen

## The list and its filters

Cards on the left, details on the right. Each card has three lines: display name + role tag, account (with
"me" / "root account" markers), status + number of tenants. Search by account or display name (triggered by
Enter), filter by role and status; the bottom is server-side pagination.

## Creating a user

Fields: account (letters/digits/underscore/hyphen, 1-64 characters), display name (optional; the account
name is shown when empty), role (defaults to user), initial password (prefilled with `12345678`, editable).
Account and initial password are required.

## The two roles

- **Admin**: manages every tenant; the detail pane hides the "owned tenants" block.
- **User**: manages only the tenants they own.

Changing a role asks for confirmation, because the permissions change immediately.

## There are only three account states

Active / disabled / locked. **"Pending" and "deleted" are not account states** — they only appear inside
the "owned tenants" column, describing those tenants.

"Locked" and "disabled" block sign-in identically (both deny); the difference is the label. The "enable"
button in the card header pulls either of them straight back to active.

## Four protection rules

- You cannot disable the account you are signed in with (the button is disabled).
- The root account is read-only: not editable, not disable-able, not deletable.
- You cannot disable or demote the last active admin — the frontend does not block this, the backend
  rejects it.
- Disabling or demoting someone does **not** transfer their tenants; change the owner under Tenants
  afterwards.

## Resetting a password does not sign anyone out

The reset applies to that one account and requires at least 8 characters, entered twice. **Tokens already
issued stay valid**, so the person keeps working in other windows; to kick them out immediately, disable (or
lock) the account and enable it again.
