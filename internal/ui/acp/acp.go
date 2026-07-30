// Package acp is a headless frontend that speaks a minimal subset of the
// Agent Client Protocol (JSON-RPC 2.0 over stdio, newline-delimited) so
// another process can drive this agent as a long-lived subagent.
//
// Only the methods a non-interactive caller needs are implemented:
// initialize, session/new, and session/prompt. There is no permission
// round-trip (session/request_permission) and no session/cancel: a
// subagent spawned this way is expected to run trusted, read-only tools
// with no destructive action requiring approval.
package acp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/toddky/menehune/internal/agent"
	"github.com/toddky/menehune/internal/llm"
)

// protocolVersion is the ACP schema revision this subset targets.
// Bump this if the wire methods below change to track a newer ACP schema.
const protocolVersion = 1

// rpcRequest is one incoming JSON-RPC line; ID is absent for notifications.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// rpcResponse is one outgoing JSON-RPC reply, success or error.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// rpcNotification is a request with no ID: session/update goes out this way.
type rpcNotification struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
}

type initializeResult struct {
	ProtocolVersion   int             `json:"protocolVersion"`
	AgentCapabilities map[string]bool `json:"agentCapabilities"`
}

type newSessionResult struct {
	SessionID string `json:"sessionId"`
}

type promptParameters struct {
	SessionID string         `json:"sessionId"`
	Prompt    []contentBlock `json:"prompt"`
}

// contentBlock mirrors ACP's content block shape; only "text" is produced
// or consumed by this subset, matching what a headless caller sends.
type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type promptResult struct {
	StopReason string `json:"stopReason"`
}

type sessionUpdateParameters struct {
	SessionID string      `json:"sessionId"`
	Update    interface{} `json:"update"`
}

type agentMessageChunk struct {
	SessionUpdate string       `json:"sessionUpdate"`
	Content       contentBlock `json:"content"`
}

type toolCallUpdate struct {
	SessionUpdate string `json:"sessionUpdate"`
	ToolCallID    string `json:"toolCallId"`
	Title         string `json:"title"`
	Status        string `json:"status"`
	RawInput      string `json:"rawInput,omitempty"`
	RawOutput     string `json:"rawOutput,omitempty"`
}

// Run reads JSON-RPC requests from input, one per line, and writes
// responses/notifications to output, one per line, until input hits EOF
// or a read error occurs. engine drives every session's Turn.
// sessions maps sessionId to that conversation's message history, so
// repeated session/prompt calls over the process's life keep context.
func Run(engine *agent.Agent, input io.Reader, output io.Writer) error {
	sessions := map[string][]llm.Message{}
	scanner := bufio.NewScanner(input)
	// ACP messages can carry large tool output; grow past bufio's 64KiB
	// default. 16MiB is a generous, arbitrary cap with no observed need for more.
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var request rpcRequest
		if err := json.Unmarshal(line, &request); err != nil {
			writeError(output, nil, -32700, fmt.Sprintf("parse error: %v", err))
			continue
		}
		handle(engine, sessions, request, output)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read request: %w", err)
	}
	return nil
}

func handle(engine *agent.Agent, sessions map[string][]llm.Message, request rpcRequest, output io.Writer) {
	switch request.Method {
	case "initialize":
		writeResult(output, request.ID, initializeResult{
			ProtocolVersion:   protocolVersion,
			AgentCapabilities: map[string]bool{"loadSession": false},
		})

	case "session/new":
		id := fmt.Sprintf("sess-%d", len(sessions)+1)
		sessions[id] = nil
		writeResult(output, request.ID, newSessionResult{SessionID: id})

	case "session/prompt":
		var parameters promptParameters
		if err := json.Unmarshal(request.Params, &parameters); err != nil {
			writeError(output, request.ID, -32602, fmt.Sprintf("invalid params: %v", err))
			return
		}
		if _, known := sessions[parameters.SessionID]; !known {
			writeError(output, request.ID, -32602, fmt.Sprintf("unknown sessionId %q", parameters.SessionID))
			return
		}
		handlePrompt(engine, sessions, parameters, request.ID, output)

	default:
		writeError(output, request.ID, -32601, fmt.Sprintf("method not found: %s", request.Method))
	}
}

