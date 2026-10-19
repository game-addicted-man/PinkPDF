package winps

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

const createNoWindow = 0x08000000

// Hide makes a child process start without a console window.
func Hide(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

func exe() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	p := filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return "powershell.exe"
}

func command(ctx context.Context, script string, env map[string]string) (*exec.Cmd, error) {
	if err := Check(script); err != nil {
		return nil, err
	}
	var cmd *exec.Cmd
	if ctx != nil {
		cmd = exec.CommandContext(ctx, exe(), Args(script)...)
	} else {
		cmd = exec.Command(exe(), Args(script)...)
	}
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, "PINKPDF_"+k+"="+v)
	}
	Hide(cmd)
	return cmd, nil
}

// Run executes script and waits. A script fails when it exits with a
// non-zero code; the error then carries the messages it reported with Fail.
// When ctx ends first, Run returns ctx.Err().
func Run(ctx context.Context, script string, env map[string]string) error {
	cmd, err := command(ctx, script, env)
	if err != nil {
		return err
	}
	out, runErr := cmd.CombinedOutput()
	if runErr == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if msg := Message(out); msg != "" {
		return errors.New(msg)
	}
	return runErr
}

// Start launches script in the background without waiting.
func Start(script string, env map[string]string) error {
	cmd, err := command(nil, script, env)
	if err != nil {
		return err
	}
	cmd.Dir = os.TempDir()
	return cmd.Start()
}
