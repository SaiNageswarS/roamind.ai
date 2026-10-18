package workflows

import "roamind.ai/agent/protocol"

// TurnTools is the configuration of Roamind's tool sets (bin/mcp) each
// task's turns get, by the task's workflow name. A task not listed gets
// none, and neither do turns that don't get the workspace tools, whose
// input is third-party text.
var TurnTools = map[string][]string{
	ChatTurnName:         {protocol.ServerInterests, protocol.ServerJobs},
	LearningExerciseName: {},
	ResumeImportName:     {},
}

// serverTools are the tool sets the task's turns get: those configured
// for it that the worker can serve.
func (d Deps) serverTools(task string) []string {
	var sets []string
	for _, set := range TurnTools[task] {
		if d.serves(set) {
			sets = append(sets, set)
		}
	}
	return sets
}

// serves reports whether the worker can serve a tool set: jobs needs the
// job assistant, which needs memory, where resumes are.
func (d Deps) serves(set string) bool {
	switch set {
	case protocol.ServerJobs:
		return d.Memory != nil
	}
	return true
}