// handlePrompt runs one Turn against the session's history and replies
// with the final promptResult, keeping context across repeated calls.
func handlePrompt(engine *agent.Agent, sessions map[string][]llm.Message, parameters promptParameters, id json.RawMessage, output io.Writer) {
	var textParts []string
	for _, block := range parameters.Prompt {
		if block.Type == "text" {
			textParts = append(textParts, block.Text)
		}
	}
	text := strings.Join(textParts, "")

	history := append(sessions[parameters.SessionID], llm.TextMessage("user", text))
	notify := func(event agent.Event) {
		writeSessionUpdate(output, parameters.SessionID, event)
	}

	updated, err := engine.Turn(history, notify)
	var exitRequest *agent.ExitRequest
	if errors.As(err, &exitRequest) {
		writeResult(output, id, promptResult{StopReason: "end_turn"})
		return
	}
	if err != nil {
		writeError(output, id, -32000, err.Error())
		return
	}

	sessions[parameters.SessionID] = updated
	writeResult(output, id, promptResult{StopReason: "end_turn"})
}

// writeSessionUpdate converts one engine event into an ACP session/update
// notification: text deltas stream as agent_message_chunk, tool activity as tool_call updates.
func writeSessionUpdate(output io.Writer, sessionID string, event agent.Event) {
	switch event.Type {
	case agent.EventTextDelta:
		writeNotification(output, "session/update", sessionUpdateParameters{
			SessionID: sessionID,
			Update: agentMessageChunk{
				SessionUpdate: "agent_message_chunk",
				Content:       contentBlock{Type: "text", Text: event.Text},
			},
		})
	case agent.EventToolCallStarted:
		writeNotification(output, "session/update", sessionUpdateParameters{
			SessionID: sessionID,
			Update: toolCallUpdate{
				SessionUpdate: "tool_call",
				ToolCallID:    event.ToolName,
				Title:         event.ToolName,
				Status:        "in_progress",
				RawInput:      event.ToolInput,
			},
		})
	case agent.EventToolResult:
		status := "completed"
		if event.IsError {
			status = "failed"
		}
		writeNotification(output, "session/update", sessionUpdateParameters{
			SessionID: sessionID,
			Update: toolCallUpdate{
				SessionUpdate: "tool_call_update",
				ToolCallID:    event.ToolName,
				Title:         event.ToolName,
				Status:        status,
				RawOutput:     event.Result,
			},
		})
	}
}

func writeResult(output io.Writer, id json.RawMessage, result interface{}) {
	payload, err := json.Marshal(result)
	if err != nil {
		writeError(output, id, -32603, fmt.Sprintf("marshal result: %v", err))
		return
	}
	writeLine(output, rpcResponse{JSONRPC: "2.0", ID: id, Result: payload})
}

func writeError(output io.Writer, id json.RawMessage, code int, message string) {
	writeLine(output, rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}})
}

func writeNotification(output io.Writer, method string, parameters interface{}) {
	writeLine(output, rpcNotification{JSONRPC: "2.0", Method: method, Params: parameters})
}

// writeLine marshals value as one JSON line. A marshal failure here means a
// Go value in this package cannot serialize; print to stderr, don't drop it silently.
func writeLine(output io.Writer, value interface{}) {
	payload, err := json.Marshal(value)
	if err != nil {
		fmt.Fprintf(os.Stderr, "acp: marshal frame: %v\n", err)
		return
	}
	payload = append(payload, '\n')
	if _, err := output.Write(payload); err != nil {
		fmt.Fprintf(os.Stderr, "acp: write frame: %v\n", err)
	}
}
