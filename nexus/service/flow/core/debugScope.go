package flow

import "sync"

// DraftRef is one entity whose draft was followed by a debug run.
//
// It lands in the run record (flow_run_records.drafts) to answer "which drafts produced this run" —
// exactly what blindly propagating drafts loses: if the content inside the scope changed, it must
// still be possible to say afterwards which copy was used at the time.
type DraftRef struct {
	Kind string `json:"kind"` // flow / agent / skill / tool
	ID   string `json:"id"`
	Name string `json:"name"`
	Note string `json:"note,omitempty"` // e.g. "never released", "has unreleased changes"
}

// DebugScope is the scope of one debug session.
//
// It only has two sources: the editor debugger (the flowDebugSource websocket) and a run_flow trial
// run. Every other entry point (MCP / RPC / the agent's local__ tools / user chat) leaves it unset
// when building the execution context, so they structurally cannot obtain a scope — it is not a
// matter of "remembering not to set it".
//
// The scope is inherited along in-process internal calls (flow → flow, and later flow → agent → flow);
// calls crossing a process boundary (MCP / RPC / HTTP) have no scope at all.
//
// The semantics are "drafts travel along ownership and dependencies, but never across processes":
//   - the callee is editable by the debugger and not archived → use the draft and record it in Drafts;
//   - otherwise → use the released version (error out when there is none, never fall back silently).
type DebugScope struct {
	// FollowDrafts controls whether dependencies follow their drafts. It is the switch in the debug
	// panel and defaults to on: turning it off means "run only my own layer", used to confirm
	// "is the change to A alone harmless".
	FollowDrafts bool

	mu     sync.Mutex
	drafts []DraftRef
}

// NewDebugScope creates a debug scope.
func NewDebugScope(followDrafts bool) *DebugScope {
	return &DebugScope{FollowDrafts: followDrafts}
}

// Enabled reports whether this is a draft-following debug scope; nil safe (production runs return false).
func (s *DebugScope) Enabled() bool {
	return s != nil && s.FollowDrafts
}

// RecordDraft records whose draft was followed. The same entity is recorded once, so concurrent
// nodes do not write it twice.
//
// Concurrent nodes (mono / parallel) call this from several goroutines, hence the lock.
func (s *DebugScope) RecordDraft(kind, id, name, note string) {
	if s == nil || id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.drafts {
		if d.Kind == kind && d.ID == id {
			return
		}
	}
	s.drafts = append(s.drafts, DraftRef{Kind: kind, ID: id, Name: name, Note: note})
}

// Drafts returns the drafts followed by this run (a copy, for persistence).
func (s *DebugScope) Drafts() []DraftRef {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.drafts) == 0 {
		return nil
	}
	out := make([]DraftRef, len(s.drafts))
	copy(out, s.drafts)
	return out
}
