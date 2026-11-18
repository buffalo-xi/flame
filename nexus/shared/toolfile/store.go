// Package toolfile 自定义工具契约文件（TOOL.json）的读写。
//
// 分工：
//   - models.ToolFile：纯数据结构与「契约 → 表行」的语义（不碰文件系统）
//   - 本包：磁盘布局、原子写、读失败时的降级口径
//
// 读失败一律**降级到表**（Source="table" + warning），绝不因为一个坏文件让工具消失：
// 契约是「描述」，没了描述工具照样能跑；把它变成不可用只会让人以为工具坏了。
package toolfile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"flame/nexus/shared/models"
	"flame/nexus/shared/paths"
)

// Contract 契约来源与告警：管理端据此显示「这份契约是从哪来的」。
type Contract struct {
	Source   string   `json:"source"`             // file：来自 TOOL.json；table：回退表定义
	Warnings []string `json:"warnings,omitempty"` // 解析失败 / 名字不符 / 入口缺失等
}

// Dir 工具目录的宿主绝对路径：{root}/tenants/{schema}/tools/{name}。
func Dir(rootDir, schema, name string) string {
	return filepath.Join(paths.ResolveToolsDir(rootDir, schema), name)
}

// Path 契约文件的宿主绝对路径。
func Path(rootDir, schema, name string) string {
	return filepath.Join(Dir(rootDir, schema, name), models.ToolFileName)
}

// Read 读契约文件。文件不存在返回 (nil, nil) —— 灰度期缺文件是正常态，不是错误。
func Read(rootDir, schema, name string) (*models.ToolFile, error) {
	if err := checkRoot(rootDir); err != nil {
		return nil, err
	}
	if err := safeName(name); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(Path(rootDir, schema, name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return models.ParseToolFile(data)
}

// Write 原子写契约文件：先写同目录的 .tmp 再 rename。
//
// 不做「直接覆盖」：执行侧可能在读，半个 JSON 会让它判定成解析失败并回退表 ——
// 一秒钟的窗口里工具描述忽然变成旧的，比写失败更难查。
func Write(rootDir, schema, name string, f *models.ToolFile) error {
	if err := checkRoot(rootDir); err != nil {
		return err
	}
	if err := safeName(name); err != nil {
		return err
	}
	return WriteAt(Dir(rootDir, schema, name), f)
}

// ReadAt 读**指定目录**里的契约文件（与 WriteAt 对称；正式运行从版本目录读定义）。
// 文件不存在返回 (nil, nil)：调用方按「版本缺契约」决定是报错还是跳过。
func ReadAt(dir string) (*models.ToolFile, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("toolfile: empty dir")
	}
	data, err := os.ReadFile(filepath.Join(dir, models.ToolFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return models.ParseToolFile(data)
}

// WriteAt 把契约写进**指定目录**（版本快照用：目录不是草稿根，但写入口径必须一致）。
//
// 快照为什么需要它：工具的版本必须自带合法 TOOL.json（定义以文件为准，缺文件会
// 退化到表行）。草稿里缺或坏时，发布动作要把合并视图补写进版本目录 —— 且只写快照，
// 不动草稿：发布不该顺手改用户正在编辑的内容。
func WriteAt(dir string, f *models.ToolFile) error {
	if strings.TrimSpace(dir) == "" {
		return fmt.Errorf("toolfile: empty dir")
	}
	data, err := models.MarshalToolFile(f)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	p := filepath.Join(dir, models.ToolFileName)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// WriteOf 由表行生成并写入契约文件（管理端与元工具共用的唯一写入口）。
//
// 两处写行的地方（admin API、超级智能体的元工具）都必须调用它：读路径是「文件优先」，
// 只改表不写文件的话，下一次读取会把旧契约盖回去，用户看到「保存成功但没变」。
func WriteOf(rootDir, schema string, t *models.CustomTool) error {
	return Write(rootDir, schema, t.Name, models.ToolFileOf(t))
}

// Load 把盘上的契约应用到表行上，并报告来源。
//
// 三条降级（都不阻断）：解析失败、name 与目录不一致、entry 越界 → 用表定义；
// 入口文件缺失只告警 —— 执行时（尤其是试跑）会给出更准确的错误。
func Load(rootDir, schema string, t *models.CustomTool) Contract {
	f, err := Read(rootDir, schema, t.Name)
	if err != nil {
		return Contract{
			Source:   "table",
			Warnings: []string{fmt.Sprintf("TOOL.json 解析失败，已回退表定义：%v", err)},
		}
	}
	if f == nil {
		return Contract{Source: "table"}
	}
	if verr := f.Validate(t.Name); verr != nil {
		return Contract{
			Source:   "table",
			Warnings: []string{fmt.Sprintf("%v（已回退表定义，请修好 TOOL.json）", verr)},
		}
	}
	f.Apply(t)

	c := Contract{Source: "file"}
	entry := filepath.Join(Dir(rootDir, schema, t.Name), filepath.FromSlash(t.Entry))
	if _, serr := os.Stat(entry); serr != nil {
		c.Warnings = append(c.Warnings, fmt.Sprintf("TOOL.json 声明的入口文件不存在：%s", t.Entry))
	}
	return c
}

// EnsureAll 为「有表行、缺文件」的工具补写契约（迁移入口，幂等）。
//
// 只在管理端打开列表时调用：读路径不写文件，避免「看一眼就把盘改了」。
func EnsureAll(rootDir, schema string, list []models.CustomTool) (written int, errs []string) {
	for i := range list {
		t := &list[i]
		exists, err := hasFile(rootDir, schema, t.Name)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", t.Name, err))
			continue
		}
		if exists {
			continue
		}
		if err := Write(rootDir, schema, t.Name, models.ToolFileOf(t)); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", t.Name, err))
			continue
		}
		written++
	}
	return written, errs
}

func hasFile(rootDir, schema, name string) (bool, error) {
	if err := safeName(name); err != nil {
		return false, err
	}
	st, err := os.Stat(Path(rootDir, schema, name))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return !st.IsDir(), nil
}

// checkRoot 存储根必须已配置。
//
// 空 rootDir 会让 Path 变成 "tenants/t0/tools/..." 这样的**相对路径**：
// 读会读到工作目录里的同名文件、写会在进程 cwd 下造一棵假目录树 ——
// 比直接报错难查得多，所以这里显式拒掉。
func checkRoot(rootDir string) error {
	if strings.TrimSpace(rootDir) == "" {
		return fmt.Errorf("存储根未初始化（agent.root_dir）")
	}
	return nil
}

// safeName 名字只能是一级目录名：拒绝分隔符与 ..（否则 Path 会跳出 tools/）。
//
// 表里有 name 唯一约束，但写盘函数也会被管理端/迁移直接调用，这里兜住。
func safeName(name string) error {
	n := strings.TrimSpace(name)
	if n == "" {
		return fmt.Errorf("工具名为空")
	}
	if n != name || strings.ContainsAny(n, `/\`) || n == "." || n == ".." {
		return fmt.Errorf("非法工具名 %q（只能是单级目录名）", name)
	}
	return nil
}
