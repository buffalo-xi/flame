package agent

import (
	"context"
	"errors"

	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/auth"
)

// RunFlowTestFunc runs a workflow in-process for a trial run; flow/executors registers it.
//
// Why another registration point: executors imports this package in reverse (the rpc node calls agent endpoints directly, see
// executors/rpc_target.go), so this package cannot import it back without creating a cycle. The same reasoning as
// SetLocalEndpointRunner; only the subject differs - "run an endpoint that is already exposed" vs "run a flow named by id", the latter
// needing no endpoint and checking no exposed flag, which is what the run_flow trial run wants.
type RunFlowTestFunc func(
	ctx context.Context,
	authInfo *auth.AuthInfo,
	flowID string,
	args map[string]any,
	sessionID string,
	callDepth int,
	followDrafts bool,
) (*tools.FlowRunResult, error)

var runFlowTest RunFlowTestFunc

// SetFlowTestRunner is called by flow/executors from init and takes effect process-wide.
// Any process that loads flow (the main / websocket / rpc / mcp entries all import that package) gets it automatically.
func SetFlowTestRunner(fn RunFlowTestFunc) { runFlowTest = fn }

// errFlowTestUnavailable means the execution channel is not wired: state the reason clearly rather than letting the tool "succeed with an
// empty result".
var errFlowTestUnavailable = errors.New(
	"trial run unavailable: the flow executor is not loaded in this process (run_flow needs /v1/code, the sandbox and the executor registration)")
