package customtools

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/models"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// ============================================================================
// File management for a custom tool's **content directory** (mirrors admin/skills/files.go).
//
//	host directory: {root_dir}/tenants/<schema>/tools/<name>/
//	inside the sandbox: mounted read-only at /tool, and the entry point starts here
//
// Two differences from the skill side, both because the directory is the entity:
//   - TOOL.json is the **contract of record**: readable, **not writable and not deletable**. The only writers are create / update
//     (they write the file and the table row together and run Validate), so raw writes would bypass validation
//     and let the file and the table row drift apart.
//   - the entry file (entry in TOOL.json): readable and writable, **not deletable**. Delete it and the tool is still
//     in the directory, but every run reports a missing entry: half dead.
//
// Deliberately not going through the storage abstraction: the executor needs the host path as the sandbox mount source (tools.RunCustomToolSpec
// calls os.Stat on this directory), and toolfile reads and writes TOOL.json with os too. Going through storage would make an S3 deployment
// show the files in the editor yet unable to run them, plus two views of the same directory. This path never supported
// custom tools on S3 anyway (see the same error in run_skill_script).
// ============================================================================

const (
	// maxToolFileBytes caps a single file read or write (same as the skill side).
	maxToolFileBytes = 2 << 20
	// toolKeepFile is the placeholder written into a new empty directory (the local backend really creates the directory; it stays for
	// parity with the skill side, and so listing never treats an empty directory as missing).
	toolKeepFile = ".keep"
)

// toolFileRoot locates the tool directory and returns (host dir, relative entry, ok).
//
// forWrite=true passes the status gate: locked (2) and deprecated (3) always reject writes (same rule as tools/gate.go,
// the meta-tool trio and the skill file API). Read paths are not gated: viewing needs no justification.
func toolFileRoot(c *gin.Context, forWrite bool) (string, string, bool) {
	info, ok := mustAuth(c)
	if !ok {
		return "", "", false
	}
	name := c.Param("name")
	// Name gate: name from the URL is joined into a host path, so it must pass the creation-time rules.
	if !tools.ValidName(name) || strings.Contains(name, "__") {
		xhttp.ErrCode(c, "tool.invalid_name", gin.H{"name": name}, "invalid tool name", "")
		return "", "", false
	}

	dir := toolDir(info.Schema, name)
	st, serr := os.Stat(dir)
	if serr != nil || !st.IsDir() {
		xhttp.ErrCode(c, "tool.not_found", gin.H{"name": name}, fmt.Sprintf("custom tool directory does not exist: tools/%s/", name), "")
		return "", "", false
	}

	store := storeFor(info.Schema)
	status, found, err := store.Status(name)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to read tool status: "+err.Error(), "")
		return "", "", false
	}
	if forWrite {
		if !found {
			xhttp.ErrCode(c, "tool.invalid_contract", gin.H{"name": name},
				"this directory is not a valid tool yet (TOOL.json missing or contract invalid): create it with \"new custom tool\" first, or fix TOOL.json", "")
			return "", "", false
		}
		if !models.IsModifiable(status) {
			xhttp.ErrCode(c, "tool.deprecated", gin.H{"name": name}, "this tool is locked or deprecated and cannot be modified (switch it back to \"normal\" in the list)", "")
			return "", "", false
		}
	}

	entry := ""
	if t, gerr := store.Get(info.Schema, name); gerr == nil && t != nil {
		entry = path.Clean(strings.ReplaceAll(t.Entry, "\\", "/"))
		if entry == "." {
			entry = ""
		}
	}
	return dir, entry, true
}

// joinToolFile joins a path relative to the tool directory into an absolute host path (with traversal checks).
func joinToolFile(root, rel string) (string, string, error) {
	if strings.TrimSpace(rel) == "" {
		return root, "", nil
	}
	clean, err := tools.SafePath(rel)
	if err != nil {
		return "", "", err
	}
	if clean == "" {
		return root, "", nil
	}
	return filepath.Join(root, filepath.FromSlash(clean)), clean, nil
}

// isToolContract reports whether the path is the TOOL.json contract file (case-insensitive, both spellings count on Windows).
func isToolContract(rel string) bool {
	return strings.EqualFold(path.Clean(rel), models.ToolFileName)
}

