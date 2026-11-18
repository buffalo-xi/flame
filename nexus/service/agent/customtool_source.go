package agent

import (
	"context"
	"database/sql"
	"errors"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/config"
	"flame/nexus/shared/customtoolstore"
	"flame/nexus/shared/models"
)

// customToolSource 把「工具目录 + 策略表」的合并视图翻成 tools 包要的形状。
//
// 为什么中间隔一层：tools 包只该知道「有哪些工具、怎么跑」，不该知道表结构、
// 目录布局、状态语义。翻错了也只影响这一处。
//
// 从 2a 起，存在性以**目录**为准（customtoolstore 负责合并），表只提供
// status / configs：盘上多一个合法工具目录，平台就能看见它。
type customToolSource struct {
	schema string
	store  *customtoolstore.Store
	// mode 本次运行读哪份定义：正式入口（MCP / RPC / 端点 / flow）读当前版本，
	// 编辑页调试 / 超级智能体读草稿。缺省（零值）即草稿。
	mode tools.DefinitionMode
}

func newCustomToolSource(schema string, mode tools.DefinitionMode) *customToolSource {
	return &customToolSource{
		schema: schema,
		store:  customtoolstore.New(config.AgentRootDir(), schema),
		mode:   mode,
	}
}

func (s *customToolSource) List(ctx context.Context) ([]tools.CustomToolSpec, error) {
	// 正式运行只列「有当前版本」的工具：清单来自版本里的 TOOL.json。
	// 读草稿会让「新建未发布」的工具直接出现在线上（调用时才报未发布），
	// 也会让「草稿删掉但版本还在」的工具消失 —— 与定义冻结相冲突。
	var (
		list []models.CustomTool
		err  error
	)
	if s.mode.IsCurrent() {
		list, err = s.store.ListReleased()
	} else {
		list, err = s.store.ListEnabled(s.schema)
	}
	if err != nil {
		return nil, err
	}
	out := make([]tools.CustomToolSpec, 0, len(list))
	for i := range list {
		out = append(out, tools.CustomToolSpecFromModel(&list[i]))
	}
	return out, nil
}

// Get 按短名取。不存在或**非正常态**都返回 (nil, nil)：
// 锁定 / 废弃的工具在目录里已经不可见，执行层若还放行就等于状态栏只是个装饰。
//
// 正式运行读当前版本目录里的契约（与 ToolDir 同源）；没有正式版本 = 不存在。
func (s *customToolSource) Get(ctx context.Context, name string) (*tools.CustomToolSpec, error) {
	var (
		t   *models.CustomTool
		err error
	)
	if s.mode.IsCurrent() {
		t, err = s.store.Released(name)
	} else {
		t, err = s.store.Get(s.schema, name)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if t.Status != models.StatusNormal {
		return nil, nil
	}
	spec := tools.CustomToolSpecFromModel(t)
	return &spec, nil
}

// Status 文件闸用：只回状态，不构造 spec（锁定态也要能判，所以不能复用 Get）。
func (s *customToolSource) Status(ctx context.Context, name string) (int, bool, error) {
	return s.store.Status(name)
}
