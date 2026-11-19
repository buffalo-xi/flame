package skills

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"flame/nexus/admin/agents"
	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/auth"
	"flame/nexus/shared/globalvars"
	"flame/nexus/shared/storage"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// ============================================================================
// File management for a skill's content directory (isomorphic to the agent
// workspace file endpoints, only the root differs)
//   tenant-level skill: tenants/<schema>/skills/<name>/
//   agent-private     : tenants/<schema>/agents/<agentName>/skills/<name>/
//   everything goes through the storage abstraction (local / nfs / s3)
//   entry file SKILL.md: editable, but never deletable (the runtime reads the
//   skill body from it)
// ============================================================================

const (
	// maxSkillFileBytes caps a single file read or write (same as the agent side, so a large file never lands in the editor).
	maxSkillFileBytes = 2 << 20 // 2 MiB
	// skillDirKeepFile: S3 has no empty directories, so creating one writes a placeholder object that makes it enumerable.
	skillDirKeepFile = ".keep"
	// skillEntryFile is the skill entry file (a runtime read convention; never deletable).
	skillEntryFile = "SKILL.md"
	// skillSubdirName is the subdirectory of agent-private skills inside an agent workspace (matches the constant inside the tools package).
	skillSubdirName = "skills"
)

// skillRoot returns the storage instance and the relative key of the skill content directory; on
// failure the response is already written.
// With forWrite=true it also passes the owning agent's status gate: once the owning agent is
// deprecated (3), every private skill under it freezes. The read path passes false - deprecated
// stays readable (D4), and reads have no side effects.
func skillRoot(c *gin.Context, forWrite bool) (storage.Storage, string, bool) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return nil, "", false
	}
	info := authInfo.(*auth.AuthInfo)

	s, err := skillRepo.Get(info.Schema, c.Param("id"))
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to get the skill: "+err.Error(), "")
		return nil, "", false
	}
	if s == nil {
		xhttp.ErrCode(c, "skill.not_found", nil, "Skill not found", "")
		return nil, "", false
	}
	// The write path passes a double status gate: the owning agent plus the skill itself (the same
	// rule as Update / Delete in admin.go, the meta-tool trio and tools/gate.go). The read path is
	// never gated (D4).
	if forWrite {
		if verr := ensureAgentModifiable(info, string(s.AgentID)); verr != nil {
			xhttp.ErrCode(c, "skill.owner_agent_not_modifiable", nil, verr.Error(), "")
			return nil, "", false
		}
		if !s.IsModifiable() {
			xhttp.ErrCode(c, "skill.deprecated", nil, "the skill is deprecated and cannot be modified (switch it back to normal first)", "")
			return nil, "", false
		}
	}
	st, serr := getStorage()
	if serr != nil {
		xhttp.ErrCode(c, "skill.storage_unavailable", nil, "storage not ready: "+serr.Error(), "")
		return nil, "", false
	}

	agentName, aerr := resolveAgentName(info.Schema, string(s.AgentID))
	if aerr != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to resolve the owner agent: "+aerr.Error(), "")
		return nil, "", false
	}
	return st, skillDirKey(info.Schema, agentName, s.Name), true
}

// skillDirKey returns the relative key of a skill content directory (an empty agentName means a
// tenant-level shared skill).
func skillDirKey(schema, agentName, name string) string {
	if agentName == "" {
		return path.Join(tools.SkillsKey(schema), name)
	}
	return path.Join(tools.AgentsKey(schema, agentName), skillSubdirName, name)
}

// joinSkillKey joins a path inside the skill directory (with traversal checks) and returns the
// full key plus the relative path.
func joinSkillKey(rootKey, rel string) (string, string, error) {
	clean, err := tools.SafePath(rel)
	if err != nil {
		return "", "", err
	}
	if clean == "" {
		return rootKey, "", nil
	}
	return path.Join(rootKey, clean), clean, nil
}

// isSkillEntry reports whether a relative path is the entry file SKILL.md.
func isSkillEntry(clean string) bool {
	return strings.EqualFold(clean, skillEntryFile)
}

// ListSkillFiles lists the entries of a skill content directory (an empty path means the root).
func ListSkillFiles(c *gin.Context) {
	st, rootKey, ok := skillRoot(c, false)
	if !ok {
		return
	}
	abs, clean, err := joinSkillKey(rootKey, c.Query("path"))
	if err != nil {
		xhttp.ErrCode(c, "skill.invalid_path", nil, err.Error(), "")
		return
	}

	entries, lerr := st.List(context.Background(), abs)
	if lerr != nil {
		// A missing directory counts as an empty list (on S3 an empty directory does not exist at all)
		xhttp.OkJSON(c, "Success", gin.H{"path": clean, "entries": []any{}}, "")
		return
	}

	items := make([]gin.H, 0, len(entries))
	for _, e := range entries {
		if strings.HasPrefix(e.Name, ".") { // skip hidden files (including .keep placeholders)
			continue
		}
		p := path.Join(clean, e.Name)
		items = append(items, gin.H{
			"name":     e.Name,
			"path":     p,
			"is_dir":   e.IsDir,
			"readonly": isSkillEntry(p), // entry file: editable but not deletable
			"size":     e.Size,
		})
	}
	xhttp.OkJSON(c, "Success", gin.H{"path": clean, "entries": items}, "")
}

