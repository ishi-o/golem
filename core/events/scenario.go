package events

import (
	"github.com/ishi-o/golem/core/agent"
	"github.com/ishi-o/golem/core/knowledge"
	"github.com/ishi-o/golem/core/tools"
)

// TriageScenario is the memoryless, unattended scenario used by Sweeper.
// Schedule and subagent tools are excluded to prevent an event body from
// creating unbounded background work.
var TriageScenario agent.Scenario = triageScenario{}

type triageScenario struct{}

func (triageScenario) Name() string             { return "EVENT_TRIAGE" }
func (triageScenario) ConversationMemory() bool { return false }
func (triageScenario) Offers(name string) bool {
	switch name {
	case tools.ToolNameStartSubagent, tools.ToolNameWaitSubagent, tools.ToolNameCancelSubagent,
		tools.ToolNameCreateScheduledTask, tools.ToolNameListScheduledTasks, tools.ToolNameCancelScheduledTask,
		tools.ToolNameUpdateScheduledTask, tools.ToolNameStopScheduledTask, tools.ToolNameRescheduleTask,
		knowledge.ToolNameListOwnerKnowledge, knowledge.ToolNameSearchOwnerKnowledge,
		tools.ToolNameListPlaybooks, tools.ToolNameWritePlaybook:
		return false
	default:
		return true
	}
}
