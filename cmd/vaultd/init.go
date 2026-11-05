package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/hortensia/vaultd/internal/config"
)

const (
	defaultWritable = "AI, Inbox"
	defaultAppName  = "claude-desktop"
)

// initCommand asks a few questions and writes a working config. It never
// replaces an existing one.
func initCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags, where := newFlags("init", stderr)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := where.resolve(); err != nil {
		return err
	}
	answers := newPrompter(stdin, stderr)
	cfg, app, err := askSetup(answers)
	if err != nil {
		return err
	}
	token, hash := config.NewToken()
	cfg.Apps = []config.App{{Name: app, TokenSHA256: hash}}
	if err := config.WriteNew(where.config, cfg); err != nil {
		return err
	}
	fmt.Fprintln(stderr, "\nConfig saved in", where.config)
	fmt.Fprintf(stderr, "Token for %s (save it now, it cannot be shown again):\n", app)
	fmt.Fprintln(stdout, token)
	fmt.Fprintln(stderr, "\nNext:\n  vaultd hash-password --write    set the admin password\n  vaultd run                       start the gateway")
	return nil
}

func askSetup(answers *prompter) (*config.Config, string, error) {
	cfg := config.Default()
	vaultPath := answers.ask("Vault folder", "")
	if vaultPath == "" {
		return nil, "", errors.New("a vault folder is required")
	}
	abs, err := filepath.Abs(vaultPath)
	if err != nil {
		return nil, "", err
	}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		return nil, "", fmt.Errorf("%s is not a folder", abs)
	}
	cfg.Vault.Path = abs
	cfg.Vault.WritableFolders = splitList(answers.ask("Folders AI apps may write to", defaultWritable))
	if len(cfg.Vault.WritableFolders) > 0 {
		cfg.Vault.MemoryFile = cfg.Vault.WritableFolders[0] + "/memory.md"
	}
	cfg.Vault.GitAutocommit = true
	return cfg, answers.ask("Name of the first app", defaultAppName), answers.err
}

func splitList(text string) []string {
	var items []string
	for _, item := range strings.Split(text, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}

type prompter struct {
	input  *bufio.Reader
	output io.Writer
	err    error
}

func newPrompter(stdin io.Reader, output io.Writer) *prompter {
	return &prompter{input: bufio.NewReader(stdin), output: output}
}

// ask shows a question with its default. Pressing Enter accepts the default.
func (p *prompter) ask(question, fallback string) string {
	if fallback != "" {
		fmt.Fprintf(p.output, "%s [%s]: ", question, fallback)
	} else {
		fmt.Fprintf(p.output, "%s: ", question)
	}
	line, err := p.input.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		p.err = err
	}
	if answer := strings.TrimSpace(line); answer != "" {
		return answer
	}
	return fallback
}
