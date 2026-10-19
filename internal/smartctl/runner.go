// Package smartctl wraps invocations of the smartctl binary and device discovery.
package smartctl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// Result is the outcome of one smartctl invocation.
//
// smartctl uses its exit status as a bitmask (see EXIT STATUS in smartctl(8)),
// so a non-zero ExitCode does not mean the JSON is unusable.
type Result struct {
	Args     []string
	Stdout   []byte
	ExitCode int
}

// JSON decodes Stdout into a generic map. Numbers are kept as json.Number so
// values beyond float64 precision (e.g. NVMe data units) are not silently lost
// before the caller decides how to convert them.
func (r Result) JSON() (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(r.Stdout))
	dec.UseNumber()
	var out map[string]any
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("smartctl %s: invalid JSON: %w", strings.Join(r.Args, " "), err)
	}
	return out, nil
}

// Runner executes smartctl. It is an interface so tests can substitute fixtures.
type Runner interface {
	Run(ctx context.Context, args ...string) (Result, error)
}

// ExecRunner runs the real smartctl binary.
type ExecRunner struct {
	Path    string
	Timeout time.Duration
	Logger  *slog.Logger
}

func (e ExecRunner) Run(ctx context.Context, args ...string) (Result, error) {
	if e.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, e.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, e.Path, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	res := Result{Args: args, Stdout: stdout.Bytes()}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr) && ctx.Err() == nil:
		res.ExitCode = exitErr.ExitCode()
		if e.Logger != nil {
			e.Logger.Debug("smartctl returned non-zero exit code",
				"args", args, "exit_code", res.ExitCode, "stderr", strings.TrimSpace(stderr.String()))
		}
	default:
		return res, fmt.Errorf("smartctl %s: %w", strings.Join(args, " "), errors.Join(err, ctx.Err()))
	}
	return res, nil
}
