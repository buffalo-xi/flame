package flow

import (
	"fmt"
	"testing"
)

// iterations is readable as a variable, not only visible in the run record's extra: it is the exact round count, which the
// loop vars cannot express for the two shapes that matter - a loop that ran no round at all, and one that break / exit cut
// short (there index / item only hold the last round).
func TestLoopResultIterationsVariable(t *testing.T) {
	lr := &LoopResult{}
	lr.IncIteration()
	lr.IncIteration()

	got, err := lr.GetVariable("iterations")
	if err != nil || got != 2 {
		t.Fatalf("GetVariable(iterations) = %v, %v; want 2", got, err)
	}
	if _, err := lr.GetVariable("nope"); err == nil {
		t.Error("an unknown name must still be an error")
	}

	// A loop var the author happened to name "iterations" wins: that declaration is theirs, the reserved name is the fallback.
	if err := lr.SetLoopVars(map[string]interface{}{"iterations": 7}); err != nil {
		t.Fatalf("SetLoopVars = %v", err)
	}
	// Loop vars are stored through the normalising path, so the type is not the int the test set: compare the value as text.
	if got, _ := lr.GetVariable("iterations"); fmt.Sprint(got) != "7" {
		t.Errorf("GetVariable(iterations) = %v, want the declared loop var (7)", got)
	}
}
