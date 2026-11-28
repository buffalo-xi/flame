package flowcheck

import "testing"

// skipReaderDiagram wires start -> producer -> reader. The producer declares one output; whether it also skips, and whether
// it leaves a fallback, is what each case varies.
func skipReaderDiagram(producerCfg, readerCfg map[string]any) map[string]any {
	return map[string]any{
		"nodes": []any{
			psNode("1", "start", "", false, nil),
			psNode("2", "code", "", false, producerCfg),
			psNode("3", "code", "", false, readerCfg),
		},
		"edges": []any{testEdge("e1", "1", "2"), testEdge("e2", "2", "3")},
	}
}

func skipProducer(onErrorExtra map[string]any) map[string]any {
	onError := map[string]any{"skip": map[string]any{"codes": []any{"not_found"}}}
	for k, v := range onErrorExtra {
		onError[k] = v
	}
	return map[string]any{
		"outputs":  map[string]any{"out": map[string]any{"type": "string"}},
		"on_error": onError,
	}
}

func readsOut() map[string]any {
	return map[string]any{"inputs": map[string]any{
		"x": map[string]any{"type": "string", "source_type": "node", "source_ref": "2", "source_key": "out"},
	}}
}

// A skip without a fallback plus a downstream reader is the case the check exists for: the reader gets "" and the run still
// reports success, so nothing else would ever point at it.
func TestSkippedOutputReadIsReported(t *testing.T) {
	fs := checkDiagramMap(t, skipReaderDiagram(skipProducer(nil), readsOut()))
	if !has(fs, SeverityWarning, CodeSkippedOutputRead) {
		t.Fatalf("codes = %v, want %s", codes(fs), CodeSkippedOutputRead)
	}
}

// A fallback that writes a value makes the skip visible downstream: post.set_outputs on a type that keeps post.
func TestSkippedOutputReadQuietWithPostOutputs(t *testing.T) {
	cfg := skipProducer(map[string]any{"post": map[string]any{"set_outputs": map[string]any{"out": ""}}})
	fs := checkDiagramMap(t, skipReaderDiagram(cfg, readsOut()))
	if has(fs, SeverityWarning, CodeSkippedOutputRead) {
		t.Fatalf("codes = %v, want no %s", codes(fs), CodeSkippedOutputRead)
	}
}

// A handler runs on the failure path and may write the fallback itself, so it counts as a fallback too.
func TestSkippedOutputReadQuietWithHandler(t *testing.T) {
	cfg := skipProducer(map[string]any{
		"handler": map[string]any{"codes": []any{"other"}, "lang": "lua", "script": "return { out = 'x' }"},
	})
	fs := checkDiagramMap(t, skipReaderDiagram(cfg, readsOut()))
	if has(fs, SeverityWarning, CodeSkippedOutputRead) {
		t.Fatalf("codes = %v, want no %s", codes(fs), CodeSkippedOutputRead)
	}
}

// A host keeps post (process / loop declare HasPost), so skip + post.set_outputs is the way to leave a marker on a
// failed concurrent section: the warning must stay quiet, exactly as for any other type.
func TestSkippedOutputReadQuietWithPostOnHost(t *testing.T) {
	producer := psNode("2", "process", "", false, map[string]any{
		"outputs": map[string]any{"out": map[string]any{"type": "string"}},
		"on_error": map[string]any{
			"skip": map[string]any{"codes": []any{"timeout"}},
			"post": map[string]any{"set_outputs": map[string]any{"out": "partial"}},
		},
	})
	diagram := map[string]any{
		"nodes": []any{psNode("1", "start", "", false, nil), producer, psNode("3", "code", "", false, readsOut())},
		"edges": []any{testEdge("e1", "1", "2"), testEdge("e2", "2", "3")},
	}
	fs := checkDiagramMap(t, diagram)
	if has(fs, SeverityWarning, CodeSkippedOutputRead) {
		t.Fatalf("codes = %v, want no %s (the host keeps post)", codes(fs), CodeSkippedOutputRead)
	}
}

// No reader means nothing to protect: the skip is the author's business.
func TestSkippedOutputReadQuietWithoutReader(t *testing.T) {
	diagram := map[string]any{
		"nodes": []any{
			psNode("1", "start", "", false, nil),
			psNode("2", "code", "", false, skipProducer(nil)),
			psNode("3", "code", "", false, map[string]any{}),
		},
		"edges": []any{testEdge("e1", "1", "2"), testEdge("e2", "2", "3")},
	}
	fs := checkDiagramMap(t, diagram)
	if has(fs, SeverityWarning, CodeSkippedOutputRead) {
		t.Fatalf("codes = %v, want no %s", codes(fs), CodeSkippedOutputRead)
	}
}

// An empty codes list disables skip at run time, so there is nothing to warn about.
func TestSkippedOutputReadQuietWithoutCodes(t *testing.T) {
	cfg := map[string]any{
		"outputs":  map[string]any{"out": map[string]any{"type": "string"}},
		"on_error": map[string]any{"skip": map[string]any{"codes": []any{}}},
	}
	fs := checkDiagramMap(t, skipReaderDiagram(cfg, readsOut()))
	if has(fs, SeverityWarning, CodeSkippedOutputRead) {
		t.Fatalf("codes = %v, want no %s", codes(fs), CodeSkippedOutputRead)
	}
}
