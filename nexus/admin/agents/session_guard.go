package agents

import (
	"errors"

	"flame/nexus/shared/auth"
	"flame/nexus/shared/models"
	"flame/nexus/shared/repository/sessions"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// ============================================================================
// 会话归属校验的 HTTP 侧收口
//
//   解析本身（取 sid / 校验 UUID / 查库 / 比 owner）全在 sessions.Lookup。
//   这里只负责把三类失败翻成响应，保证同一类错误在 chat / stop / 文件接口 /
//   下载票上是同一句话 —— 否则「会话不存在」与「不是你的」的差异就能被拿来
//   枚举 sid。
// ============================================================================

// notOwnedSession 「不存在 / 不属于本 agent」的统一话术。
const notOwnedSession = "Session not found or not owned by this agent"

// respondSessionErr 按错误类别写响应。返回 false 只是为了让调用点写成
// `if !respondSessionErr(...) { return }` 时读起来顺，实际上它总会写一个响应。
func respondSessionErr(c *gin.Context, err error, notOwned string) bool {
	switch {
	case errors.Is(err, sessions.ErrSessionRequired), errors.Is(err, sessions.ErrSessionInvalid):
		// 入参问题：文案本身是干净的英文，直接透出
		xhttp.ErrJSON(c, err.Error(), nil, "")
	case errors.Is(err, sessions.ErrSessionForbidden):
		xhttp.ErrJSON(c, notOwned, nil, "")
	default:
		// 存储层故障：保留原文便于排查（与改造前的行为一致）
		xhttp.ErrJSON(c, "Failed to get session: "+err.Error(), nil, "")
	}
	return false
}

// lookupSessionForUser 控制台入口的会话解析：按「会话归属用户」收口。
//
// 会话是「某个用户在某个 agent 下的私有对话」，不是租户共享资源。允许别人
// 拿着 sid 进来会连着出四件事，而且都不是理论风险：
//   - 会话工作目录按归属用户组织（users/{uid}/sessions/{sid}/），别人续写等于
//     往别人的目录里落文件，还能浏览、下载、删除里面的产物；
//   - session_records 不记录「这一轮是谁发的」（只有 owner + session），
//     跨用户续写后历史无法归属，事后看到的是两个人的对话混在一条时间线里；
//   - shell / browser 的交互进程按 session 复用（tools.KillInteractive），
//     别人进来会直接接管对方的 cwd、环境变量和还活着的进程；
//   - stop 按 session 取消全部在跑的运行，别人能打断别人的一轮。
//
// 因此三处入口（HTTP chat / MCP / RPC）一律按 user 收口（sessions.ResolveForUser）：
// 会话归属的是用户，owner_type / owner_id 只记录创建者。父子 agent 共用同一 session
// 时 owner 必然对不上，若用 owner 收口，call_agent 的合法级联会被整体拒掉。
func lookupSessionForUser(ai *auth.AuthInfo, schema, sessionID string) (*models.Session, error) {
	if ai == nil {
		return nil, sessions.ErrSessionForbidden
	}
	return sessions.ResolveForUser(schema, sessionID, ai.ID)
}

// sessionVisibleTo 会话是否对该登录用户可见 —— 控制台所有会话入口的统一口径。
//
// 与 ListSessions / ListByUser 对齐（列表只列 user_id = 当前用户的会话），
// 免得出现「列表里看不到、知道 id 就能读写」。
//
// 判据本身在 models.Session.VisibleToUser：user_id 为空表示租户级会话（没有归属用户，
// 列表里也不出现），它不存在「别人」，放行；越权只发生在 user_id 非空且不等于自己时。
// 放在模型层是为了让 RPC / MCP 与这里共用同一份实现，避免三处口径各自漂移。
func sessionVisibleTo(sess *models.Session, ai *auth.AuthInfo) bool {
	if ai == nil {
		return false
	}
	return sess.VisibleToUser(ai.ID)
}
