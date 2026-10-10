package protocol

// TurnInput is everything the agent needs to run one conversational turn.
type TurnInput struct {
	UserID string `json:"user_id"`
	// SessionID is the claude session to resume; empty starts a new one.
	SessionID string `json:"session_id,omitempty"`
	Text      string `json:"text"`
	// UserName is shown to the model so it can address the user.
	UserName string `json:"user_name,omitempty"`
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
}
