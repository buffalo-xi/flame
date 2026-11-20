package daemon

import (
	"reflect"
	"testing"
)

// The criterion for code_langs must be "the languages that actually have a pool" — it is the only basis on which the
// workflow editor and the agent creating a workflow pick a language. Wider than the set actually served and you get
// "it is in the editor but unsupported_lang at runtime"; narrower and usable languages are hidden for no reason.
func TestCodeLangsFollowsPools(t *testing.T) {
	s := &Server{pools: map[string]*slotPool{"lua": nil, "python": nil}}
	want := []string{"lua", "python"} // stable order (lexical), so the consumer's dropdown need not re-sort every time

	if got := s.codeLangs(); !reflect.DeepEqual(got, want) {
		t.Errorf("codeLangs() = %v, want %v", got, want)
	}

	// A language present in the top-level langs but without a duplex pool must not appear (a language used only for
	// skill scripts is exactly that case).
	s.cfg = &Config{Langs: map[string]LangDef{"ruby": {Command: "ruby"}}}
	if got := s.codeLangs(); !reflect.DeepEqual(got, want) {
		t.Errorf("a language without a pool must not appear in code_langs: %v", got)
	}
}
