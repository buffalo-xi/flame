package agent

import "errors"

// ConfigError is a run that cannot start because the tenant's configuration is missing or unusable.
//
// Why a type instead of a plain error string: every entry point has to answer in its own protocol - SSE carries an
// error code the console translates, JSON-RPC has an implementation-defined code range, MCP only has isError, and a
// flow node has ErrCodeConfig. What they all need to know is the same single thing: this is not a server fault, it is
// a setting the operator must fix. Carrying the classification on the error keeps the entry points from each
// re-deriving it from the message text (which is how "no default model" ended up reported as an internal error).
type ConfigError struct {
	Code   string            // application code, translatable in the console (error.<Code> in the locale bundle)
	Params map[string]string // copy variables for that code
	Msg    string            // English diagnostic: logs, curl, protocol messages and the model
}

func (e *ConfigError) Error() string { return e.Msg }

// AsConfigError reports whether err is (or wraps) a configuration failure, plus the code to render.
func AsConfigError(err error) (*ConfigError, bool) {
	var ce *ConfigError
	if errors.As(err, &ce) {
		return ce, true
	}
	return nil, false
}

// The two ways "there is no usable default model" can happen. Both are configuration states rather than bugs, so
// they are reported as config errors and never as an internal error.
var (
	// ErrNoDefaultModel: no row with is_default = true in llm_models.
	ErrNoDefaultModel = &ConfigError{
		Code: "llm.no_default_model",
		Msg:  "no default LLM model is configured: mark one model as the default in the console first",
	}
	// ErrDefaultModelNotEnabled: the default row exists but was never provisioned, so it has no gateway id and the
	// gateway cannot route it. Same rule as the flow llm node (see service/flow/executors/llm.go): calling it would
	// fail deeper with a far less actionable message.
	//
	// The condition is an empty gateway_id, not an empty gateway_model: the seeded model already carries a
	// gateway_model, so a message about "the gateway model name" sends the reader looking at a field that is
	// filled in correctly.
	ErrDefaultModelNotEnabled = &ConfigError{
		Code: "llm.default_model_not_enabled",
		Msg:  "the default LLM model is not enabled (it has no gateway id because it was never provisioned): provision it in the console first",
	}
)
