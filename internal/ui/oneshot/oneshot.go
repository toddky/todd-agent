// Package oneshot is a non-interactive frontend: one prompt, one completed turn, exit.
//
// Model text streams to stdout; tool machinery goes to stderr so scripts
// capturing stdout get only the answer.
package oneshot

import (
	"fmt"
	"os"

	"github.com/toddky/todd-agent/internal/agent"
	"github.com/toddky/todd-agent/internal/llm"
)

const (
	reset = "\033[0m"
	gray  = "\033[38;5;245m"
	red   = "\033[38;5;196m"
)

// printEvent mirrors the REPL's rendering but keeps stdout clean for scripting.
func printEvent(event agent.Event) {
	switch event.Type {
	case agent.EventTextDelta:
		fmt.Print(event.Text)
	case agent.EventToolCallStarted:
		fmt.Fprintf(os.Stderr, "%s🔧 %s %s%s\n", gray, event.ToolName, event.ToolInput, reset)
	case agent.EventToolResult:
		if event.IsError {
			fmt.Fprintf(os.Stderr, "%s✗ %s%s\n", red, event.Result, reset)
		}
	case agent.EventError:
		fmt.Fprintf(os.Stderr, "%s%v%s\n", red, event.Err, reset)
	}
}

// Run drives a single turn to completion and returns the turn's error.
// A turn may span many model/tool round-trips; it ends when the model stops requesting tools.
func Run(engine *agent.Agent, prompt string) error {
	messages := []llm.Message{llm.TextMessage("user", prompt)}
	_, err := engine.Turn(messages, printEvent)
	if err != nil {
		return err
	}
	fmt.Println()
	return nil
}
