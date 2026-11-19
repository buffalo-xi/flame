package agents

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/config"
	"flame/nexus/shared/globalvars"
	"flame/nexus/shared/models"
	"flame/nexus/shared/paths"
	"flame/nexus/shared/quota"
	"flame/nexus/shared/repository/sessions"
	"flame/nexus/shared/storage"
	"flame/tools/xhttp"
	"flame/tools/xlog"

	"github.com/gin-gonic/gin"
)

// ============================================================================
// Agent workspace file management (an agent is essentially a folder).
//   Everything goes through the storage abstraction (local / nfs / s3 alike).
//   skills/ and its contents are read-only (maintained by skill management; the file API must not touch them).
// ============================================================================

const (
	// maxAgentFileBytes caps a single file read or write (large files must not end up in the editor).
	maxAgentFileBytes = 2 << 20 // 2 MiB
	// readonlyPrefix is the read-only directory inside a workspace (the agent's private skills, maintained by the skills table).
	readonlyPrefix = "skills"
	// dirKeepFile: S3 has no empty directories, so a placeholder object is written to keep one listable.
	dirKeepFile = ".keep"
	// uploadDir is where session uploads land. Deliberately inside the session working directory (not a temp dir or a
	// user-level files/): it is both the default root of the file tools and the exec cwd, and only here can the agent read it.
	uploadDir = "uploads"
	// maxSessionUploadBytes caps one upload (20 MiB): anything larger becomes unreadable for tools such as read_file.
	maxSessionUploadBytes = 20 << 20
	// maxRecentScanEntries caps how many entries "recent files" scans at once (root plus one level):
	// when an agent has filled its session directory, one list request must not become dozens of LISTs.
	maxRecentScanEntries = 300
)

var (
	storeOnce sync.Once
	store     storage.Storage
	storeErr  error
)

// getStorage lazily builds the agent storage instance (the same configuration as service/agent.InitStorage).
func getStorage() (storage.Storage, error) {
	storeOnce.Do(func() {
		store, storeErr = storage.New(config.Cfg.Agent.Storage, config.Cfg.Agent.RootDir)
	})
	return store, storeErr
}

// isReadonlyRel reports whether a relative path falls in the read-only area (the skills directory and its contents).
func isReadonlyRel(clean string) bool {
	return clean == readonlyPrefix || strings.HasPrefix(clean, readonlyPrefix+"/")
}

// agentWorkspace returns the storage instance and the workspace key; on failure the response is already written.
func agentWorkspace(c *gin.Context) (storage.Storage, string, bool) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return nil, "", false
	}
	info := authInfo.(*auth.AuthInfo)

	inst, err := repo.GetAgent(info, c.Param("id"))
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to get the agent: "+err.Error(), "")
		return nil, "", false
	}
	if inst == nil {
		xhttp.ErrCode(c, "agent.not_found", nil, "Agent not found", "")
		return nil, "", false
	}
	st, serr := getStorage()
	if serr != nil {
		xhttp.ErrCode(c, "agent.storage_unavailable", nil, "storage not ready: "+serr.Error(), "")
		return nil, "", false
	}
	return st, tools.AgentsKey(info.Schema, inst.Name), true
}

// agentMutable checks that the agent may be changed: an exposed endpoint (exposed=true) forbids every change.
// A false return means the response is written; the caller just returns.
func agentMutable(c *gin.Context) bool {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return false
	}
	info := authInfo.(*auth.AuthInfo)
	inst, err := repo.GetAgent(info, c.Param("id"))
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to get the agent: "+err.Error(), "")
		return false
	}
	if inst == nil {
		xhttp.ErrCode(c, "agent.not_found", nil, "Agent not found", "")
		return false
	}
	// Only the normal state allows changing file contents
	if !inst.IsModifiable() {
		xhttp.ErrCode(c, "agent.deprecated", nil, "the agent is deprecated and cannot be modified (switch it back to normal first)", "")
		return false
	}
	return true
}

// joinKey joins a workspace-relative path (with traversal checks) and returns the full key plus the relative path.
func joinKey(wsKey, rel string) (string, string, error) {
	clean, err := tools.SafePath(rel)
	if err != nil {
		return "", "", err
	}
	if clean == "" {
		return wsKey, "", nil
	}
	return path.Join(wsKey, clean), clean, nil
}

// authInfoOf returns the current tenant (injected by middleware). When absent it returns false and writes nothing,
// leaving it to the caller whether to skip validation or report an error.
func authInfoOf(c *gin.Context) (*auth.AuthInfo, bool) {
	v, exists := c.Get("authInfo")
	if !exists {
		return nil, false
	}
	info, ok := v.(*auth.AuthInfo)
	return info, ok && info != nil
}

// checkEntryPath validates an entry path the user typed (shared by create and rename). An empty string means it passed.
//
// All three rules follow from "it must be visible in the list": a name starting with . is filtered out by List, so
// creating one leaves something holding a key that cannot be found; deep nesting cannot be navigated in the UI and adds one more prefix on S3.
func checkEntryPath(clean string) string {
	if clean == "" {
		return "path is required"
	}
	if strings.HasPrefix(path.Base(clean), ".") {
		return "a name cannot start with . (the list hides it)"
	}
	if len([]rune(path.Base(clean))) > 120 {
		return "the name is too long (limit 120 characters)"
	}
	if len(strings.Split(clean, "/")) > 16 {
		return "the directory is nested too deeply (limit 16 levels)"
	}
	return ""
}

// checkRename validates "rename oldClean to newClean" on its own. An empty string means it passed.
//
// It is pure string logic and never touches storage (easy to test); the caller checks "source missing" and "target exists".
// Two hard rules:
//   - never overwrite: a rename is not an overwrite; on object storage CopyObject silently replaces a same-named object with no rollback,
//     and the user usually just made a typo.
//   - never move into your own subtree: dir -> dir/sub copies successfully, then deleting the old prefix takes the
//     freshly moved content with it (S3 prefix matching), and locally it fails with an invalid argument - both must be blocked up front.
func checkRename(oldClean, newClean string) string {
	if oldClean == "" {
		return "path is required"
	}
	if newClean == "" {
		return "new_path is required"
	}
	if oldClean == newClean {
		return "the new name is the same as the old one"
	}
	if msg := checkEntryPath(newClean); msg != "" {
		return msg
	}
	if strings.HasPrefix(newClean, oldClean+"/") {
		return "cannot move a directory into its own subtree"
	}
	return ""
}

