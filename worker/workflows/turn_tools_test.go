package workflows

import (
	"reflect"
	"testing"

	"roamind.ai/agent/protocol"
	"roamind.ai/memory/store/notes"
)

func TestServerTools(t *testing.T) {
	var d Deps
	if got := d.serverTools(ChatTurnName); !reflect.DeepEqual(got, []string{protocol.ServerInterests}) {
		t.Fatalf("chat turns without the job assistant get %v", got)
	}
	d.Web, d.Memory = newFakeWeb(), &notes.Notes{}
	if got := d.serverTools(ChatTurnName); !reflect.DeepEqual(got, []string{protocol.ServerInterests, protocol.ServerJobs}) {
		t.Fatalf("chat turns get %v", got)
	}
	for _, task := range []string{LearningExerciseName, ResumeImportName, JobLinksName} {
		if got := d.serverTools(task); got != nil {
			t.Errorf("%s turns get %v", task, got)
		}
	}
}
