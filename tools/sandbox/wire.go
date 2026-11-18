package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// ============================================================================
// 与 sandboxd 之间的线协议（两端共享）
//
//   文本帧 = JSON 控制帧（Frame）
//   二进制帧 = [1 字节通道标记][原始字节]
//
// 用二进制帧承载 stdout/stderr/stdin，避免 base64 带来的 33% 膨胀与额外分配。
// ============================================================================

// 二进制帧的通道标记（首字节）。
const (
	ChanStdout byte = 1
	ChanStderr byte = 2
	ChanStdin  byte = 3
)

// 控制帧类型。
const (
	FrameOpen     = "open"      // 客户端 → 服务端：开始（单工可带 Spec，含可选 Lang；双工只带 Lang）
	FrameReady    = "ready"     // 服务端 → 客户端：已就绪
	FrameExec     = "exec"      // 客户端 → 服务端：执行脚本（双工）
	FrameDone     = "done"      // 服务端 → 客户端：脚本执行完成
	FrameStdinEOF = "stdin_eof" // 客户端 → 服务端：输入结束（半关闭）
	FrameExit     = "exit"      // 服务端 → 客户端：进程退出
	FrameKill     = "kill"      // 客户端 → 服务端：强杀
	FrameClose    = "close"     // 客户端 → 服务端：正常结束（允许归还池）
	FrameError    = "error"     // 服务端 → 客户端：错误

	// FrameSessionEmpty 服务端 → 客户端：该 session 已无任何长交互工具。
	//
	// 只在 kill 路径上发（那条连接还在）。一个 session 可以同时有多个长交互工具，
	// 注册表里是 tools map —— 杀掉其中一个不代表会话作废，所以判据是「杀完之后
	// 还剩几个」，只有全没了才发这一帧。进程自退 / janitor 空闲回收都没有连接的
	// 上下文，那两条路径的粘性绑定仍靠 TTL 兜底。
	FrameSessionEmpty = "session_empty"
)

// maxFrameBytes 单条消息上限（大输出场景预留）。
const maxFrameBytes = 32 << 20 // 32 MiB

// WireLimits Limits 的可序列化形态（Timeout 用秒，便于人工排查协议）。
type WireLimits struct {
	MemoryMB       int     `json:"memory_mb,omitempty"`
	CPUCount       int     `json:"cpu_count,omitempty"`
	MaxPIDs        int     `json:"max_pids,omitempty"`
	MaxFileMB      int     `json:"max_file_mb,omitempty"`
	TimeoutSec     float64 `json:"timeout_sec,omitempty"`
	MaxOutputBytes int64   `json:"max_output_bytes,omitempty"`
}

// Wire 转成可序列化形态。
func (l Limits) Wire() WireLimits {
	w := WireLimits{
		MemoryMB:       l.MemoryMB,
		CPUCount:       l.CPUCount,
		MaxPIDs:        l.MaxPIDs,
		MaxFileMB:      l.MaxFileMB,
		MaxOutputBytes: l.MaxOutputBytes,
	}
	if l.Timeout > 0 {
		w.TimeoutSec = l.Timeout.Seconds()
	}
	return w
}

// Limits 转回内部形态。
func (w WireLimits) Limits() Limits {
	l := Limits{
		MemoryMB:       w.MemoryMB,
		CPUCount:       w.CPUCount,
		MaxPIDs:        w.MaxPIDs,
		MaxFileMB:      w.MaxFileMB,
		MaxOutputBytes: w.MaxOutputBytes,
	}
	if w.TimeoutSec > 0 {
		l.Timeout = time.Duration(w.TimeoutSec * float64(time.Second))
	}
	return l
}

// WireSpec Spec 的可序列化形态（去掉 Stderr 这类本地 IO）。
//
// 注意：Spec.Argv / Spec.Dir / Mounts 中的路径均为「沙箱内」语义；
// Mounts 的 Host 是宿主绝对路径，要求 nexus 与 sandboxd 共享同一份存储。
type WireSpec struct {
	// Script / Args 配合 Frame.Lang 使用（单工的语言路径）：
	// 语言名走 Frame.Lang（与双工同一个字段），脚本与参数在这里。
	Script string     `json:"script,omitempty"`
	Args   []string   `json:"args,omitempty"`
	Argv   []string   `json:"argv,omitempty"`
	Dir    string     `json:"dir,omitempty"`
	Env    []string   `json:"env,omitempty"`
	Mounts []Mount    `json:"mounts,omitempty"`
	Limits WireLimits `json:"limits,omitempty"`
	// Egress 请求出网。nil = 没表态（按部署默认），false = 明确要断网，true = 要出网。
	//
	// 这是**请求意图**，不是结果：sandboxd 会按部署上限裁决后才落到隔离参数上
	// （network=off 时 true 会被拒）。只对单工有效 —— 双工复用建好的隔离单元，
	// 网络在建单元时写死，且双工的 open 帧根本不带 Spec。
	Egress *bool `json:"egress,omitempty"`
}

