package client

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"

	"flame/nexus/service/rpc/protocol"
	"flame/nexus/shared/models"
	"flame/tools/xsql"
)

// ClientSession 一个 RPC 客户端会话，封装与远端 RPC server 的 JSON-RPC 2.0 通信。
// streamable_http 下为无状态短连接（每次请求独立 POST）；stdio 下对应一个子进程。
type ClientSession struct {
	conn *models.RpcConnection

	// streamable_http 专用
	httpClient *http.Client

	// stdio 专用
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	reqMu   sync.Mutex // 串行化 stdio 请求（一次一个 in-flight 请求）
	nextID  int64
	closed  bool
	closeMu sync.Mutex
}

// rpcRequest JSON-RPC 2.0 请求
type rpcRequest struct {
	Jsonrpc string      `json:"jsonrpc"`
	Version int         `json:"version"`
	ID      int64       `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

// rpcResponse JSON-RPC 2.0 响应
type rpcResponse struct {
	Jsonrpc string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// rpcProgress 进度通知信封（服务端主动推送，无 id）
type rpcProgress struct {
	Jsonrpc string                     `json:"jsonrpc"`
	Method  string                     `json:"method"`
	Params  ProgressNotificationParams `json:"params"`
}

// ProgressNotificationParams 进度通知参数（字段位置对齐 MCP 2026-07-28 的 notifications/progress）。
// Meta 用 map[string]any：agent 事件会在 _meta 里带 call_id / ok / duration_ms 等非字符串字段。
//
// Progress 只保证单调递增（「第几条通知」），不代表完成度；总量未知时 Total 为 0（缺省）。
type ProgressNotificationParams struct {
	ProgressToken any            `json:"progressToken"`
	Progress      float64        `json:"progress"`
	Total         float64        `json:"total,omitempty"`
	Message       string         `json:"message"`
	Meta          map[string]any `json:"_meta,omitempty"`
}

// NewClientSession 根据连接配置建立与远端 RPC server 的会话。
// 调用方负责在会话使用完毕后调用返回的 session.Close()。
func NewClientSession(ctx context.Context, conn *models.RpcConnection) (*ClientSession, error) {
	s := &ClientSession{conn: conn}

	switch strings.ToLower(conn.Transport) {
	case "", "streamable_http":
		if conn.URL == "" {
			return nil, fmt.Errorf("streamable_http 传输需要 url")
		}
		s.httpClient = newHTTPClientWithHeaders(customHeaders(conn.Auth))
	case "stdio":
		if conn.Command == "" {
			return nil, fmt.Errorf("stdio 传输需要 command")
		}
		if err := s.startStdio(); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("不支持的 transport: %s", conn.Transport)
	}

	return s, nil
}

// startStdio 启动 stdio 子进程并建立 stdin/stdout 通道
func (s *ClientSession) startStdio() error {
	args := parseArgs(s.conn.Args)
	cmd := exec.Command(s.conn.Command, args...)
	if s.conn.WorkDir != "" {
		cmd.Dir = s.conn.WorkDir
	}
	if env := parseEnv(s.conn.Env); len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	// stderr 直接丢弃（子进程调试日志不参与协议）
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 stdio 子进程失败: %w", err)
	}

	s.cmd = cmd
	s.stdin = stdin
	s.stdout = bufio.NewReader(stdout)
	s.nextID = 0
	return nil
}

// Call 发送一次 JSON-RPC 请求并返回 result 原始 JSON。
func (s *ClientSession) Call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	if strings.ToLower(s.conn.Transport) == "stdio" {
		return s.callStdio(ctx, method, params)
	}
	return s.callHTTP(ctx, method, params)
}

// CallWithProgress 发送一次 JSON-RPC 请求，并在执行期间流式接收 progress 通知。
// onProgress 会在服务端推送 notifications/progress 时边推边调（与阻塞在调用上的主协程并发）。
//   - stdio：在读取循环中识别 method=="notifications/progress" 的帧并回调。
//   - streamable_http：解析 SSE 流，识别 progress 事件与最终 result。
func (s *ClientSession) CallWithProgress(ctx context.Context, method string, params interface{}, onProgress func(*ProgressNotificationParams)) (json.RawMessage, error) {
	if strings.ToLower(s.conn.Transport) == "stdio" {
		return s.callStdioWithProgress(ctx, method, params, onProgress)
	}
	return s.callHTTPWithProgress(ctx, method, params, onProgress)
}

// callHTTP streamable_http 下发起一次 JSON-RPC POST 请求
func (s *ClientSession) callHTTP(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	return s.callHTTPWithProgress(ctx, method, params, nil)
}

// callHTTPWithProgress streamable_http 下发起请求。
// 服务端流式响应为 text/event-stream：先推多条 notifications/progress，最后推带 id 的最终 result。
// 非流式响应为单条 application/json。两种情况统一处理，progress 通知边读边回调 onProgress。
func (s *ClientSession) callHTTPWithProgress(ctx context.Context, method string, params interface{}, onProgress func(*ProgressNotificationParams)) (json.RawMessage, error) {
	reqBody, err := json.Marshal(rpcRequest{Jsonrpc: "2.0", Version: protocol.CurrentProtocolVersion, ID: 1, Method: method, Params: params})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.conn.URL, strings.NewReader(string(reqBody)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	// 鉴权头（stdio 下忽略，凭证走 env）
	if authHeader, aerr := buildAuthorizationHeader(s.conn.Auth); aerr != nil {
		return nil, aerr
	} else if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") {
		return s.parseSSE(ctx, resp.Body, onProgress)
	}

	var rpcResp rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, err
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("RPC error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}
	return rpcResp.Result, nil
}

// parseSSE 解析 text/event-stream 响应流：progress 事件边读边回调，最终 result 返回。
// 事件格式：`event: message\ndata: {json}\n\n`，流结束即关闭连接（无 [DONE] 标记）。
func (s *ClientSession) parseSSE(ctx context.Context, body io.Reader, onProgress func(*ProgressNotificationParams)) (json.RawMessage, error) {
	reader := bufio.NewReader(body)
	for {
		line, err := readSSELine(ctx, reader)
		if err != nil {
			if err == io.EOF {
				return nil, fmt.Errorf("SSE 流提前结束，未收到最终结果")
			}
			return nil, err
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))

		// 先尝试按 progress 通知解析
		var prog rpcProgress
		if err := json.Unmarshal([]byte(payload), &prog); err == nil && prog.Method == "notifications/progress" {
			if onProgress != nil {
				onProgress(&prog.Params)
			}
			continue
		}
		// 否则视为最终 result
		var rpcResp rpcResponse
		if err := json.Unmarshal([]byte(payload), &rpcResp); err != nil {
			continue
		}
		if rpcResp.Error != nil {
			return nil, fmt.Errorf("RPC error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
		}
		if len(rpcResp.Result) == 0 {
			continue
		}
		return rpcResp.Result, nil
	}
}

// readSSELine 读取一行 SSE 文本（\n 分隔），支持 ctx 取消。
func readSSELine(ctx context.Context, reader *bufio.Reader) (string, error) {
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		l, err := reader.ReadString('\n')
		ch <- result{line: l, err: err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case r := <-ch:
		return r.line, r.err
	}
}

// callStdio stdio 下发起一次 JSON-RPC 请求（\n 分隔帧，串行）
func (s *ClientSession) callStdio(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	return s.callStdioWithProgress(ctx, method, params, nil)
}

// callStdioWithProgress stdio 下发起请求，并在读取循环中识别 progress 通知并回调。
func (s *ClientSession) callStdioWithProgress(ctx context.Context, method string, params interface{}, onProgress func(*ProgressNotificationParams)) (json.RawMessage, error) {
	s.reqMu.Lock()
	defer s.reqMu.Unlock()

	if s.isClosed() {
		return nil, fmt.Errorf("RPC stdio 会话已关闭")
	}

	s.closeMu.Lock()
	s.nextID++
	id := s.nextID
	s.closeMu.Unlock()

	reqBody, err := json.Marshal(rpcRequest{Jsonrpc: "2.0", Version: protocol.CurrentProtocolVersion, ID: id, Method: method, Params: params})
	if err != nil {
		return nil, err
	}

	if _, err := s.stdin.Write(append(reqBody, '\n')); err != nil {
		return nil, fmt.Errorf("写入 stdio 请求失败: %w", err)
	}

	// 读取响应直到匹配到对应 id；期间识别 server 主动推送的 progress 通知。
	for {
		line, err := s.readFrame(ctx)
		if err != nil {
			return nil, err
		}
		// 先尝试按 progress 通知解析（method == notifications/progress）
		var prog rpcProgress
		if err := json.Unmarshal(line, &prog); err == nil && prog.Method == "notifications/progress" {
			if onProgress != nil {
				onProgress(&prog.Params)
			}
			continue
		}
		var rpcResp rpcResponse
		if err := json.Unmarshal(line, &rpcResp); err != nil {
			continue
		}
		if rpcResp.ID != id {
			continue
		}
		if rpcResp.Error != nil {
			return nil, fmt.Errorf("RPC error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
		}
		return rpcResp.Result, nil
	}
}

// readFrame 读取一行（\n 分隔）JSON 帧
func (s *ClientSession) readFrame(ctx context.Context) ([]byte, error) {
	type result struct {
		line []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := s.stdout.ReadBytes('\n')
		ch <- result{line: line, err: err}
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		return r.line, nil
	}
}

// ListTools 调用 tools/list 发现工具列表
func (s *ClientSession) ListTools(ctx context.Context) ([]*models.RpcConnectionTool, error) {
	result, err := s.Call(ctx, "tools/list", map[string]interface{}{})
	if err != nil {
		return nil, err
	}

	var listResult struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
		ServerVersion int `json:"serverVersion"`
	}
	if err := json.Unmarshal(result, &listResult); err != nil {
		return nil, fmt.Errorf("解析 tools/list 结果失败: %w", err)
	}

	// 版本协商：服务端版本高于客户端时，记录告警（内部同构代码通常同步升级）
	if listResult.ServerVersion > protocol.CurrentProtocolVersion {
		// 可选：记录告警或返回特定错误，交由上层决定
		_ = listResult.ServerVersion
	}

	tools := make([]*models.RpcConnectionTool, 0, len(listResult.Tools))
	for _, t := range listResult.Tools {
		var schema xsql.JSONB
		if len(t.InputSchema) > 0 {
			schema = xsql.JSONB{Data: json.RawMessage(t.InputSchema)}
		}
		tools = append(tools, &models.RpcConnectionTool{
			ConnectionID: s.conn.ID,
			Name:         t.Name,
			Description:  t.Description,
			InputSchema:  schema,
		})
	}
	return tools, nil
}

func (s *ClientSession) isClosed() bool {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	return s.closed
}

// Close 关闭会话（stdio 下终止子进程）
func (s *ClientSession) Close() error {
	if strings.ToLower(s.conn.Transport) != "stdio" {
		return nil
	}
	s.closeMu.Lock()
	if s.closed {
		s.closeMu.Unlock()
		return nil
	}
	s.closed = true
	s.closeMu.Unlock()

	if s.stdin != nil {
		_ = s.stdin.Close()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_ = s.cmd.Wait()
	}
	return nil
}

// newHTTPClientWithHeaders 返回一个注入了默认请求头的 *http.Client
func newHTTPClientWithHeaders(headers map[string]string) *http.Client {
	base := http.DefaultClient
	return &http.Client{
		Transport: &headerRoundTripper{
			base:   base.Transport,
			header: headers,
		},
		Timeout: base.Timeout,
	}
}

// headerRoundTripper 在每次请求前注入自定义头
type headerRoundTripper struct {
	base   http.RoundTripper
	header map[string]string
}

func (rt *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range rt.header {
		req.Header.Set(k, v)
	}
	base := rt.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

// parseArgs 将 JSONB 参数数组解析为字符串切片
func parseArgs(args xsql.JSONB) []string {
	var arr []string
	if args.Data == nil {
		return arr
	}
	if b, ok := args.Data.(json.RawMessage); ok {
		_ = json.Unmarshal(b, &arr)
	}
	return arr
}

// parseEnv 将 JSONB 环境变量数组（["K=V", ...]）解析为字符串切片
func parseEnv(env xsql.JSONB) []string {
	var arr []string
	if env.Data == nil {
		return arr
	}
	if b, ok := env.Data.(json.RawMessage); ok {
		_ = json.Unmarshal(b, &arr)
	}
	return arr
}