// isToolEntry reports whether the path is the entry file declared in TOOL.json.
func isToolEntry(rel, entry string) bool {
	if entry == "" || rel == "" {
		return false
	}
	return strings.EqualFold(path.Clean(rel), path.Clean(entry))
}

// ListToolFiles GET /customtools/:name/files?path=
func ListToolFiles(c *gin.Context) {
	root, entry, ok := toolFileRoot(c, false)
	if !ok {
		return
	}
	abs, clean, err := joinToolFile(root, c.Query("path"))
	if err != nil {
		xhttp.ErrCode(c, "tool.invalid_path", nil, err.Error(), "")
		return
	}
	entries, lerr := os.ReadDir(abs)
	if lerr != nil {
		// A missing directory is an empty list (same as the skill side): "no subdirectory yet" is not an error.
		xhttp.OkJSON(c, "Success", gin.H{"path": clean, "entries": []any{}, "entry": entry}, "")
		return
	}

	items := make([]gin.H, 0, len(entries))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") { // skip hidden files (including the .keep placeholder)
			continue
		}
		p := path.Join(clean, e.Name())
		var size int64
		if fi, ierr := e.Info(); ierr == nil {
			size = fi.Size()
		}
		items = append(items, gin.H{
			"name":   e.Name(),
			"path":   p,
			"is_dir": e.IsDir(),
			"size":   size,
			// readonly = not deletable (the entry file); locked = content frozen too (TOOL.json)
			"readonly": isToolContract(p) || isToolEntry(p, entry),
			"locked":   isToolContract(p),
		})
	}
	xhttp.OkJSON(c, "Success", gin.H{"path": clean, "entries": items, "entry": entry}, "")
}

// ReadToolFile GET /customtools/:name/file?path=
func ReadToolFile(c *gin.Context) {
	root, entry, ok := toolFileRoot(c, false)
	if !ok {
		return
	}
	abs, clean, err := joinToolFile(root, c.Query("path"))
	if err != nil {
		xhttp.ErrCode(c, "tool.invalid_path", nil, err.Error(), "")
		return
	}
	if clean == "" {
		xhttp.ErrCode(c, "tool.path_required", nil, "path is required", "")
		return
	}
	st, serr := os.Stat(abs)
	if serr != nil || st.IsDir() {
		xhttp.ErrCode(c, "tool.file_not_found", nil, "file not found", "")
		return
	}
	if st.Size() > maxToolFileBytes {
		xhttp.ErrCode(c, "tool.file_too_large", gin.H{"limit_mb": maxToolFileBytes >> 20}, "file too large to edit", "")
		return
	}
	data, rerr := os.ReadFile(abs)
	if rerr != nil {
		xhttp.ErrCode(c, "tool.file_not_found", nil, "file not found", "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{
		"path":     clean,
		"content":  string(data),
		"size":     len(data),
		"readonly": isToolContract(clean) || isToolEntry(clean, entry),
		"locked":   isToolContract(clean),
	}, "")
}

// WriteToolFile PUT /customtools/:name/file
func WriteToolFile(c *gin.Context) {
	root, _, ok := toolFileRoot(c, true)
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
	abs, clean, err := joinToolFile(root, body.Path)
	if err != nil {
		xhttp.ErrCode(c, "tool.invalid_path", nil, err.Error(), "")
		return
	}
	if clean == "" {
		xhttp.ErrCode(c, "tool.path_required", nil, "path is required", "")
		return
	}
	if isToolContract(clean) {
		// The contract has exactly one write path: update_custom_tool / the form (it writes file and table row together and runs Validate).
		xhttp.ErrCode(c, "tool.contract_readonly", gin.H{"file": models.ToolFileName},
			"TOOL.json is the tool contract; edit it with the form above (writing the file directly bypasses validation and diverges from the table row)", "")
		return
	}
	if len(body.Content) > maxToolFileBytes {
		xhttp.ErrCode(c, "tool.file_too_large", gin.H{"limit_mb": maxToolFileBytes >> 20}, "content too large", "")
		return
	}
	if st, serr := os.Stat(abs); serr == nil && st.IsDir() {
		xhttp.ErrCode(c, "tool.path_is_dir", gin.H{"path": clean}, "the target is a directory and cannot be written as a file", "")
		return
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to create directory: "+err.Error(), "")
		return
	}
	if werr := os.WriteFile(abs, []byte(body.Content), 0o644); werr != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to write the file: "+werr.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"path": clean, "size": len(body.Content)}, "")
}