// Wire 转成可序列化形态。
func (s *Spec) Wire() *WireSpec {
	if s == nil {
		return nil
	}
	return &WireSpec{
		Script: s.Script,
		Args:   s.Args,
		Argv:   s.Argv,
		Dir:    s.Dir,
		Env:    s.Env,
		Mounts: s.Mounts,
		Limits: s.Limits.Wire(),
		Egress: s.Egress,
	}
}

// Spec 转回内部形态。
func (w *WireSpec) Spec() *Spec {
	if w == nil {
		return nil
	}
	return &Spec{
		Script: w.Script,
		Args:   w.Args,
		Argv:   w.Argv,
		Dir:    w.Dir,
		Env:    w.Env,
		Mounts: w.Mounts,
		Limits: w.Limits.Limits(),
		Egress: w.Egress,
	}
}

// Frame 控制帧。按 Type 使用不同字段，未用字段省略。
type Frame struct {
	Type string `json:"type"`

	// ready
	//
	// ScriptID 本次脚本会话的 ID（sandboxd 侧的 worker ID）。它是两端日志对账的 join key：
	// 容器身份只有 sandboxd 知道，它把这个 ID 回传，本侧记下它出现的时刻。
	ScriptID string `json:"script_id,omitempty"`
	Reused   bool   `json:"reused,omitempty"`

	// open
	Lang string    `json:"lang,omitempty"`
	Spec *WireSpec `json:"spec,omitempty"`

	// interactive open：长交互会话身份与工具标识（/v1/interactive 专用）
	SessionID string `json:"session_id,omitempty"`
	ToolID    string `json:"tool_id,omitempty"`

	// Ephemeral 临时进程（无 session 的调用）：sandboxd 用它选短兜底超时。
	//
	// 进程仍可被同一 session_id 的后续调用复用（run 内的多次工具调用），销毁靠
	// nexus 在 run 结束时显式 kill（快路径）或短超时兜底 —— 缺省 false = 持久保留。
	Ephemeral bool `json:"ephemeral,omitempty"`

	// exec
	Script string         `json:"script,omitempty"`
	Inputs map[string]any `json:"inputs,omitempty"`

	// done
	Result any `json:"result,omitempty"`

	// done / error 共用：脚本 stderr（print 输出 / 错误 traceback），可能被截断。
	Logs string `json:"logs,omitempty"`

	// exit / error
	Code    int    `json:"code,omitempty"`
	ErrCode string `json:"err_code,omitempty"`
	Message string `json:"message,omitempty"`
}

// ============================================================================
// Conn：一条到 sandboxd 的 WS 连接
// ============================================================================

// Conn 帧读写封装。写操作串行化（gorilla 不允许并发写）。
type Conn struct {
	ws *websocket.Conn
	mu sync.Mutex
}

// Dial 建立到 sandboxd 的连接。
func Dial(ctx context.Context, endpoint, token string) (*Conn, error) {
	h := http.Header{}
	if token != "" {
		h.Set("Authorization", "Bearer "+token)
	}
	d := &websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	ws, resp, err := d.DialContext(ctx, endpoint, h)
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("sandbox: dial %s: %w (http %d)", endpoint, err, resp.StatusCode)
		}
		return nil, fmt.Errorf("sandbox: dial %s: %w", endpoint, err)
	}
	ws.SetReadLimit(maxFrameBytes)
	return &Conn{ws: ws}, nil
}

// NewConn 用一条已建立的 WebSocket 连接创建 Conn（服务端使用）。
func NewConn(ws *websocket.Conn) *Conn {
	ws.SetReadLimit(maxFrameBytes)
	return &Conn{ws: ws}
}

// SendFrame 发送控制帧（文本帧）。
func (c *Conn) SendFrame(f *Frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("sandbox: marshal frame: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ws.WriteMessage(websocket.TextMessage, b)
}

// SendData 发送一段原始字节（ch 为通道标记）。
func (c *Conn) SendData(ch byte, p []byte) error {
	buf := make([]byte, 1+len(p))
	buf[0] = ch
	copy(buf[1:], p)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ws.WriteMessage(websocket.BinaryMessage, buf)
}

// Recv 读取下一条消息，返回互斥的控制帧或原始字节。
// data 非 nil 时，data[0] 是通道标记，data[1:] 是负载。
func (c *Conn) Recv() (*Frame, []byte, error) {
	for {
		typ, data, err := c.ws.ReadMessage()
		if err != nil {
			return nil, nil, err
		}
		switch typ {
		case websocket.TextMessage:
			var f Frame
			if err := json.Unmarshal(data, &f); err != nil {
				return nil, nil, fmt.Errorf("sandbox: bad control frame: %w", err)
			}
			return &f, nil, nil
		case websocket.BinaryMessage:
			if len(data) == 0 {
				continue
			}
			return nil, data, nil
		default:
			continue
		}
	}
}

// Close 关闭连接。
func (c *Conn) Close() error { return c.ws.Close() }
