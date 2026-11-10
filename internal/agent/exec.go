package agent

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Runner executes commands for a task, streaming output lines to the task
// log. In dry-run mode it only logs what it would execute.
type Runner struct {
	DryRun bool
	Log    func(string)
}

func (r *Runner) logf(format string, args ...any) {
	if r.Log != nil {
		r.Log(fmt.Sprintf(format, args...))
	}
}

// Run executes a command and streams its combined output to the task log.
func (r *Runner) Run(name string, args ...string) error {
	return r.RunEnv(nil, name, args...)
}

// RunEnv is Run with extra environment variables.
func (r *Runner) RunEnv(env []string, name string, args ...string) error {
	r.logf("$ %s %s", name, strings.Join(args, " "))
	if r.DryRun {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if env != nil {
		cmd.Env = append(cmd.Environ(), env...)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		r.logf("%s", sc.Text())
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

// RunShell runs a command line through bash (for pipelines/redirects).
func (r *Runner) RunShell(script string) error {
	return r.Run("bash", "-c", script)
}

// Capture runs a command quietly and returns its trimmed stdout.
func (r *Runner) Capture(name string, args ...string) (string, error) {
	if r.DryRun {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// CaptureCombined is Capture but returns stdout+stderr (for error reports).
func (r *Runner) CaptureCombined(name string, args ...string) (string, error) {
	if r.DryRun {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// Step logs a template step header.
func (r *Runner) Step(format string, args ...any) {
	if r.Log != nil {
		r.Log("==> " + fmt.Sprintf(format, args...))
	}
}

// Warn logs a non-fatal problem; the step is skipped but the task continues.
func (r *Runner) Warn(format string, args ...any) {
	if r.Log != nil {
		r.Log("!! " + fmt.Sprintf(format, args...) + "（已跳过，不中断）")
	}
}