// entryExists reports whether anything (file or directory) exists at the path.
// S3 directories have no object, so Exists is always false; a List is also needed to see whether it holds entries,
// while a local Stat does report a directory; this covers both.
func entryExists(ctx context.Context, st storage.Storage, abs string) bool {
	if ex, _ := st.Exists(ctx, abs); ex {
		return true
	}
	entries, err := st.List(ctx, abs)
	return err == nil && len(entries) > 0
}

// ListAgentFiles lists the entries of a workspace directory (an empty path = the workspace root).
func ListAgentFiles(c *gin.Context) {
	st, wsKey, ok := agentWorkspace(c)
	if !ok {
		return
	}
	abs, clean, err := joinKey(wsKey, c.Query("path"))
	if err != nil {
		xhttp.ErrCode(c, "agent.invalid_path", nil, err.Error(), "")
		return
	}

	entries, lerr := st.List(context.Background(), abs)
	if lerr != nil {
		// A missing directory counts as an empty list (an empty directory does not exist on S3)
		xhttp.OkJSON(c, "Success", gin.H{"path": clean, "entries": []any{}}, "")
		return
	}

	items := make([]gin.H, 0, len(entries))
	for _, e := range entries {
		if strings.HasPrefix(e.Name, ".") { // skip hidden files (including the .keep placeholder)
			continue
		}
		p := path.Join(clean, e.Name)
		items = append(items, gin.H{
			"name":     e.Name,
			"path":     p,
			"is_dir":   e.IsDir,
			"readonly": isReadonlyRel(p),
			"size":     e.Size,
		})
	}
	xhttp.OkJSON(c, "Success", gin.H{"path": clean, "entries": items}, "")
}

// ReadAgentFile reads a file inside the workspace (skills/ is readable).
func ReadAgentFile(c *gin.Context) {
	st, wsKey, ok := agentWorkspace(c)
	if !ok {
		return
	}
	abs, clean, err := joinKey(wsKey, c.Query("path"))
	if err != nil {
		xhttp.ErrCode(c, "agent.invalid_path", nil, err.Error(), "")
		return
	}
	if clean == "" {
		xhttp.ErrCode(c, "agent.path_required", nil, "path is required", "")
		return
	}
	data, rerr := st.ReadFile(context.Background(), abs)
	if rerr != nil {
		xhttp.ErrCode(c, "agent.file_not_found", nil, "file not found", "")
		return
	}
	if len(data) > maxAgentFileBytes {
		xhttp.ErrCode(c, "agent.file_too_large", gin.H{"limit_mb": maxAgentFileBytes >> 20}, "file too large to edit", "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"path":     clean,
		"content":  string(data),
		"size":     len(data),
		"readonly": isReadonlyRel(clean),
	}, "")
}

// ============================================================================
// Session workspace file API ("workspace" = where one session runs).
//   Separate from the definition-domain file API above: these are products, not definitions.
//   Capabilities: list / read / download / upload / delete.
//   Deliberately **no** create and no in-place editing: the workspace belongs to the agent, and the UI only browses and feeds it;
//   another writer would race the running agent over the same unlocked directory, and a bad write is irreversible.
// ============================================================================

// sessionWorkspace resolves the working directory key of a session from the session_id query parameter.
//
// Ownership is checked the same way as in UploadSessionFile: the session must belong to the agent in the route.
// Without it anyone holding a session_id could borrow any agent's /session/* routes to read and write someone else's
// session workspace - the returned path is built from the session's real owner user, pointing straight into their directory.
func sessionWorkspace(c *gin.Context) (storage.Storage, string, bool) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return nil, "", false
	}
	info := authInfo.(*auth.AuthInfo)

	inst, ierr := repo.GetAgent(info, c.Param("id"))
	if ierr != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to get the agent: "+ierr.Error(), "")
		return nil, "", false
	}
	if inst == nil {
		xhttp.ErrCode(c, "agent.not_found", nil, "Agent not found", "")
		return nil, "", false
	}

	// Everything goes through lookupSessionForUser: empty / not a UUID / missing / not the owner
	// the four failures are decided at once and reported as one sentence (otherwise this becomes an API for probing whether a sid exists).
	sess, lerr := lookupSessionForUser(info, info.Schema, c.Query("session_id"))
	if lerr != nil {
		respondSessionErr(c, lerr, notOwnedSession)
		return nil, "", false
	}
	if sess.UserID == "" {
		xhttp.ErrCode(c, "agent.session_no_owner", nil, "session has no owner user", "")
		return nil, "", false
	}
	st, serr := getStorage()
	if serr != nil {
		xhttp.ErrCode(c, "agent.storage_unavailable", nil, "storage not ready: "+serr.Error(), "")
		return nil, "", false
	}
	return st, paths.UserSessionKey(info.Schema, sess.UserID, sess.ID), true
}

// ListSessionFiles lists the entries of the session working directory (an empty path = the directory root).
func ListSessionFiles(c *gin.Context) {
	st, wsKey, ok := sessionWorkspace(c)
	if !ok {
		return
	}
	abs, clean, err := joinKey(wsKey, c.Query("path"))
	if err != nil {
		xhttp.ErrCode(c, "agent.invalid_path", nil, err.Error(), "")
		return
	}

	entries, lerr := st.List(context.Background(), abs)
	if lerr != nil {
		// A missing directory counts as an empty list (an empty directory does not exist on S3)
		xhttp.OkJSON(c, "Success", gin.H{"path": clean, "entries": []any{}}, "")
		return
	}

	items := make([]gin.H, 0, len(entries))
	for _, e := range entries {
		if strings.HasPrefix(e.Name, ".") { // skip hidden files (including the .keep placeholder)
			continue
		}
		item := gin.H{
			"name":     e.Name,
			"path":     path.Join(clean, e.Name),
			"is_dir":   e.IsDir,
			"readonly": true, // content is never edited in the UI (download / delete are fine; uploads use the upload API)
			"size":     e.Size,
		}
		// S3 "directories" come from CommonPrefixes and have no modification time; a zero value is omitted and the front end treats it as empty
		if !e.ModTime.IsZero() {
			item["mod_time"] = e.ModTime
		}
		items = append(items, item)
	}
	xhttp.OkJSON(c, "Success", gin.H{"path": clean, "entries": items}, "")
}

