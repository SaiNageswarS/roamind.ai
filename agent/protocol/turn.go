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
	// News is set for turns in the user's news-shorts channel.
	News *NewsRef `json:"news,omitempty"`
	// Tools is the set of tools the turn gets: ToolsWorkspace, ToolsWeb or
	// ToolsNone.
	Tools string `json:"tools,omitempty"`
	// OutputSchema, a JSON Schema, makes the turn return structured output
	// (TurnOutput.Structured) instead of chat text.
	OutputSchema string `json:"output_schema,omitempty"`
}

// ExerciseRef names an exercise note in the user's memory.
type ExerciseRef struct {
	Permalink string `json:"permalink"`
}

// Tool sets for TurnInput.Tools.
const (
	// ToolsWorkspace is the full set: the web, and files and commands in the
	// user's workspace, plus the memory and Roamind MCP servers. It is also
	// what the zero value gives.
	ToolsWorkspace = ""
	// ToolsWeb is WebSearch and WebFetch only, in the user's workspace
	// directory, so the session can be resumed from a chat turn.
	ToolsWeb = "web"
	// ToolsNone is no tools at all, in a throwaway directory, with no saved
	// session: for turns whose whole input is third-party text, so nothing
	// in it can steer the turn at the user's files or the web.
	ToolsNone = "none"
)

// NewsRef is the context of a turn in the user's news-shorts channel.
type NewsRef struct {
	// Interests are the catalog interests the user follows.
	Interests []InterestRef `json:"interests,omitempty"`
	// Digest is the digest whose thread the turn is in, as posted; empty
	// for top-level messages.
	Digest string `json:"digest,omitempty"`
}

// InterestRef is a news interest the user follows.
type InterestRef struct {
	Interest string `json:"interest"`
	Status   string `json:"status"` // active or muted
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