// CreateToolEntry POST /customtools/:name/file - creates a file or a directory.
func CreateToolEntry(c *gin.Context) {
	root, entry, ok := toolFileRoot(c, true)
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
	abs, clean, err := joinToolFile(root, body.Path)
	if err != nil {
		xhttp.ErrCode(c, "tool.invalid_path", nil, err.Error(), "")
		return
	}
	if clean == "" {
		xhttp.ErrCode(c, "tool.path_required", nil, "path is required", "")
		return
	}
	if isToolContract(clean) {
		xhttp.ErrCode(c, "tool.contract_readonly", gin.H{"file": models.ToolFileName},
			models.ToolFileName+" is generated by \"new custom tool\" and cannot be created by hand", "")
		return
	}
	if isToolEntry(clean, entry) {
		xhttp.ErrCode(c, "tool.entry_exists", gin.H{"entry": entry}, "this is the current entry file and already exists; to change the entry, edit entry in the form", "")
		return
	}
	if _, serr := os.Stat(abs); serr == nil {
		xhttp.ErrCode(c, "tool.file_exists", gin.H{"path": clean}, "already exists", "")
		return
	}
	if body.IsDir {
		if err = os.MkdirAll(abs, 0o755); err == nil {
			// Placeholder file, same as the skill side, so "does an empty directory count as existing" has one answer.
			err = os.WriteFile(filepath.Join(abs, toolKeepFile), []byte{}, 0o644)
		}
	} else {
		if merr := os.MkdirAll(filepath.Dir(abs), 0o755); merr != nil {
			xhttp.ErrCode(c, "common.internal", nil, "failed to create directory: "+merr.Error(), "")
			return
		}
		err = os.WriteFile(abs, []byte{}, 0o644)
	}
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to create the entry: "+err.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"path": clean, "is_dir": body.IsDir}, "")
}

// DeleteToolEntry DELETE /customtools/:name/file?path=
func DeleteToolEntry(c *gin.Context) {
	root, entry, ok := toolFileRoot(c, true)
	if !ok {
		return
	}
	abs, clean, err := joinToolFile(root, c.Query("path"))
	if err != nil {
		xhttp.ErrCode(c, "tool.invalid_path", nil, err.Error(), "")
		return
	}
	if clean == "" {
		xhttp.ErrCode(c, "tool.path_required", nil, "path is required", "")
		return
	}
	if isToolContract(clean) {
		xhttp.ErrCode(c, "tool.contract_readonly", gin.H{"file": models.ToolFileName},
			models.ToolFileName+" is the tool contract and cannot be deleted (to remove the tool, delete the whole tool)", "")
		return
	}
	if isToolEntry(clean, entry) {
		xhttp.ErrCode(c, "tool.entry_readonly", gin.H{"entry": entry}, "the entry file cannot be deleted: the tool would remain but every run would report a missing entry", "")
		return
	}
	if rerr := os.RemoveAll(abs); rerr != nil {
		xhttp.ErrCode(c, "tool.delete_failed", nil, "delete failed: "+rerr.Error(), "")
		return
	}
	xhttp.OkJSON(c, "Success", gin.H{"path": clean}, "")
}

