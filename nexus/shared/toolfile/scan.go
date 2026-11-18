package toolfile

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"flame/nexus/shared/models"
	"flame/nexus/shared/paths"
)

// Entry 扫描到的一个工具目录（目录存在且契约可用）。
type Entry struct {
	Name string
	Dir  string
	File *models.ToolFile
}

// ScanIssue 一个「看起来像工具、但不能当工具用」的目录或记录。
//
// 为什么不静默跳过：目录里躺着代码、平台上却看不见，是这一步最容易被抱怨的
// 形态（以前只有表里有的才算工具，盘上的孤儿连报错都没有）。现在至少能列出来。
type ScanIssue struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

// Scan 扫描租户的 tools/ 目录，返回可用工具与逐条问题。
//
// 只认「目录 + 合法 TOOL.json」：契约是声明式的，绝不为了读契约去执行租户代码
// （插件可以，因为插件是平台方写的；这里不行）。
func Scan(rootDir, schema string) ([]Entry, []ScanIssue) {
	if err := checkRoot(rootDir); err != nil {
		return nil, []ScanIssue{{Path: schema, Error: err.Error()}}
	}
	base := paths.ResolveToolsDir(rootDir, schema)
	dirs, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // 还没建过 tools/ = 没有工具，不是错误
		}
		return nil, []ScanIssue{{Path: base, Error: err.Error()}}
	}

	out := make([]Entry, 0, len(dirs))
	var issues []ScanIssue
	for _, d := range dirs {
		name := d.Name()
		if !d.IsDir() || strings.HasPrefix(name, ".") {
			continue // .tmp 之类的中间文件、隐藏目录不参与
		}
		f, rerr := Read(rootDir, schema, name)
		if rerr != nil {
			issues = append(issues, ScanIssue{Path: name, Error: "TOOL.json 解析失败：" + rerr.Error()})
			continue
		}
		if f == nil {
			issues = append(issues, ScanIssue{Path: name, Error: "缺 TOOL.json（无法确定工具契约）"})
			continue
		}
		if verr := f.Validate(name); verr != nil {
			issues = append(issues, ScanIssue{Path: name, Error: verr.Error()})
			continue
		}
		out = append(out, Entry{Name: name, Dir: filepath.Join(base, name), File: f})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	sort.Slice(issues, func(i, j int) bool { return issues[i].Path < issues[j].Path })
	return out, issues
}