// ReadSessionFile reads a file in the session working directory (read-only).
func ReadSessionFile(c *gin.Context) {
	st, wsKey, ok := sessionWorkspace(c)
	if !ok {
		return
	}
	abs, clean, err := joinKey(wsKey, c.Query("path"))
	if err != nil {
		xhttp.ErrCode(c, "agent.invalid_path", nil, err.Error(), "")
		return
	}
	if clean == "" {
		xhttp.ErrCode(c, "agent.path_required", nil, "path is required", "")
		return
	}
	data, rerr := st.ReadFile(context.Background(), abs)
	if rerr != nil {
		xhttp.ErrCode(c, "agent.file_not_found", nil, "file not found", "")
		return
	}
	if len(data) > maxAgentFileBytes {
		xhttp.ErrCode(c, "agent.file_too_large", gin.H{"limit_mb": maxAgentFileBytes >> 20}, "file too large to view", "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"path":     clean,
		"content":  string(data),
		"size":     len(data),
		"readonly": true,
	}, "")
}

// CreateSessionEntry creates a file or directory in the session workspace.
//
// It mirrors CreateAgentEntry in the definition domain, with three differences:
//   - no agentMutable gate: the session workspace is run output and stays writable even when the agent is deprecated;
//   - a new file may carry initial content - the panel offers no in-place editing (ReadSessionFile is always readonly),
//     so content can only be written once, at creation;
//   - names starting with . are rejected: ListSessionFiles skips them (that is how the .keep placeholder hides),
//     and allowing one would create something invisible in the list that still holds a key.
//
// Subdirectories: path may contain / (e.g. reports/2026) and the intermediate levels are created by the storage layer -
// a local WriteFile MkdirAll-s the parent chain, and on S3 a prefix is the directory.
// S3 has no empty directory: creating one writes a .keep placeholder object so it can be listed.
//
// A clash is always an error, never a silent rename: an upload feeds in a file (a clash saves as name-2),
// while here the user named it explicitly, and a quiet rename is worse than an error. To replace a file, delete it first (that asks for confirmation).
func CreateSessionEntry(c *gin.Context) {
	st, wsKey, ok := sessionWorkspace(c)
	if !ok {
		return
	}
	var body struct {
		Path    string `json:"path"`
		IsDir   bool   `json:"is_dir"`
		Content string `json:"content"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}
	abs, clean, err := joinKey(wsKey, body.Path)
	if err != nil {
		xhttp.ErrCode(c, "agent.invalid_path", nil, err.Error(), "")
		return
	}
	if msg := checkEntryPath(clean); msg != "" {
		xhttp.ErrCode(c, "agent.invalid_path", nil, msg, "")
		return
	}
	if !body.IsDir && len(body.Content) > maxAgentFileBytes {
		xhttp.ErrCode(c, "agent.file_too_large", gin.H{"limit_mb": maxAgentFileBytes >> 20}, fmt.Sprintf("content too large (limit %d MB)", maxAgentFileBytes>>20), "")
		return
	}
	ctx := context.Background()
	if ex, _ := st.Exists(ctx, abs); ex {
		xhttp.ErrCode(c, "agent.file_exists", gin.H{"path": clean}, "already exists", "")
		return
	}
	if !body.IsDir {
		// Directories take no space (the S3 .keep is zero bytes), so only writing a file goes through the quota
		if _, qerr := quota.Admit(ctx, st, authInfoSchema(c), wsKey, authInfoID(c), int64(len(body.Content))); qerr != nil {
			respondQuotaErr(c, qerr)
			return
		}
	}
	if body.IsDir {
		err = st.WriteFile(ctx, path.Join(abs, dirKeepFile), nil)
	} else {
		err = st.WriteFile(ctx, abs, []byte(body.Content))
	}
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to write the entry: "+err.Error(), "")
		return
	}
	if !body.IsDir {
		quota.Commit(ctx, authInfoSchema(c), wsKey, authInfoID(c), int64(len(body.Content)))
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"path":   clean,
		"is_dir": body.IsDir,
		"size":   len(body.Content),
	}, "")
}

// DeleteSessionEntry deletes a file or directory in the session workspace (cleaning up products).
//
// One key difference from DeleteAgentEntry in the definition domain: **no prefix is refused** here.
// The definition-domain skills/ is read-only because the skills table maintains it; everything in a session workspace (uploads/ included) is a product
// owned by the session owner and may be cleaned up.
//
// Note: deleting a directory is recursive and unrecoverable, so the UI must confirm and spell out the path.
func DeleteSessionEntry(c *gin.Context) {
	st, wsKey, ok := sessionWorkspace(c)
	if !ok {
		return
	}
	abs, clean, err := joinKey(wsKey, c.Query("path"))
	if err != nil {
		xhttp.ErrCode(c, "agent.invalid_path", nil, err.Error(), "")
		return
	}
	if clean == "" {
		xhttp.ErrCode(c, "agent.path_required", nil, "path is required", "")
		return
	}
	// Measure the size before deleting and give the usage back afterwards (otherwise deleting frees no quota)
	freed := entrySize(context.Background(), st, abs)
	// Delete as file and as directory (both are idempotent and tolerate a missing target):
	//   local: Delete = os.Remove (fails on a non-empty directory, ignore it), DeletePrefix = os.RemoveAll (handles both)
	//   S3   : Delete removes an object, DeletePrefix removes a prefix (directory)
	// Note: Exists cannot tell a file from a directory - a local os.Stat reports a directory as existing too.
	_ = st.Delete(context.Background(), abs)
	_ = st.DeletePrefix(context.Background(), abs)
	// Verify it is really gone (valid locally; on S3 a directory has no object and Exists is always false)
	if ex, _ := st.Exists(context.Background(), abs); ex {
		xhttp.ErrCode(c, "agent.delete_failed", nil, "delete failed: "+clean, "")
		return
	}
	if freed > 0 {
		quota.Commit(context.Background(), authInfoSchema(c), wsKey, authInfoID(c), -freed)
	}
	xhttp.OkJSON(c, "Success", gin.H{"path": clean}, "")
}

// entrySize estimates the bytes an entry occupies: Stat for a file, a recursive sum for a directory.
// It only gives quota back, so an imprecise estimate just makes the next calibration scan once more; correctness is unaffected.
func entrySize(ctx context.Context, st storage.Storage, key string) int64 {
	if sz, _, err := st.Stat(ctx, key); err == nil {
		return sz
	}
	w, ok := st.(storage.Walker)
	if !ok {
		return 0
	}
	var total int64
	_ = w.WalkPrefix(ctx, key, func(_ string, size int64, _ time.Time) error {
		total += size
		return nil
	})
	return total
}

// RenameSessionEntry renames / moves a file or directory in the session workspace (body: {path, new_path}).
//
// Why a dedicated endpoint instead of "download and create again": a create takes content as a string,
// and binary files (xlsx / zip / images / audio and video) cannot take that path, yet they are exactly what needs renaming.
//
// Unlike the definition domain, no prefix is refused here: everything in a session workspace is a product, uploads/ included.
func RenameSessionEntry(c *gin.Context) {
	st, wsKey, ok := sessionWorkspace(c)
	if !ok {
		return
	}
	var body struct {
		Path    string `json:"path"`
		NewPath string `json:"new_path"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}
	oldAbs, oldClean, err := joinKey(wsKey, body.Path)
	if err != nil {
		xhttp.ErrCode(c, "agent.invalid_path", nil, err.Error(), "")
		return
	}
	newAbs, newClean, err := joinKey(wsKey, body.NewPath)
	if err != nil {
		xhttp.ErrCode(c, "agent.invalid_path", nil, err.Error(), "")
		return
	}
	if msg := checkRename(oldClean, newClean); msg != "" {
		xhttp.ErrCode(c, "agent.invalid_rename", nil, msg, "")
		return
	}
	ctx := context.Background()
	if !entryExists(ctx, st, oldAbs) {
		xhttp.ErrCode(c, "agent.file_not_found", nil, "not found: "+oldClean, "")
		return
	}
	// Overwrites are always refused: see the checkRename comment
	if entryExists(ctx, st, newAbs) {
		xhttp.ErrCode(c, "agent.file_exists", gin.H{"path": newClean}, "already exists: "+newClean, "")
		return
	}
	if rerr := st.Rename(ctx, oldAbs, newAbs); rerr != nil {
		xhttp.ErrCode(c, "agent.rename_failed", nil, "rename failed: "+rerr.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"path": newClean}, "")
}