// UploadToolFile POST /customtools/:name/upload（multipart：file + dir）
//
// No overwriting: save as name-2.ext instead - overwriting silently deletes a file the tool already has,
// which must go through delete (the UI asks for confirmation) rather than happen quietly inside an upload.
func UploadToolFile(c *gin.Context) {
	root, _, ok := toolFileRoot(c, true)
	if !ok {
		return
	}
	fh, ferr := c.FormFile("file")
	if ferr != nil {
		xhttp.ErrCode(c, "tool.file_required", nil, "file is required", "")
		return
	}
	tooLarge := fmt.Sprintf("file too large (limit %d MB)", maxToolFileBytes>>20)
	if fh.Size > maxToolFileBytes {
		xhttp.ErrCode(c, "tool.file_too_large", gin.H{"limit_mb": maxToolFileBytes >> 20}, tooLarge, "")
		return
	}
	absDir, cleanDir, derr := joinToolFile(root, strings.TrimSpace(c.PostForm("dir")))
	if derr != nil {
		xhttp.ErrCode(c, "tool.invalid_path", nil, derr.Error(), "")
		return
	}
	name := sanitizeToolFileName(fh.Filename)
	if name == "" {
		xhttp.ErrCode(c, "tool.invalid_file_name", nil, "invalid file name", "")
		return
	}
	if isToolContract(path.Join(cleanDir, name)) {
		xhttp.ErrCode(c, "tool.contract_readonly", gin.H{"file": models.ToolFileName}, "cannot overwrite the contract file "+models.ToolFileName, "")
		return
	}
	rel := uniqueToolRel(absDir, name)

	src, oerr := fh.Open()
	if oerr != nil {
		xhttp.ErrCode(c, "tool.upload_failed", nil, "failed to read the upload: "+oerr.Error(), "")
		return
	}
	defer src.Close()
	// Read one extra byte to cover the case where the declared size cannot be trusted.
	data, rerr := io.ReadAll(io.LimitReader(src, maxToolFileBytes+1))
	if rerr != nil {
		xhttp.ErrCode(c, "tool.upload_failed", nil, "failed to read the upload: "+rerr.Error(), "")
		return
	}
	if len(data) > maxToolFileBytes {
		xhttp.ErrCode(c, "tool.file_too_large", gin.H{"limit_mb": maxToolFileBytes >> 20}, tooLarge, "")
		return
	}
	if werr := os.WriteFile(filepath.Join(absDir, rel), data, 0o644); werr != nil {
		xhttp.ErrCode(c, "tool.upload_save_failed", nil, "failed to save the upload: "+werr.Error(), "")
		return
	}
	out := rel
	if cleanDir != "" {
		out = path.Join(cleanDir, rel)
	}
	xhttp.OkJSON(c, "Success", gin.H{"name": rel, "path": out, "size": len(data)}, "")
}

// DownloadToolFile GET /customtools/:name/download?path=
//
// Serves the bytes for a drag to the desktop. Files only, never directories.
func DownloadToolFile(c *gin.Context) {
	root, _, ok := toolFileRoot(c, false)
	if !ok {
		return
	}
	abs, clean, err := joinToolFile(root, c.Query("path"))
	if err != nil {
		xhttp.ErrCode(c, "tool.invalid_path", nil, err.Error(), "")
		return
	}
	if clean == "" {
		xhttp.ErrCode(c, "tool.path_required", nil, "path is required", "")
		return
	}
	st, serr := os.Stat(abs)
	if serr != nil || st.IsDir() {
		xhttp.ErrCode(c, "tool.file_not_found", nil, "file not found", "")
		return
	}
	f, oerr := os.Open(abs)
	if oerr != nil {
		xhttp.ErrCode(c, "tool.file_not_found", nil, "file not found", "")
		return
	}
	defer f.Close()
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", path.Base(clean)))
	http.ServeContent(c.Writer, c.Request, path.Base(clean), st.ModTime(), f)
}

// sanitizeToolFileName keeps only the file name itself (a browser may send the whole path), strips control characters and caps the length.
func sanitizeToolFileName(raw string) string {
	name := path.Base(strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || name == "/" {
		return ""
	}
	rs := []rune(name)
	if len(rs) > 120 {
		ext := path.Ext(name)
		tail := []rune(ext)
		if len(tail) >= 20 {
			tail, ext = nil, ""
		}
		name = string(rs[:120-len(tail)]) + ext
	}
	return name
}

// uniqueToolRel never overwrites: name, name-2.ext, name-3.ext, ...
// It returns the file name relative to the directory (the caller joins dir directly).
func uniqueToolRel(dir, name string) string {
	taken := map[string]bool{}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			taken[e.Name()] = true
		}
	}
	if !taken[name] {
		return name
	}
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 2; i < 1000; i++ {
		cand := fmt.Sprintf("%s-%d%s", stem, i, ext)
		if !taken[cand] {
			return cand
		}
	}
	return fmt.Sprintf("%d-%s", time.Now().UnixNano(), name)
}
