package protocol

import "encoding/json"

// TurnInput is everything the agent needs to run one conversational turn.
type TurnInput struct {
	UserID string `json:"user_id"`
	// SessionID is the claude session to resume; empty starts a new one.
	SessionID string `json:"session_id,omitempty"`
	Text      string `json:"text"`
	// UserName is shown to the model so it can address the user.
	UserName string `json:"user_name,omitempty"`
	// UserEmail is the user's verified email (their identity), shown to the
	// model as theirs.
	UserEmail string `json:"user_email,omitempty"`
	// MemoryProject is the user's Basic Memory project; empty runs the turn
	// without memory.
	MemoryProject string `json:"memory_project,omitempty"`
	// Goal is the memory note the conversation is about (in a goal's
	// channel); nil for general conversations.
	Goal *GoalRef `json:"goal,omitempty"`
	// Exercise is set in the thread of a daily exercise for Goal, where the
	// user answers it.
	Exercise *ExerciseRef `json:"exercise,omitempty"`
	// OutputSchema, a JSON Schema, makes the turn return structured output
	// (TurnOutput.Structured) instead of chat text.
	OutputSchema string `json:"output_schema,omitempty"`
}

// ExerciseRef names an exercise note in the user's memory.
type ExerciseRef struct {
	Permalink string `json:"permalink"`
}

// GoalRef names a goal note in the user's memory.
type GoalRef struct {
	Title     string `json:"title"`
	Permalink string `json:"permalink"`
}

// TurnOutput is the final result of a turn.
type TurnOutput struct {
	SessionID string            `json:"session_id"`
	Text      string            `json:"text"`
	Citations map[int]SearchDoc `json:"citations,omitempty"`
	CostUSD   float64           `json:"cost_usd"`
	NumTurns  int               `json:"num_turns"`
	// SessionReset is true when the requested session could not be resumed
	// and the turn ran in a fresh session.
	SessionReset bool `json:"session_reset,omitempty"`
	// Structured is the turn's output when TurnInput.OutputSchema was set.
	Structured json.RawMessage `json:"structured,omitempty"`
}