// DownloadSessionFile downloads a file from the session working directory as an attachment (authenticated direct call, for previews and scripts).
// Navigation-style browser downloads go through DownloadByTicket (a ticket, no Authorization header).
func DownloadSessionFile(c *gin.Context) {
	st, wsKey, ok := sessionWorkspace(c)
	if !ok {
		return
	}
	abs, clean, err := joinKey(wsKey, c.Query("path"))
	if err != nil {
		xhttp.ErrCode(c, "agent.invalid_path", nil, err.Error(), "")
		return
	}
	if clean == "" {
		xhttp.ErrCode(c, "agent.path_required", nil, "path is required", "")
		return
	}
	ServeDownload(c, st, abs, clean)
}

// ============================================================================
// Presigned downloads: switch + probe
//
//	presign_download: false                            -> off (default); S3 also streams through nexus
//	presign_download: true                             -> on, trusting the deployer's assertion
//	presign_download: true + presign_probe_key: <key>  -> auto, used only once the probe passes
//
// The three states are derived from two fields rather than an off/auto/on string: in YAML 1.1 on/off are booleans
// themselves, so decoding into a string needs an extra compatibility pass; and "is probe_key set" already says auto.
// ============================================================================

const (
	// presignRetryAfter is the cooldown after a failed probe: "unsupported" and "a temporary endpoint / credential fault"
	// cannot be told apart, and disabling for good would turn one blip into a permanent downgrade.
	presignRetryAfter = 10 * time.Minute
	// presignProbeFileName is the download name signed into the probe URL (pure ASCII; it verifies that the parameter is
	// honoured - the filename* encoding of non-ASCII names is covered separately by storage.contentDisposition).
	presignProbeFileName = "probe.txt"
	presignProbeTimeout  = 5 * time.Second
)

var (
	probeMu   sync.Mutex
	probeOK   bool
	probeNext time.Time // end of the cooldown after a failed probe
)

// presignAllowed reports whether presigned downloads are usable; false means the fallback (this process sends the bytes).
func presignAllowed() bool {
	h := config.Cfg.Agent.HTTP
	if !h.PresignDownload {
		return false
	}
	if config.Cfg.Agent.Storage.Driver != "s3" {
		return false // local / nfs have no externally reachable temporary link, so signing yields an empty string
	}
	if strings.TrimSpace(h.PresignProbeKey) == "" {
		return true // no probe configured: the deployer tested it, so trust the assertion
	}
	return presignProbePassed()
}

