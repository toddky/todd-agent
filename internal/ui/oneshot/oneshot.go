// Package oneshot is a non-interactive frontend: one prompt, one completed turn, exit.
//
// Model text streams to stdout; tool machinery goes to stderr so scripts
// capturing stdout get only the answer.
package oneshot

import (
	"errors"
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

// Run drives a single turn to completion and returns the exit code for main.
// A turn may span many model/tool round-trips; it ends when the model stops requesting tools.
// If the model calls the exit tool, the reason prints to stdout and its code becomes the exit code.
func Run(engine *agent.Agent, prompt string) (int, error) {
	messages := []llm.Message{llm.TextMessage("user", prompt)}
	_, err := engine.Turn(messages, printEvent)

	var exitRequest *agent.ExitRequest
	if errors.As(err, &exitRequest) {
		fmt.Println(exitRequest.Reason)
		return exitRequest.Code, nil
	}
	if err != nil {
		// 3 = runtime error, distinct from any model-chosen exit code (see main.go).
		return 3, err
	}
	fmt.Println()
	return 0, nil
}
