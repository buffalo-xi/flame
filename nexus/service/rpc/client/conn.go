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

// ClientSession is one RPC client session wrapping the JSON-RPC 2.0 conversation with a remote RPC server.
// Under streamable_http it is a stateless short-lived connection (one POST per request); under stdio it
// corresponds to one child process.
type ClientSession struct {
	conn *models.RpcConnection

	// streamable_http only.
	httpClient *http.Client

	// stdio only.
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	reqMu   sync.Mutex // serializes stdio requests (one in-flight request at a time)
	nextID  int64
	closed  bool
	closeMu sync.Mutex
}

// rpcRequest is a JSON-RPC 2.0 request.
type rpcRequest struct {
	Jsonrpc string      `json:"jsonrpc"`
	Version int         `json:"version"`
	ID      int64       `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

// rpcResponse is a JSON-RPC 2.0 response.
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

// rpcProgress is the envelope of a progress notification (pushed by the server, no id).
type rpcProgress struct {
	Jsonrpc string                     `json:"jsonrpc"`
	Method  string                     `json:"method"`
	Params  ProgressNotificationParams `json:"params"`
}

// ProgressNotificationParams holds the parameters of a progress notification (field layout aligned
// with notifications/progress of MCP 2026-07-28).
// Meta is a map[string]any: agent events carry non-string fields such as call_id / ok / duration_ms
// inside _meta.
//
// Progress is only guaranteed to increase monotonically ("which notification this is"), it does not
// express completion; Total is 0 (absent) when the total is unknown.
type ProgressNotificationParams struct {
	ProgressToken any            `json:"progressToken"`
	Progress      float64        `json:"progress"`
	Total         float64        `json:"total,omitempty"`
	Message       string         `json:"message"`
	Meta          map[string]any `json:"_meta,omitempty"`
}

// NewClientSession opens a session with the remote RPC server according to the connection config.
// The caller is responsible for calling session.Close() once the session is no longer needed.
func NewClientSession(ctx context.Context, conn *models.RpcConnection) (*ClientSession, error) {
	s := &ClientSession{conn: conn}

	switch strings.ToLower(conn.Transport) {
	case "", "streamable_http":
		if conn.URL == "" {
			return nil, fmt.Errorf("the streamable_http transport requires a url")
		}
		s.httpClient = newHTTPClientWithHeaders(customHeaders(conn.Auth))
	case "stdio":
		if conn.Command == "" {
			return nil, fmt.Errorf("the stdio transport requires a command")
		}
		if err := s.startStdio(); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported transport: %s", conn.Transport)
	}

	return s, nil
}

// startStdio starts the stdio child process and wires up the stdin/stdout pipes.
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
	// stderr is discarded (the child's debug output is not part of the protocol).
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start the stdio child process: %w", err)
	}

	s.cmd = cmd
	s.stdin = stdin
	s.stdout = bufio.NewReader(stdout)
	s.nextID = 0
	return nil
}

// Call sends one JSON-RPC request and returns the raw result JSON.
func (s *ClientSession) Call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	if strings.ToLower(s.conn.Transport) == "stdio" {
		return s.callStdio(ctx, method, params)
	}
	return s.callHTTP(ctx, method, params)
}

// CallWithProgress sends one JSON-RPC request and streams progress notifications while it runs.
// onProgress is invoked as the server pushes notifications/progress (concurrently with the main
// goroutine blocked on the call):
//   - stdio: the read loop recognizes frames with method == "notifications/progress" and calls back;
//   - streamable_http: the SSE stream is parsed for progress events and the final result.
func (s *ClientSession) CallWithProgress(ctx context.Context, method string, params interface{}, onProgress func(*ProgressNotificationParams)) (json.RawMessage, error) {
	if strings.ToLower(s.conn.Transport) == "stdio" {
		return s.callStdioWithProgress(ctx, method, params, onProgress)
	}
	return s.callHTTPWithProgress(ctx, method, params, onProgress)
}

// callHTTP issues one JSON-RPC POST under streamable_http.
func (s *ClientSession) callHTTP(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	return s.callHTTPWithProgress(ctx, method, params, nil)
}

// callHTTPWithProgress issues a request under streamable_http.
// A streaming response is text/event-stream: several notifications/progress first, then the final
// result carrying the id. A non-streaming response is a single application/json body. Both are handled
// uniformly, and progress notifications call onProgress while being read.
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

	// Auth header (ignored under stdio, where credentials travel through env).
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

// parseSSE parses a text/event-stream response: progress events call back while being read, and the
// final result is returned. Event format: `event: message\ndata: {json}\n\n`; the connection closes
// when the stream ends (there is no [DONE] marker).
func (s *ClientSession) parseSSE(ctx context.Context, body io.Reader, onProgress func(*ProgressNotificationParams)) (json.RawMessage, error) {
	reader := bufio.NewReader(body)
	for {
		line, err := readSSELine(ctx, reader)
		if err != nil {
			if err == io.EOF {
				return nil, fmt.Errorf("the SSE stream ended early without a final result")
			}
			return nil, err
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))

		// Try a progress notification first.
		var prog rpcProgress
		if err := json.Unmarshal([]byte(payload), &prog); err == nil && prog.Method == "notifications/progress" {
			if onProgress != nil {
				onProgress(&prog.Params)
			}
			continue
		}
		// Otherwise treat it as the final result.
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

// readSSELine reads one line of SSE text (separated by \n) and honours ctx cancellation.
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

// callStdio issues one JSON-RPC request under stdio (\n separated frames, serialized).
func (s *ClientSession) callStdio(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	return s.callStdioWithProgress(ctx, method, params, nil)
}

// callStdioWithProgress issues a request under stdio and recognizes progress notifications in the read
// loop, calling back for each of them.
func (s *ClientSession) callStdioWithProgress(ctx context.Context, method string, params interface{}, onProgress func(*ProgressNotificationParams)) (json.RawMessage, error) {
	s.reqMu.Lock()
	defer s.reqMu.Unlock()

	if s.isClosed() {
		return nil, fmt.Errorf("the RPC stdio session is closed")
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
		return nil, fmt.Errorf("failed to write the stdio request: %w", err)
	}

	// Read responses until the matching id shows up, recognizing the progress notifications the server
	// pushes on its own along the way.
	for {
		line, err := s.readFrame(ctx)
		if err != nil {
			return nil, err
		}
		// Try a progress notification first (method == notifications/progress).
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

// readFrame reads one JSON frame (separated by \n).
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

// ListTools calls tools/list to discover the tool list.
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
		return nil, fmt.Errorf("failed to parse the tools/list result: %w", err)
	}

	// Version negotiation: when the server version is higher than the client one, a warning is expected
	// (internally isomorphic code is usually upgraded in lockstep).
	if listResult.ServerVersion > protocol.CurrentProtocolVersion {
		// Optionally log a warning or return a specific error, left to the caller.
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

// Close closes the session (terminating the child process under stdio).
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

// newHTTPClientWithHeaders returns an *http.Client with the given default headers injected.
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

// headerRoundTripper injects custom headers on every request.
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

// parseArgs parses a JSONB argument array into a string slice.
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

// parseEnv parses a JSONB environment array (["K=V", ...]) into a string slice.
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