// presignProbePassed checks once, with a real object, whether this implementation honours "presign + response header override".
//
// Two points that cannot be skipped:
//  1. it must be GET. SigV4 signs the HTTP method into the canonical request, so sending HEAD to a GET presigned URL
//     returns 403 - the probe would misread "supported" as "unsupported". So the probe object must be small
//     (zero bytes is best), reading a single byte and stopping, at negligible cost.
//  2. besides status 200 the Content-Disposition must be checked: some implementations neither error nor override the header,
//     showing up as "downloads work but the file name is still garbled"; a status check alone misses that case.
func presignProbePassed() bool {
	probeMu.Lock()
	defer probeMu.Unlock()
	if probeOK {
		return true
	}
	if time.Now().Before(probeNext) {
		return false
	}
	fail := func(reason string) bool {
		probeNext = time.Now().Add(presignRetryAfter)
		xlog.Error("presign probe failed; falling back to nexus relay for %s: %s", presignRetryAfter, reason)
		return false
	}

	key := strings.TrimSpace(config.Cfg.Agent.HTTP.PresignProbeKey)
	st, serr := getStorage()
	if serr != nil {
		return fail("storage not ready: " + serr.Error())
	}
	u, perr := st.PresignedGetURL(context.Background(), key, config.Cfg.Agent.HTTP.PresignTTLDuration(), presignProbeFileName)
	if perr != nil {
		return fail("presign: " + perr.Error())
	}
	if u == "" {
		return fail("the backend does not support presigning")
	}

	req, rerr := http.NewRequest(http.MethodGet, u, nil)
	if rerr != nil {
		return fail("bad url: " + rerr.Error())
	}
	resp, derr := (&http.Client{Timeout: presignProbeTimeout}).Do(req)
	if derr != nil {
		return fail("request failed: " + derr.Error())
	}
	defer resp.Body.Close()
	_, _ = io.CopyN(io.Discard, resp.Body, 1) // read one byte only; do not pull the whole object

	if resp.StatusCode != http.StatusOK {
		return fail(fmt.Sprintf("HTTP %d", resp.StatusCode))
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(strings.ToLower(cd), "attachment") {
		return fail("the response header was not overridden (Content-Disposition=" + cd + ")")
	}
	probeOK = true
	xlog.Info("presign probe passed key=%s", key)
	return true
}

// presignDownload tries to hand byte delivery to the object store. A true return means the response is written; the caller just returns.
//
// Presigning is an accelerator, not a feature: when the backend returns an empty string (local/nfs) or signing fails it silently
// falls back to this process sending the bytes - never turns a download into a 500.
func presignDownload(c *gin.Context, st storage.Storage, abs, name string) bool {
	if !presignAllowed() {
		return false
	}
	u, err := st.PresignedGetURL(context.Background(), abs, config.Cfg.Agent.HTTP.PresignTTLDuration(), name)
	if err != nil || u == "" {
		return false
	}
	// no-store: keep the browser from caching this 302 - the ticket is single-use and the link expires
	c.Header("Cache-Control", "no-store")
	c.Redirect(http.StatusFound, u)
	return true
}

// ServeDownload is the shared download implementation, dispatching on deployment shape:
//  0. S3 + presign_download -> a 302 to the presigned URL, bytes sent by the object store (Range / resume come for free);
//  1. nginx (agent.http.x_accel_prefix set) -> X-Accel-Redirect, bytes sent by nginx;
//  2. local / nfs -> http.ServeContent (Range / 206 / If-Modified-Since included);
//  3. anything else (S3 with presigning off) -> a streaming relay without Range.
func ServeDownload(c *gin.Context, st storage.Storage, abs, clean string) {
	ctx := context.Background()
	name := path.Base(clean)

	// Fetch the metadata first: it feeds the size check and gives ServeContent a modTime (which also fills Content-Length)
	size, mod, serr := st.Stat(ctx, abs)
	if serr != nil {
		xhttp.ErrCode(c, "agent.file_not_found", nil, "file not found", "")
		return
	}
	limit := config.Cfg.Agent.HTTP.MaxDownloadBytes()
	if size > limit {
		xhttp.ErrCode(c, "agent.file_too_large", gin.H{"limit_mb": limit >> 20}, fmt.Sprintf("file too large (limit %d MB)", limit>>20), "")
		return
	}

	// 0) object-store presigning: a different delivery path is not a different rule set, so the size check comes first
	if presignDownload(c, st, abs, name) {
		return
	}

	// A non-ASCII name must use RFC 5987 filename*, or the browser saves it under a garbled name
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name))
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")

	if prefix := config.Cfg.Agent.HTTP.XAccelPrefix; prefix != "" {
		c.Header("X-Accel-Redirect", accelPath(prefix, abs))
		c.Status(http.StatusOK)
		return
	}

	if seekable, ok := st.(storage.SeekableStorage); ok {
		f, oerr := seekable.OpenSeekCloser(ctx, abs)
		if oerr != nil {
			xhttp.ErrCode(c, "agent.file_not_found", nil, "file not found", "")
			return
		}
		defer f.Close()
		http.ServeContent(c.Writer, c.Request, name, mod, f)
		return
	}

	rc, rerr := st.ReadStream(ctx, abs)
	if rerr != nil {
		xhttp.ErrCode(c, "agent.file_not_found", nil, "file not found", "")
		return
	}
	defer rc.Close()
	c.Header("Content-Length", strconv.FormatInt(size, 10))
	c.Status(http.StatusOK)
	// The response is already being written, so a failure ends here (a client disconnect is normal, not an error)
	_, _ = io.Copy(c.Writer, rc)
}

// accelPath turns a storage key into the URI for X-Accel-Redirect, escaping segment by segment.
// Escaping the whole string would encode "/" as %2F and nginx would not find the file.
func accelPath(prefix, key string) string {
	segs := strings.Split(strings.TrimPrefix(key, "/"), "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.TrimRight(prefix, "/") + "/" + strings.Join(segs, "/")
}

// WriteAgentFile writes or overwrites a file in the workspace (skills/ is refused).
func WriteAgentFile(c *gin.Context) {
	if !agentMutable(c) {
		return
	}
	st, wsKey, ok := agentWorkspace(c)
	if !ok {
		return
	}
	var body struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}
	abs, clean, err := joinKey(wsKey, body.Path)
	if err != nil {
		xhttp.ErrCode(c, "agent.invalid_path", nil, err.Error(), "")
		return
	}
	if clean == "" {
		xhttp.ErrCode(c, "agent.path_required", nil, "path is required", "")
		return
	}
	if isReadonlyRel(clean) {
		xhttp.ErrCode(c, "agent.skills_dir_readonly", nil, "skills directory is read-only", "")
		return
	}
	if len(body.Content) > maxAgentFileBytes {
		xhttp.ErrCode(c, "agent.file_too_large", gin.H{"limit_mb": maxAgentFileBytes >> 20}, "content too large", "")
		return
	}
	// ${global_vars.X} inside a document is expanded into the system prompt, so secret variables must be stopped at save
	// time - once inside AGENT.md they reach the model context and the run records (see globalvars.ValidateDocRefs).
	if strings.HasSuffix(strings.ToLower(clean), ".md") {
		if info, ok := authInfoOf(c); ok {
			if vErr := globalvars.ValidateDocRefs(info.Schema, body.Content); vErr != nil {
				xhttp.ErrCode(c, "agent.invalid_doc_refs", nil, vErr.Error(), "")
				return
			}
		}
	}
	if werr := st.WriteFile(context.Background(), abs, []byte(body.Content)); werr != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to write the file: "+werr.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"path": clean, "size": len(body.Content)}, "")
}

// DownloadAgentFile streams a file from the definition domain.
// Use: "drag to desktop" in the file list needs the raw bytes for a data: payload; a read-only editor does not forbid fetching the original.
func DownloadAgentFile(c *gin.Context) {
	st, wsKey, ok := agentWorkspace(c)
	if !ok {
		return
	}
	abs, clean, jerr := joinKey(wsKey, c.Query("path"))
	if jerr != nil {
		xhttp.ErrCode(c, "agent.invalid_path", nil, jerr.Error(), "")
		return
	}
	if clean == "" {
		xhttp.ErrCode(c, "agent.path_required", nil, "path is required", "")
		return
	}
	ServeDownload(c, st, abs, clean)
}

