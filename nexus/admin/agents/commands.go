package agents

import (
	"context"

	"flame/nexus/service/agent"
	"flame/nexus/service/agent/tools"
	"flame/nexus/shared/auth"
	"flame/tools/xhttp"

	"github.com/gin-gonic/gin"
)

// commandItem is one "/" entry in the chat window.
//
// Dispatch and Tool are omitted when the command only injects a body: the menu shows a tool badge for the ones that bypass the
// model, and a plain skill entry for the rest.
type commandItem struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Dispatch    string `json:"dispatch,omitempty"`
	Tool        string `json:"tool,omitempty"`
}

// ListAgentCommands lists the skills this agent exposes as user commands ("/<name>").
//
// The visibility criterion is the agent layer's List (private + bound, plus whatever this agent's discovery setting admits),
// narrowed to user_invocable. That is deliberate: a command means "the user picks a skill for this agent", never "the user
// grants it something new" - a wider menu would let "/" reach a shared skill that restricted keeps away from this agent.
//
// Draft mode matches the trial chat, so the menu and the run that follows it read the same copy of a skill.
func ListAgentCommands(c *gin.Context) {
	authInfo, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return
	}
	ai := authInfo.(*auth.AuthInfo)

	instance, err := repo.GetAgent(ai, c.Param("id"))
	if err != nil {
		xhttp.ErrCode(c, "agent.not_found", nil, "Agent instance not found", "")
		return
	}
	st, serr := getStorage()
	if serr != nil {
		xhttp.ErrCode(c, "common.internal", nil, "storage not available: "+serr.Error(), "")
		return
	}

	mgr := agent.NewSkillManager(ai.Schema, instance.ID, instance.Name, ai.ID, st,
		instance.SkillDiscovery, instance.IsSupervisor, tools.ModeDraft)
	list, lerr := mgr.ListCommands(context.Background())
	if lerr != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to list commands: "+lerr.Error(), "")
		return
	}

	items := make([]commandItem, 0, len(list))
	for _, s := range list {
		items = append(items, commandItem{
			Name:        s.Name,
			Description: s.Description,
			Dispatch:    s.CommandDispatch,
			Tool:        s.CommandTool,
		})
	}
	xhttp.OkJSON(c, "Success", gin.H{"items": items}, "")
}
