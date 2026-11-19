package auth

// BootstrapAdminName is the login name of the platform's **protected administrator**: the active administrator seeded by
// the environment's initial data.
//
// **Fixed at plat, not configurable** - symmetrical with shared/tenants.PrimaryUserName on the tenant side: both are "the
// account the system preinstalls", so the console forbids modifying them (profile / role / status) and deleting them
// (the guard is isProtectedPlatformUser in handler/users.go; the delete endpoint does not exist at all).
//
// It lives in the auth package because it belongs to the platform identity vocabulary (alongside the role vocabulary),
// the handler layer references it directly, and this package must not depend on handler.
const BootstrapAdminName = "plat"
