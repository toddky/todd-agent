package agent

import (
	"github.com/toddky/todd-agent/internal/llm"
)

type Agent struct {
	Client *llm.Client
	Tools  *Registry
}

// Turn sends the message history and loops on tool calls until the model stops requesting tools.
// Each event is reported through notify.
// It returns the updated history including assistant turns and tool results.
func (a *Agent) Turn(messages []llm.Message, notify func(Event)) ([]llm.Message, error) {
	for {
		response, err := a.Client.CompleteStream(messages, a.Tools.Definitions(), func(text string) {
			notify(Event{Type: EventTextDelta, Text: text})
		})
		if err != nil {
			notify(Event{Type: EventError, Err: err})
			return messages, err
		}

		messages = append(messages, llm.Message{Role: "assistant", Content: response.Content})
		if response.StopReason != "tool_use" {
			notify(Event{Type: EventTurnComplete})
			return messages, nil
		}

		var results []llm.ContentBlock
		for _, block := range response.Content {
			if block.Type != "tool_use" {
				continue
			}
			notify(Event{
				Type:      EventToolCallStarted,
				ToolName:  block.Name,
				ToolInput: string(block.Input),
			})

			output, err := a.Tools.Run(block.Name, block.Input)
			isError := err != nil
			if isError {
				output = err.Error()
			}
			notify(Event{
				Type:     EventToolResult,
				ToolName: block.Name,
				Result:   output,
				IsError:  isError,
			})
			results = append(results, llm.ContentBlock{
				Type:      "tool_result",
				ToolUseID: block.ID,
				Content:   output,
				IsError:   isError,
			})
		}
		messages = append(messages, llm.Message{Role: "user", Content: results})
	}
}
