package store

import "testing"

func TestLifecycleActionsRouteToDedicatedTopics(t *testing.T) {
	cases := map[string]string{
		"pipeline.submitted": "kionga.pipeline.commands",
		"model.promoted":     "kionga.model.commands",
		"agent.deployed":     "kionga.agent.commands",
		"tool.registered":    "kionga.tool.commands",
		"connection.created": "kionga.connection.commands",
		"access.upserted":    "kionga.workspace.commands",
		"project.created":    "",
	}
	for action, expected := range cases {
		if got := topicForAction(action); got != expected {
			t.Errorf("%s: expected %q, got %q", action, expected, got)
		}
	}
}