// ReadSkillFile reads a file inside the skill directory.
func ReadSkillFile(c *gin.Context) {
	st, rootKey, ok := skillRoot(c, false)
	if !ok {
		return
	}
	abs, clean, err := joinSkillKey(rootKey, c.Query("path"))
	if err != nil {
		xhttp.ErrCode(c, "skill.invalid_path", nil, err.Error(), "")
		return
	}
	if clean == "" {
		xhttp.ErrCode(c, "skill.path_required", nil, "path is required", "")
		return
	}
	data, rerr := st.ReadFile(context.Background(), abs)
	if rerr != nil {
		xhttp.ErrCode(c, "skill.file_not_found", nil, "file not found", "")
		return
	}
	if len(data) > maxSkillFileBytes {
		xhttp.ErrCode(c, "skill.file_too_large", gin.H{"limit_mb": maxSkillFileBytes >> 20}, "file too large to edit", "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"path":     clean,
		"content":  string(data),
		"size":     len(data),
		"readonly": false, // the entry file is editable too; only deleting it is refused
	}, "")
}

// WriteSkillFile writes or overwrites a file inside the skill directory.
func WriteSkillFile(c *gin.Context) {
	st, rootKey, ok := skillRoot(c, true)
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
	abs, clean, err := joinSkillKey(rootKey, body.Path)
	if err != nil {
		xhttp.ErrCode(c, "skill.invalid_path", nil, err.Error(), "")
		return
	}
	if clean == "" {
		xhttp.ErrCode(c, "skill.path_required", nil, "path is required", "")
		return
	}
	if len(body.Content) > maxSkillFileBytes {
		xhttp.ErrCode(c, "skill.file_too_large", gin.H{"limit_mb": maxSkillFileBytes >> 20}, "content too large", "")
		return
	}
	// A ${global_vars.X} in SKILL.md travels into the model context with the body, so secret types
	// must be blocked at save time (see globalvars.ValidateDocRefs). Non-.md files (scripts, data)
	// never go through text substitution and are not checked.
	if strings.HasSuffix(strings.ToLower(clean), ".md") {
		if v, exists := c.Get("authInfo"); exists {
			if info, ok := v.(*auth.AuthInfo); ok && info != nil {
				if vErr := globalvars.ValidateDocRefs(info.Schema, body.Content); vErr != nil {
					xhttp.ErrCode(c, "skill.invalid_doc_refs", nil, vErr.Error(), "")
					return
				}
			}
		}
	}
	if werr := st.WriteFile(context.Background(), abs, []byte(body.Content)); werr != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to write the file: "+werr.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"path": clean, "size": len(body.Content)}, "")
}

// CreateSkillEntry creates a file or directory (S3 has no empty directories, so creating one
// writes a .keep placeholder).
func CreateSkillEntry(c *gin.Context) {
	st, rootKey, ok := skillRoot(c, true)
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
	abs, clean, err := joinSkillKey(rootKey, body.Path)
	if err != nil {
		xhttp.ErrCode(c, "skill.invalid_path", nil, err.Error(), "")
		return
	}
	if clean == "" {
		xhttp.ErrCode(c, "skill.path_required", nil, "path is required", "")
		return
	}
	if isSkillEntry(clean) {
		xhttp.ErrCode(c, "skill.file_exists", gin.H{"path": clean}, skillEntryFile+" already exists", "")
		return
	}
	if ex, _ := st.Exists(context.Background(), abs); ex {
		xhttp.ErrCode(c, "skill.file_exists", gin.H{"path": clean}, "already exists", "")
		return
	}
	if body.IsDir {
		err = st.WriteFile(context.Background(), path.Join(abs, skillDirKeepFile), []byte{})
	} else {
		err = st.WriteFile(context.Background(), abs, []byte{})
	}
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to create the entry: "+err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"path": clean, "is_dir": body.IsDir}, "")
}

