package executors

import (
	"context"
	"strings"
	"testing"

	"flame/nexus/service/flow/core"
)

// 固定输出变量由执行器写，脚本返回同名键必须被忽略：
// string 是追加语义，两边都写会把"最终结果"和"脚本拼的片段"混在一起，
// 变量到底是什么就没有答案了。
func TestApplyHandlerAppendDeniesFixedOutputVars(t *testing.T) {
	node := &flow.Node{ID: "n1", Type: "rpc"}
	nr := flow.NewNodeResult(node, "1", &flow.ExecContext{})
	nr.InitOutputVars(map[string]interface{}{
		"result": map[string]interface{}{"type": "string"},
		"note":   map[string]interface{}{"type": "string"},
	})

	if err := applyHandlerAppend(nr, map[string]any{
		"result": "脚本想写的结果",
		"note":   "片段",
	}, rpcReservedOutputVars); err != nil {
		t.Fatalf("applyHandlerAppend 失败: %v", err)
	}

	if v, _ := nr.GetVariable("result"); v != "" {
		t.Errorf("result = %v，应保持空串（固定变量不许脚本写）", v)
	}
	if v, _ := nr.GetVariable("note"); v != "片段" {
		t.Errorf("note = %v，want 片段（自定义变量应可写）", v)
	}
}

// 逐帧调用会反复命中同一条冲突，日志必须去重：500 帧不该刷 500 行。
func TestApplyHandlerAppendWarnsOncePerVar(t *testing.T) {
	node := &flow.Node{ID: "n1", Type: "rpc"}
	nr := flow.NewNodeResult(node, "1", &flow.ExecContext{})
	nr.InitOutputVars(map[string]interface{}{
		"result": map[string]interface{}{"type": "string"},
	})

	for i := 0; i < 5; i++ {
		if err := applyHandlerAppend(nr, map[string]any{"result": "x"}, rpcReservedOutputVars); err != nil {
			t.Fatalf("applyHandlerAppend 失败: %v", err)
		}
	}

	warned := 0
	for _, line := range nr.GetLogs() {
		if strings.Contains(line, "ignored") {
			warned++
		}
	}
	if warned != 1 {
		t.Errorf("固定变量告警记了 %d 条，want 1（同一变量在一次执行内只提示一次）", warned)
	}
}

// 固定变量不再取决于"有没有脚本"：无脚本照写，有脚本也照写。
// 脚本失败时也应该能看到已写入的 result（写入发生在脚本之前）。
func TestFinishRpcResultWritesFixedVarsRegardlessOfHandler(t *testing.T) {
	newNR := func() *flow.NodeResult {
		node := &flow.Node{ID: "n1", Type: "rpc"}
		nr := flow.NewNodeResult(node, "1", &flow.ExecContext{StdCtx: context.Background()})
		nr.InitOutputVars(map[string]interface{}{
			"result":   map[string]interface{}{"type": "string"},
			"progress": map[string]interface{}{"type": "array[string]"},
			"error":    map[string]interface{}{"type": "string"},
		})
		return nr
	}

	t.Run("无脚本写 result", func(t *testing.T) {
		nr := newNR()
		e := NewRpcExecutor()
		if err := e.finishRpcResult(nr.Ctx, nr, nil, false, "", "", 0, "hello", false, nil, nil); err != nil {
			t.Fatalf("finishRpcResult: %v", err)
		}
		if v, _ := nr.GetVariable("result"); v != "hello" {
			t.Errorf("result = %v，want hello", v)
		}
		if v, _ := nr.GetVariable("error"); v != "" {
			t.Errorf("error = %v，应为空串", v)
		}
	})

	t.Run("远端业务错误写 error 而不是 result", func(t *testing.T) {
		nr := newNR()
		e := NewRpcExecutor()
		if err := e.finishRpcResult(nr.Ctx, nr, nil, false, "", "", 0, "boom", true, nil, nil); err != nil {
			t.Fatalf("finishRpcResult: %v", err)
		}
		if v, _ := nr.GetVariable("error"); v != "boom" {
			t.Errorf("error = %v，want boom", v)
		}
		if v, _ := nr.GetVariable("result"); v != "" {
			t.Errorf("result = %v，应为空串", v)
		}
	})

	t.Run("有脚本时 result 仍写入", func(t *testing.T) {
		nr := newNR()
		e := NewRpcExecutor()
		// 测试环境没有脚本客户端，handler 必然执行失败；这里断言的是顺序：
		// 固定变量在脚本之前写，脚本失败不影响已经落地的 result。
		_ = e.finishRpcResult(nr.Ctx, nr, nil, true, "lua", "return {}", 0, "hello", false, nil, []string{})
		if v, _ := nr.GetVariable("result"); v != "hello" {
			t.Errorf("result = %v，want hello（固定变量先于脚本写入）", v)
		}
	})
}
