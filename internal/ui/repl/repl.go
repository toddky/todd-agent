// Package repl is a plain line-based chat frontend.
package repl

import (
	"bufio"
	"fmt"
	"io"
	"os"

	"github.com/toddky/todd-agent/internal/llm"
)

const (
	reset = "\033[0m"
	green = "\033[38;5;46m"
)

// Run reads prompts from stdin and prints model responses until EOF (Ctrl-D).
func Run(client *llm.Client) error {
	scanner := bufio.NewScanner(os.Stdin)
	var messages []llm.Message

	for {
		fmt.Print("👤 ")
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil && err != io.EOF {
				return fmt.Errorf("read input: %w", err)
			}
			fmt.Println()
			return nil
		}

		prompt := scanner.Text()
		if prompt == "" {
			continue
		}

		// Rewrite the just-entered line so the prompt text turns green, like ,agent2.
		fmt.Printf("\033[1A\033[2K👤%s %s%s\n", green, prompt, reset)

		messages = append(messages, llm.Message{Role: "user", Content: prompt})

		text, err := client.CompleteStream(messages, func(fragment string) {
			fmt.Print(fragment)
		})
		if err != nil {
			// Drop the failed turn so a transient API error doesn't poison the history.
			messages = messages[:len(messages)-1]
			fmt.Fprintln(os.Stderr, err)
			continue
		}

		fmt.Println()
		messages = append(messages, llm.Message{Role: "assistant", Content: text})
	}
}