// DeleteSkillEntry deletes a file or directory inside the skill directory (the SKILL.md entry
// file is refused).
func DeleteSkillEntry(c *gin.Context) {
	st, rootKey, ok := skillRoot(c, true)
	if !ok {
		return
	}
	abs, clean, err := joinSkillKey(rootKey, c.Query("path"))
	if err != nil {
		xhttp.ErrCode(c, "skill.invalid_path", nil, err.Error(), "")
		return
	}
	if clean == "" {
		xhttp.ErrCode(c, "skill.path_required", nil, "path is required", "")
		return
	}
	if isSkillEntry(clean) {
		xhttp.ErrCode(c, "skill.entry_undeletable", gin.H{"file": skillEntryFile}, skillEntryFile+" is the skill entry and cannot be deleted", "")
		return
	}
	// Delete as a file and as a directory (both are idempotent and tolerate absence):
	//   local: Delete = os.Remove (fails on a non-empty directory, fine to ignore), DeletePrefix = os.RemoveAll (handles both)
	//   S3   : Delete removes an object, DeletePrefix removes a prefix (directory)
	_ = st.Delete(context.Background(), abs)
	_ = st.DeletePrefix(context.Background(), abs)
	if ex, _ := st.Exists(context.Background(), abs); ex {
		xhttp.ErrCode(c, "skill.delete_failed", nil, "delete failed: "+clean, "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"path": clean}, "")
}

// UploadSkillFile uploads a file into a subdirectory of the skill content directory (multipart:
// file + dir).
//
// The semantics match UploadSessionFile for session workspaces, with two differences:
//   - no uploads/ fallback: the skill directory is the content root itself, so a missing (or
//     empty) dir means the skill root;
//   - the cap is maxSkillFileBytes (2 MiB, the same as the editor), not the session's 20 MiB.
//
// A name clash never overwrites: it is saved as name-2.ext - overwriting would mean deleting an
// existing skill file, which has to go through delete (the UI asks for confirmation) and must not
// happen quietly inside an upload.
func UploadSkillFile(c *gin.Context) {
	st, rootKey, ok := skillRoot(c, true)
	if !ok {
		return
	}
	fh, ferr := c.FormFile("file")
	if ferr != nil {
		xhttp.ErrCode(c, "skill.file_required", nil, "file is required", "")
		return
	}
	tooLarge := fmt.Sprintf("file too large (limit %d MB)", maxSkillFileBytes>>20)
	if fh.Size > maxSkillFileBytes {
		xhttp.ErrCode(c, "skill.file_too_large", gin.H{"limit_mb": maxSkillFileBytes >> 20}, tooLarge, "")
		return
	}
	// The target is decided by whether a dir field was submitted: absent means the skill root
	// (there is deliberately no uploads/ fallback here)
	rawDir, _ := c.GetPostForm("dir")
	_, cleanDir, derr := joinSkillKey(rootKey, strings.TrimSpace(rawDir))
	if derr != nil {
		xhttp.ErrCode(c, "skill.invalid_path", nil, derr.Error(), "")
		return
	}
	name := sanitizeSkillFileName(fh.Filename)
	if name == "" {
		xhttp.ErrCode(c, "skill.invalid_file_name", nil, "invalid file name", "")
		return
	}
	rel := uniqueSkillRel(context.Background(), st, rootKey, cleanDir, name)

	src, oerr := fh.Open()
	if oerr != nil {
		xhttp.ErrCode(c, "skill.upload_failed", nil, "failed to read the upload: "+oerr.Error(), "")
		return
	}
	defer src.Close()
	// Read one byte past the limit to catch the extreme case where the declared size cannot be trusted
	data, rerr := io.ReadAll(io.LimitReader(src, maxSkillFileBytes+1))
	if rerr != nil {
		xhttp.ErrCode(c, "skill.upload_failed", nil, "failed to read the upload: "+rerr.Error(), "")
		return
	}
	if len(data) > maxSkillFileBytes {
		xhttp.ErrCode(c, "skill.file_too_large", gin.H{"limit_mb": maxSkillFileBytes >> 20}, tooLarge, "")
		return
	}
	if werr := st.WriteFile(context.Background(), path.Join(rootKey, rel), data); werr != nil {
		xhttp.ErrCode(c, "skill.upload_save_failed", nil, "failed to save the upload: "+werr.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"name": path.Base(rel),
		"path": rel,
		"size": len(data),
	}, "")
}

// sanitizeSkillFileName keeps only the file name itself (a browser may send a full path), strips
// control characters and bounds the length.
// It is the same function as admin/agents' sanitizeUploadName; each package keeps its own copy so
// a pure string helper does not justify a cross-package dependency.
func sanitizeSkillFileName(raw string) string {
	name := path.Base(strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	// ".." must be blocked: path.Join(skillRoot, "..") collapses outside the skill directory
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

// uniqueSkillRel never overwrites on a name clash: name, name-2.ext, name-3.ext ...
// dir is a relative directory inside the skill directory and is included in the return value, so
// the caller can simply path.Join(rootKey, rel).
func uniqueSkillRel(ctx context.Context, st storage.Storage, rootKey, dir, name string) string {
	taken := map[string]bool{}
	if entries, err := st.List(ctx, path.Join(rootKey, dir)); err == nil {
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

// DownloadSkillFile streams a file from the skill content directory (the byte entry point for
// dragging it out to the desktop).
// It reuses agents' ServeDownload directly: the four-way dispatch (presigned / X-Accel /
// ServeContent / streaming) is tightly coupled to the deployment shape, and a second copy would
// drift apart sooner or later; the skills package does not depend on agents in reverse, so there
// is no cycle.
func DownloadSkillFile(c *gin.Context) {
	st, rootKey, ok := skillRoot(c, false)
	if !ok {
		return
	}
	abs, clean, jerr := joinSkillKey(rootKey, c.Query("path"))
	if jerr != nil {
		xhttp.ErrCode(c, "skill.invalid_path", nil, jerr.Error(), "")
		return
	}
	if clean == "" {
		xhttp.ErrCode(c, "skill.path_required", nil, "path is required", "")
		return
	}
	agents.ServeDownload(c, st, abs, clean)
}
