// Moved to src/composables/useEscClose.js: the help drawer (src/help) opens above the tenant panels
// and must share the same ESC stack, and src/platform/** must not depend on src/tenant/**.
// Re-exported so existing imports keep working and the stack stays a single instance.
export * from '../../composables/useEscClose.js'
