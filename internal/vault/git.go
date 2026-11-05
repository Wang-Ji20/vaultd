package vault

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"time"
)

const gitTimeout = 30 * time.Second

// gitMissing makes the "git is not installed" message appear only once.
var gitMissing sync.Once

// autocommit records a finished write in git. The write has already succeeded,
// so any problem here is logged and never fails the tool call.
func (v *Vault) autocommit(abs, clean string, who WriteInfo) {
	if !v.gitAutocommit {
		return
	}
	if _, err := exec.LookPath("git"); err != nil {
		gitMissing.Do(func() { v.logger().Warn("git is not installed; writes will not be committed") })
		return
	}
	if !v.runGit("rev-parse", "--is-inside-work-tree") {
		return // not a repository: a quiet no-op, as the config promises
	}
	file := v.relReal(abs)
	if !v.runGitLogged("add", "--", file) {
		return
	}
	// Naming the file limits the commit to it. Anything else the user has staged
	// stays out of vaultd's commit.
	message := "vaultd: " + who.Tool + " " + clean + " (" + who.App + ")"
	v.runGitLogged("commit", "-m", message, "--", file)
}

func (v *Vault) runGit(args ...string) bool {
	cmd, cancel := v.gitCommand(args...)
	defer cancel()
	return cmd.Run() == nil
}

func (v *Vault) runGitLogged(args ...string) bool {
	cmd, cancel := v.gitCommand(args...)
	defer cancel()
	output, err := cmd.CombinedOutput()
	if err != nil {
		v.logger().Warn("git command failed", "command", args[0], "error", err, "output", string(output))
		return false
	}
	return true
}

// gitCommand runs without any way to stop and ask. A signing key's passphrase
// or a password prompt would otherwise hang the tool call. Literal pathspecs
// mean a note named "*.md" cannot match other files. Arguments go directly to
// git with no shell, so no note name can inject a command.
func (v *Vault) gitCommand(args ...string) (*exec.Cmd, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	full := append([]string{"--literal-pathspecs", "-C", v.root, "-c", "commit.gpgsign=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return cmd, cancel
}

func (v *Vault) logger() *slog.Logger {
	if v.log != nil {
		return v.log
	}
	return slog.Default()
}