// CreateAgentEntry creates a file or directory (skills/ is refused).
// S3 has no empty directory: creating one writes a .keep placeholder object.
func CreateAgentEntry(c *gin.Context) {
	if !agentMutable(c) {
		return
	}
	st, wsKey, ok := agentWorkspace(c)
	if !ok {
		return
	}
	var body struct {
		Path  string `json:"path"`
		IsDir bool   `json:"is_dir"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "Invalid request body", "")
		return
	}
	abs, clean, err := joinKey(wsKey, body.Path)
	if err != nil {
		xhttp.ErrCode(c, "agent.invalid_path", nil, err.Error(), "")
		return
	}
	if clean == "" {
		xhttp.ErrCode(c, "agent.path_required", nil, "path is required", "")
		return
	}
	if isReadonlyRel(clean) {
		xhttp.ErrCode(c, "agent.skills_dir_readonly", nil, "skills directory is read-only", "")
		return
	}
	if ex, _ := st.Exists(context.Background(), abs); ex {
		xhttp.ErrCode(c, "agent.file_exists", gin.H{"path": clean}, "already exists", "")
		return
	}
	if body.IsDir {
		err = st.WriteFile(context.Background(), path.Join(abs, dirKeepFile), []byte{})
	} else {
		err = st.WriteFile(context.Background(), abs, []byte{})
	}
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to create the entry: "+err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"path": clean, "is_dir": body.IsDir}, "")
}

// UploadAgentFile uploads a file into a subdirectory of the agent definition domain (multipart: file + dir).
//
// It mirrors UploadSkillFile on the skill side, with two gates in between:
//   - agentMutable: an exposed endpoint freezes the definition domain, as in every other write API;
//   - skills/ is read-only: the skills table maintains that directory and the file API must not touch it.
//
// There is no uploads/ fallback: the definition root is the content root, and omitting dir means the root.
// No overwriting: save as name-2.ext - overwriting deletes an existing file, so delete first (that asks for confirmation) and upload again.
func UploadAgentFile(c *gin.Context) {
	if !agentMutable(c) {
		return
	}
	st, wsKey, ok := agentWorkspace(c)
	if !ok {
		return
	}
	fh, ferr := c.FormFile("file")
	if ferr != nil {
		xhttp.ErrCode(c, "agent.file_required", nil, "file is required", "")
		return
	}
	tooLarge := fmt.Sprintf("file too large (limit %d MB)", maxAgentFileBytes>>20)
	if fh.Size > maxAgentFileBytes {
		xhttp.ErrCode(c, "agent.file_too_large", gin.H{"limit_mb": maxAgentFileBytes >> 20}, tooLarge, "")
		return
	}
	// The destination depends on whether the dir field was submitted: absent = the definition root
	rawDir, _ := c.GetPostForm("dir")
	_, cleanDir, derr := joinKey(wsKey, strings.TrimSpace(rawDir))
	if derr != nil {
		xhttp.ErrCode(c, "agent.invalid_path", nil, derr.Error(), "")
		return
	}
	if cleanDir != "" && isReadonlyRel(cleanDir) {
		xhttp.ErrCode(c, "agent.skills_dir_readonly", nil, "the skills/ directory is read-only; edit it on the skill page", "")
		return
	}
	name := sanitizeUploadName(fh.Filename)
	if name == "" {
		xhttp.ErrCode(c, "agent.invalid_file_name", nil, "invalid file name", "")
		return
	}
	ctx := context.Background()
	rel := uniqueUploadRel(ctx, st, wsKey, cleanDir, name)
	// rel carries the dir prefix, which was already checked; this second check guards against future changes to uniqueUploadRel
	if isReadonlyRel(rel) {
		xhttp.ErrCode(c, "agent.skills_dir_readonly", nil, "the skills/ directory is read-only; edit it on the skill page", "")
		return
	}

	src, oerr := fh.Open()
	if oerr != nil {
		xhttp.ErrCode(c, "agent.upload_failed", nil, "failed to read the upload: "+oerr.Error(), "")
		return
	}
	defer src.Close()
	// Read one extra byte to cover the case where the declared size cannot be trusted
	data, rerr := io.ReadAll(io.LimitReader(src, maxAgentFileBytes+1))
	if rerr != nil {
		xhttp.ErrCode(c, "agent.upload_failed", nil, "failed to read the upload: "+rerr.Error(), "")
		return
	}
	if len(data) > maxAgentFileBytes {
		xhttp.ErrCode(c, "agent.file_too_large", gin.H{"limit_mb": maxAgentFileBytes >> 20}, tooLarge, "")
		return
	}
	if werr := st.WriteFile(ctx, path.Join(wsKey, rel), data); werr != nil {
		xhttp.ErrCode(c, "agent.upload_save_failed", nil, "failed to save the upload: "+werr.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"name": path.Base(rel),
		"path": rel,
		"size": len(data),
	}, "")
}

// DeleteAgentEntry deletes a file or directory in the workspace (skills/ is refused).
func DeleteAgentEntry(c *gin.Context) {
	if !agentMutable(c) {
		return
	}
	st, wsKey, ok := agentWorkspace(c)
	if !ok {
		return
	}
	abs, clean, err := joinKey(wsKey, c.Query("path"))
	if err != nil {
		xhttp.ErrCode(c, "agent.invalid_path", nil, err.Error(), "")
		return
	}
	if clean == "" {
		xhttp.ErrCode(c, "agent.path_required", nil, "path is required", "")
		return
	}
	if isReadonlyRel(clean) {
		xhttp.ErrCode(c, "agent.skills_dir_readonly", nil, "skills directory is read-only", "")
		return
	}
	// Delete as file and as directory (both are idempotent and tolerate a missing target):
	//   local: Delete = os.Remove (fails on a non-empty directory, ignore it), DeletePrefix = os.RemoveAll (handles both)
	//   S3   : Delete removes an object, DeletePrefix removes a prefix (directory)
	// Note: Exists cannot tell a file from a directory - a local os.Stat reports a directory as existing too.
	_ = st.Delete(context.Background(), abs)
	_ = st.DeletePrefix(context.Background(), abs)
	// Verify it is really gone (valid locally; on S3 a directory has no object and Exists is always false)
	if ex, _ := st.Exists(context.Background(), abs); ex {
		xhttp.ErrCode(c, "agent.delete_failed", nil, "delete failed: "+clean, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"path": clean}, "")
}

// ============================================================================
// Session workspace: upload (the "+" in the chat input)
//   Same destination as the browse APIs above (the session working directory); the difference is that this one **writes**.
// ============================================================================

// UploadSessionFile accepts multipart (file + optional session_id) and writes into uploads/ of the session workspace.
//
// When the session does not exist it is created first (a new chat where a file is chosen before the first message) and the session_id is returned for reuse,
// otherwise the file lands in a session directory that is then discarded while the agent's cwd is another one, and it can never be read.
func UploadSessionFile(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	info := authInfo.(*auth.AuthInfo)

	inst, err := repo.GetAgent(info, c.Param("id"))
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to get the agent: "+err.Error(), "")
		return
	}
	if inst == nil {
		xhttp.ErrCode(c, "agent.not_found", nil, "Agent not found", "")
		return
	}

	fh, ferr := c.FormFile("file")
	if ferr != nil {
		xhttp.ErrCode(c, "agent.file_required", nil, "file is required", "")
		return
	}
	tooLarge := fmt.Sprintf("file too large (limit %d MB)", maxSessionUploadBytes>>20)
	if fh.Size > maxSessionUploadBytes {
		xhttp.ErrCode(c, "agent.file_too_large", gin.H{"limit_mb": maxSessionUploadBytes >> 20}, tooLarge, "")
		return
	}

	sessRepo := sessions.NewRepository()
	sid := strings.TrimSpace(c.PostForm("session_id"))
	var sess *models.Session
	if sid == "" {
		// The old behaviour of the chat "+": create a session when none was passed
		sid, err = sessRepo.Create(info.Schema, models.SessionOwnerTypeAgent, inst.ID, info.ID, "")
		if err != nil {
			respondQuotaErr(c, err) // too many sessions -> 507
			return
		}
		sess, err = sessRepo.Get(info.Schema, sid)
		if err != nil {
			xhttp.ErrCode(c, "common.internal", nil, "failed to get the session: "+err.Error(), "")
			return
		}
	} else {
		// Everything goes through lookupSessionForUser: not a UUID / missing / not the owner
		// all map to the same response
		sess, err = lookupSessionForUser(info, info.Schema, sid)
		if err != nil {
			respondSessionErr(c, err, notOwnedSession)
			return
		}
	}
	if sess == nil {
		xhttp.ErrCode(c, "session.not_found", nil, notOwnedSession, "")
		return
	}
	if sess.UserID == "" {
		xhttp.ErrCode(c, "agent.session_no_owner", nil, "session has no owner user", "")
		return
	}

	st, serr := getStorage()
	if serr != nil {
		xhttp.ErrCode(c, "agent.storage_unavailable", nil, "storage not ready: "+serr.Error(), "")
		return
	}

	name := sanitizeUploadName(fh.Filename)
	if name == "" {
		xhttp.ErrCode(c, "agent.invalid_file_name", nil, "invalid file name", "")
		return
	}
	wsKey := paths.UserSessionKey(info.Schema, sess.UserID, sid)
	// Destination directory: the workspace panel passes the directory being browsed (what you see is where it goes), while the chat "+" passes none -> uploads/.
	// The test must be "was the dir field submitted", not "is the value empty": uploading at the root submits an empty string,
	// and testing the value would push root uploads into uploads/, so the shown destination and the real one would disagree.
	// It must pass joinKey, which cleans the path and blocks ../ escapes out of the session directory.
	rawDir, hasDir := c.GetPostForm("dir")
	if !hasDir {
		rawDir = uploadDir
	}
	dir := strings.TrimSpace(rawDir)
	_, cleanDir, derr := joinKey(wsKey, dir)
	if derr != nil {
		xhttp.ErrCode(c, "agent.invalid_path", nil, derr.Error(), "")
		return
	}
	rel := uniqueUploadRel(context.Background(), st, wsKey, cleanDir, name)

	src, oerr := fh.Open()
	if oerr != nil {
		xhttp.ErrCode(c, "agent.upload_failed", nil, "failed to read the upload: "+oerr.Error(), "")
		return
	}
	defer src.Close()
	// Read one extra byte to cover the case where the declared size cannot be trusted
	data, rerr := io.ReadAll(io.LimitReader(src, maxSessionUploadBytes+1))
	if rerr != nil {
		xhttp.ErrCode(c, "agent.upload_failed", nil, "failed to read the upload: "+rerr.Error(), "")
		return
	}
	if len(data) > maxSessionUploadBytes {
		xhttp.ErrCode(c, "agent.file_too_large", gin.H{"limit_mb": maxSessionUploadBytes >> 20}, tooLarge, "")
		return
	}
	// Quota preflight: when over the limit, evict this session's oldest products first and reject (507) only if that frees nothing.
	evicted, qerr := quota.Admit(context.Background(), st, info.Schema, wsKey, sess.UserID, int64(len(data)))
	if qerr != nil {
		respondQuotaErr(c, qerr)
		return
	}
	if werr := st.WriteFile(context.Background(), path.Join(wsKey, rel), data); werr != nil {
		xhttp.ErrCode(c, "agent.upload_save_failed", nil, "failed to save the upload: "+werr.Error(), "")
		return
	}
	// Book the usage only after a successful write (a failure books nothing, so no quota is held for a file that never landed)
	quota.Commit(context.Background(), info.Schema, wsKey, sess.UserID, int64(len(data)))
	if evicted == nil {
		evicted = []string{}
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"session_id": sid,
		"name":       path.Base(rel),
		"path":       rel,
		"size":       len(data),
		// Old files evicted by the session quota (relative to the session root), reported so the UI can inform the user
		"evicted": evicted,
	}, "")
}

// authInfoSchema returns the current tenant schema (needed to resolve quotas).
// By the time this runs sessionWorkspace has proven authInfo exists, so a miss can only be a regression.
func authInfoSchema(c *gin.Context) string {
	if v, ok := c.Get("authInfo"); ok {
		if info, ok2 := v.(*auth.AuthInfo); ok2 && info != nil {
			return info.Schema
		}
	}
	return ""
}

// authInfoID returns the current user id (used to resolve the per-user session quota).
// The session file APIs already prove ownership through lookupSessionForUser, so the logged-in user is enough here.
func authInfoID(c *gin.Context) string {
	if v, ok := c.Get("authInfo"); ok {
		if info, ok2 := v.(*auth.AuthInfo); ok2 && info != nil {
			return info.ID
		}
	}
	return ""
}

// respondQuotaErr turns a quota error into 507 (with scope/metric/used/limit),
// while other errors stay a plain 400. The front end uses the code to tell "out of space" from a bad parameter and offers a cleanup path.
func respondQuotaErr(c *gin.Context, err error) {
	var qe *quota.ExceededError
	if errors.As(err, &qe) {
		// 507 has two shapes (session space full / session count full) with different copy, so the error code lets the front end translate each
		xhttp.ErrQuotaCode(c, quotaErrCode(qe), nil, qe.Error(), gin.H{
			"scope":  qe.Scope,
			"metric": qe.Metric,
			"used":   qe.Used,
			"limit":  qe.Limit,
			// Files the pre-check already deleted: the UI says so, otherwise they vanish without explanation
			"evicted": qe.Evicted,
		}, "")
		return
	}
	xhttp.ErrCode(c, "common.internal", nil, "quota check failed: "+err.Error(), "")
}

// quotaErrCode maps a quota metric to a business code (see nexus/docs/error-codes.md).
func quotaErrCode(qe *quota.ExceededError) string {
	switch qe.Metric {
	case "bytes":
		return "quota.session_storage_exceeded"
	case "sessions":
		return "quota.user_sessions_exceeded"
	}
	return "quota.exceeded"
}

// sanitizeUploadName keeps only the file name itself (a browser may send the whole path), strips control characters and caps the length.
func sanitizeUploadName(raw string) string {
	name := path.Base(strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	// ".." must be rejected: path.Join(workspace, "uploads/..") collapses to the workspace root and writes outside uploads/
	if name == "" || name == "." || name == ".." || name == "/" {
		return ""
	}
	rs := []rune(name)
	if len(rs) > 120 {
		ext := path.Ext(name)
		tail := []rune(ext)
		if len(tail) >= 20 { // an absurdly long extension is dropped entirely rather than squeezing out the stem
			tail, ext = nil, ""
		}
		name = string(rs[:120-len(tail)]) + ext
	}
	return name
}

// uniqueUploadRel never overwrites: name, name-2.ext, name-3.ext, ...
// dir is a workspace-relative directory (the chat "+" passes uploads, the workspace panel passes the directory being browsed)
// and the returned value carries that prefix, so the caller just does path.Join(wsKey, rel).
//
// Overwriting is deliberately not done: it would delete a file the agent already produced, which is destructive
// and must not happen quietly inside an upload - to replace a file, delete it first (deleting asks for confirmation).
func uniqueUploadRel(ctx context.Context, st storage.Storage, wsKey, dir, name string) string {
	taken := map[string]bool{}
	if entries, err := st.List(ctx, path.Join(wsKey, dir)); err == nil {
		for _, e := range entries {
			taken[e.Name] = true
		}
	}
	if !taken[name] {
		return path.Join(dir, name)
	}
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 2; i < 1000; i++ {
		cand := fmt.Sprintf("%s-%d%s", stem, i, ext)
		if !taken[cand] {
			return path.Join(dir, cand)
		}
	}
	return path.Join(dir, fmt.Sprintf("%d-%s", time.Now().UnixNano(), name))
}

// attachmentPrompt builds the note the model sees for this turn's attachments; an empty string when none is usable.
//
// Only workspace-relative paths under uploads/ count: escapes (../, absolute paths) and missing files are skipped -
// an error would make normal conversations brittle, but pointing the model at a missing file is worse.
func attachmentPrompt(st storage.Storage, schema, userID, sessionID string, files []string) string {
	if st == nil || userID == "" || len(files) == 0 {
		return ""
	}
	wsKey := paths.UserSessionKey(schema, userID, sessionID)
	lines := make([]string, 0, len(files))
	for _, f := range files {
		clean, cerr := tools.SafePath(f)
		if cerr != nil || clean == "" || !strings.HasPrefix(clean, uploadDir+"/") {
			continue
		}
		if ok, _ := st.Exists(context.Background(), path.Join(wsKey, clean)); !ok {
			continue
		}
		lines = append(lines, "- "+clean)
	}
	if len(lines) == 0 {
		return ""
	}
	return "[User-uploaded files] are in your workspace (paths are relative to your workspace root, which is also the exec cwd):\n" +
		strings.Join(lines, "\n") +
		"\nRead them with tools such as read_file / list_files when needed; do not guess their content from the file name."
}

// ============================================================================
// Workspace "recent files": the root plus one level of subdirectories, newest modification first
// ============================================================================

// RecentFileItem is one entry of the recent files list (path is relative to the session workspace).
type RecentFileItem struct {
	Path    string    `json:"path"`
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

// RecentSessionFiles lists recently changed / generated files in the session workspace, for the workbench "newly generated" tab.
//
// It scans the root and one level only: an agent's output mostly lands in the root or shallow directories such as uploads/ and outputs/,
// and every extra level means multiples of LIST requests (billed per call on S3) for little gain.
func RecentSessionFiles(c *gin.Context) {
	st, wsKey, ok := sessionWorkspace(c)
	if !ok {
		return
	}
	limit := 30
	if v, aerr := strconv.Atoi(c.Query("limit")); aerr == nil && v > 0 {
		limit = v
	}
	if limit > 100 {
		limit = 100
	}

	ctx := context.Background()
	files := make([]RecentFileItem, 0, limit)
	scanned := 0

	var collect func(dir, prefix string, depth int)
	collect = func(dir, prefix string, depth int) {
		entries, lerr := st.List(ctx, dir)
		if lerr != nil {
			return // missing directory / read failure: treat this level as empty
		}
		for _, e := range entries {
			if scanned >= maxRecentScanEntries {
				return
			}
			if strings.HasPrefix(e.Name, ".") { // skip hidden files (including the .keep placeholder)
				continue
			}
			scanned++
			rel := path.Join(prefix, e.Name)
			if e.IsDir {
				if depth == 0 {
					collect(path.Join(dir, e.Name), rel, 1)
				}
				continue
			}
			files = append(files, RecentFileItem{Path: rel, Name: e.Name, Size: e.Size, ModTime: e.ModTime})
		}
	}
	collect(wsKey, "", 0)

	// Newest first; when timestamps tie (written in the same second) sort by path for a stable order
	sort.Slice(files, func(i, j int) bool {
		if files[i].ModTime.Equal(files[j].ModTime) {
			return files[i].Path < files[j].Path
		}
		return files[i].ModTime.After(files[j].ModTime)
	})
	if len(files) > limit {
		files = files[:limit]
	}
	xhttp.OkJSON(c, "Success", gin.H{"files": files, "scanned": scanned}, "")
}
