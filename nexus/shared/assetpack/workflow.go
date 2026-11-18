package assetpack

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"flame/nexus/shared/paths"
)

// WorkflowFileName 工作流资产的入口文件。
//
// 工作流与其它三类不同：本体是 {schema}.flows 里的一行 JSONB（diagram + 变量 + 限额），
// 盘上没有对应目录。上架时把这一行序列化成 FLOW.json，再走同一条文件管线
// （打包 / 清单校验 / 导出包 / 导入 / 安装）。
//
// 为什么不给「无文件资产」单开一条路：那要改 PackFilesWith（ErrNoFiles）、
// Package.verify（清单非空）、WritePackage（清单非空）三处前提，而这三处是
// 「包必须自证内容完整」的支点。用它来承载一份 JSON，改动面小得多。
const WorkflowFileName = "FLOW.json"

// ValidateWorkflowName 工作流名校验：与 agent/skill 同一套字符集（字母数字横线、≤64），
// 但理由不同 —— 工作流不落地成目录，名字却**直接对外当工具名用**：
//
//	{schema}.flows.name → svc_endpoints.name（Create 强制相等，改名同步）
//	                    → MCP tools/list 的 tool.name
//	                    → agent 侧工具名 local__<name> 与授权 ref local:rpc:<name>
//
// 后两者都有字符集硬约束（toolgrant 的 ref 正则、模型侧 function name 正则），
// 中文名能建出来、却上架不出可用的端点、授权也解析不了 —— 所以这里必须挡住，
// 中文用途写 description（列表与卡片就是靠它辨认流程的）。
func ValidateWorkflowName(name string) error {
	n := strings.TrimSpace(name)
	if n == "" {
		return errors.New("工作流名为空")
	}
	if !paths.ValidName(n) {
		return fmt.Errorf("工作流名 %q 不合法：只能用字母、数字与横线（不能以横线开头，≤%d）。"+
			"它会作为对外工具名 local__<name> 暴露给模型，中文说明请写进「描述」", n, MaxNameLen)
	}
	return nil
}

// WorkflowSnapshot FLOW.json 的结构：跨租户可搬的**内容**。
//
// 刻意不带：
//
//	id                        目标租户重新生成
//	status                    目标租户按正常态（1）创建；废弃只由本机手工设置
//	created_by / updated_by   人与身份不跨租户
//	endpoints                 端点协议与上架状态属于运行环境配置，不是内容
//	breakpoints               调试断点属于本机调试态
//
// env_vars 与 diagram 里的疑似凭据值在打包前统一清空（见 StripSecrets 的 KindWorkflow 分支）。
type WorkflowSnapshot struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Diagram     json.RawMessage `json:"diagram,omitempty"`
	Arguments   json.RawMessage `json:"arguments,omitempty"`
	Outputs     json.RawMessage `json:"outputs,omitempty"`
	EnvVars     json.RawMessage `json:"env_vars,omitempty"`
	FlowVars    json.RawMessage `json:"flow_vars,omitempty"`
	// 五个限额刻意不用 omitempty：-1 在 flows 表里是「不限制」的有意义取值，
	// 省掉字段会让安装端读成 0，等于把「不限制」悄悄改成「限制为 0」。
	Timeout           int `json:"timeout"`
	MaxRecursionDepth int `json:"max_recursion_depth"`
	MaxNodeCount      int `json:"max_node_count"`
	MaxExecCount      int `json:"max_exec_count"`
	MaxRunRecords     int `json:"max_run_records"`
	MaxSessionRecords int `json:"max_session_records"`
}
