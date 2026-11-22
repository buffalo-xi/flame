// Moved to src/utils/markdown.js: the help drawer (src/help) needs the same renderer, and
// src/platform/** must not depend on src/tenant/**. Re-exported so existing imports keep working,
// and so there stays exactly one renderer with one cache and one sanitizing policy.
export * from '../../utils/markdown.js'
