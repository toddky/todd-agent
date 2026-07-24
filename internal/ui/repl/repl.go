// Package repl is a plain line-based chat frontend.
//
// Responses are printed exactly as the model produced them.
// Never add artificial line wrapping: it makes copy/paste a pain and ruins terminal resizing.
package repl

import (
	"bufio"
	"errors"
	"fmt"
	"os"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/toddky/todd-agent/internal/agent"
	"github.com/toddky/todd-agent/internal/llm"
)

const (
	reset = "\033[0m"
	green = "\033[38;5;46m"
	gray  = "\033[38;5;245m"
	red   = "\033[38;5;196m"
)

type inputModel struct {
	input   textinput.Model
	done    bool
	aborted bool
}

func newInputModel() inputModel {
	input := textinput.New()
	input.Prompt = "👤 "
	input.Focus()
	return inputModel{input: input}
}

func (m inputModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m inputModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.Type {
		case tea.KeyEnter:
			m.done = true
			return m, tea.Quit
		case tea.KeyCtrlC, tea.KeyCtrlD:
			m.aborted = true
			return m, tea.Quit
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m inputModel) View() string {
	if m.done || m.aborted {
		// Clear the input line on exit; Run reprints the final prompt in green.
		return ""
	}
	return m.input.View()
}

// readPrompt runs a one-shot textinput program and returns the entered line.
func readPrompt() (string, error) {
	final, err := tea.NewProgram(newInputModel()).Run()
	if err != nil {
		return "", fmt.Errorf("read input: %w", err)
	}

	model := final.(inputModel)
	if model.aborted {
		return "", errQuit
	}
	return model.input.Value(), nil
}

// readPromptPlain reads one line from the scanner, for piped (non-TTY) stdin.
func readPromptPlain(scanner *bufio.Scanner) (string, error) {
	fmt.Print("👤 ")
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", fmt.Errorf("read input: %w", err)
		}
		fmt.Println()
		return "", errQuit
	}
	return scanner.Text(), nil
}

var errQuit = errors.New("quit")

// printEvent prints one engine event.
// Tool activity is gray so it reads as machinery, not model output.
func printEvent(event agent.Event) {
	switch event.Type {
	case agent.EventTextDelta:
		fmt.Print(event.Text)
	case agent.EventToolCallStarted:
		fmt.Printf("\n%s🔧 %s %s%s\n", gray, event.ToolName, event.ToolInput, reset)
	case agent.EventToolResult:
		if event.IsError {
			fmt.Printf("%s✗ %s%s\n", red, event.Result, reset)
		}
	case agent.EventError:
		fmt.Fprintf(os.Stderr, "%s%v%s\n", red, event.Err, reset)
	}
}

// Run reads prompts from stdin and runs agent turns until Ctrl-C/Ctrl-D.
func Run(engine *agent.Agent) error {
	var messages []llm.Message

	stat, _ := os.Stdin.Stat()
	interactive := stat.Mode()&os.ModeCharDevice != 0
	var scanner *bufio.Scanner
	if !interactive {
		scanner = bufio.NewScanner(os.Stdin)
	}

	for {
		var prompt string
		var err error
		if interactive {
			prompt, err = readPrompt()
		} else {
			prompt, err = readPromptPlain(scanner)
		}
		if err == errQuit {
			return nil
		}
		if err != nil {
			return err
		}
		if prompt == "" {
			continue
		}

		// Echo the submitted prompt in green, like ,agent2.
		fmt.Printf("👤%s %s%s\n", green, prompt, reset)

		history := append(messages, llm.TextMessage("user", prompt))
		updated, err := engine.Turn(history, printEvent)
		if err != nil {
			// Drop the failed turn so a transient API error doesn't poison the history.
			continue
		}

		fmt.Println()
		messages = updated
	}
}
