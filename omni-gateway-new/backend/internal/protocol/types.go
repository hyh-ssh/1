package protocol

import (
	"encoding/json"

	"github.com/zjvtit/omni-gateway/internal/model"
)

// ContentPart is a single part of a message (text or image).
type ContentPart struct {
	Type      string          `json:"type"` // "text" | "image_url" | "input_image"
	Text      string          `json:"text,omitempty"`
	ImageURL  string          `json:"image_url,omitempty"`
	ImageData string          `json:"image_data,omitempty"` // base64 data
	MediaType string          `json:"media_type,omitempty"`
	Raw       json.RawMessage `json:"-"` // exact same-protocol content part
}

// Message is a normalized chat message.
type Message struct {
	Role             string        `json:"role"` // system | developer | user | assistant | tool
	Content          []ContentPart `json:"content"`
	ReasoningContent string        `json:"reasoning_content,omitempty"`
	Name             string        `json:"name,omitempty"`
	ToolCallID       string        `json:"tool_call_id,omitempty"`
	ToolCalls        []ToolCall    `json:"tool_calls,omitempty"`
	// Extra contains same-protocol message fields that cannot be represented by
	// the normalized shape. Cross-protocol adapters must reject them explicitly.
	Extra map[string]json.RawMessage `json:"-"`
}

// ToolCall is a normalized tool call.
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Index    *int             `json:"index,omitempty"`
	Function ToolCallFunction `json:"function"`
	Raw      json.RawMessage  `json:"-"` // exact same-protocol tool call
}

// ToolCallFunction is the function part of a tool call.
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Tool is a tool definition.
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
	// Raw keeps non-function tool definitions intact on OpenAI-compatible routes.
	Raw json.RawMessage `json:"-"`
}

// ToolFunction is the function part of a tool definition.
type ToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// ChatRequest is a normalized chat completion request.
type ChatRequest struct {
	Model           string
	Messages        []Message
	Tools           []Tool
	ToolChoice      json.RawMessage
	Temperature     *float64
	TopP            *float64
	MaxTokens       *int
	MaxTokensField  string // "max_completion_tokens" to preserve the inbound key; default is "max_tokens"
	Stop            []string
	Stream          bool
	ResponseFormat  json.RawMessage // JSON schema / structured output
	ReasoningEffort string
	User            string
	Extra           map[string]json.RawMessage // passthrough for same-protocol
	// Populated by the gateway from the selected upstream route, never by the
	// public request or model alias.
	CompatibilityProfile   string
	ModelSupportsReasoning bool
}

// ChatResponse is a normalized chat completion response.
type ChatResponse struct {
	ID                string
	Model             string
	Choices           []ChatChoice
	Usage             model.Usage
	Created           int64
	SystemFingerprint string
	Raw               json.RawMessage
}

// ChatChoice is a normalized chat choice.
type ChatChoice struct {
	Index        int
	Message      Message
	FinishReason string
	LogProbs     json.RawMessage
}

// ToolCallDelta is one partial tool call carried by a streaming event. The
// Responses API reports function-call names and arguments as separate deltas;
// ToolCallDelta holds them in the same shape the Chat path already uses inside
// Message.ToolCalls, so either can be reassembled without knowing the upstream
// protocol.
type ToolCallDelta struct {
	Index     int    `json:"index"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// StreamEvent is a normalized streaming event.
type StreamEvent struct {
	Type              string // "chunk" | "usage" | "done" | "error"
	Index             int    // choice index for Chat chunks, output index for Responses
	ResponseID        string // upstream response id when the provider reports one
	Created           int64
	LogProbs          json.RawMessage
	SystemFingerprint string
	Event             string // explicit upstream event type (Responses API)
	Delta             Message
	ToolCalls         []ToolCallDelta // Responses function-call name/argument deltas
	FinishReason      string
	Usage             model.Usage
	Error             string
	Raw               json.RawMessage
}

// ResponsesRequest is a normalized Responses API request.
type ResponsesRequest struct {
	Model string
	// Input stays raw so unknown input fields survive a same-protocol
	// passthrough. Messages is the normalized view of Input, populated when
	// Input is a string or an array of chat-like input items.
	Input              json.RawMessage
	Messages           []Message
	Instructions       string
	Stream             bool
	PreviousResponseID string
	Extra              map[string]json.RawMessage
}

// ResponsesResponse is a normalized Responses API response.
type ResponsesResponse struct {
	ID     string
	Model  string
	Output json.RawMessage
	Usage  model.Usage
	Status string
	Raw    json.RawMessage
}
