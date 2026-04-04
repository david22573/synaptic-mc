package supervisor

import (
	"context"
	"log/slog"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"time"
)

// NodeRunner treats the TS process as a disposable worker.
type NodeRunner struct {
	scriptPath string
	logger     *slog.Logger
	lastPing   atomic.Int64
}

func NewNodeRunner(scriptPath string, logger *slog.Logger) *NodeRunner {
	return &NodeRunner{
		scriptPath: scriptPath,
		logger:     logger.With(slog.String("component", "ts_supervisor")),
	}
}

// Ping resets the watchdog timer.
func (r *NodeRunner) Ping() {
	r.lastPing.Store(time.Now().UnixNano())
}

// Start begins the process loop.
func (r *NodeRunner) Start(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		r.Ping() // Reset on startup

		// Resolve absolute path to make chdir bulletproof
		absScriptPath, err := filepath.Abs(r.scriptPath)
		if err != nil {
			r.logger.Error("Failed to resolve absolute path for TS script", slog.String("path", r.scriptPath), slog.Any("error", err))
			time.Sleep(5 * time.Second)
			continue
		}

		workDir := filepath.Dir(absScriptPath)

		r.logger.Info("Spawning Node.js bot process", slog.String("work_dir", workDir))
		cmd := exec.CommandContext(ctx, "node", absScriptPath)
		cmd.Dir = workDir
		cmd.Stdout = r.loggerWriter("STDOUT")
		cmd.Stderr = r.loggerWriter("STDERR")

		if err := cmd.Start(); err != nil {
			r.logger.Error("Failed to start TS process", slog.Any("error", err))
			time.Sleep(5 * time.Second)
			continue
		}

		// Watchdog goroutine
		watchdogCtx, cancelWatchdog := context.WithCancel(ctx)
		go func() {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()

			for {
				select {
				case <-watchdogCtx.Done():
					return
				case <-ticker.C:
					last := time.Unix(0, r.lastPing.Load())
					if time.Since(last) > 45*time.Second {
						r.logger.Warn("TS process starved (no state tick for 45s). Sending SIGKILL.")
						_ = cmd.Process.Kill()
						return
					}
				}
			}
		}()

		err = cmd.Wait()
		cancelWatchdog()

		if ctx.Err() != nil {
			return // Shutting down cleanly
		}

		r.logger.Warn("TS process died or was killed. Restarting in 3 seconds...", slog.Any("error", err))
		time.Sleep(3 * time.Second)
	}
}

func (r *NodeRunner) loggerWriter(stream string) *streamWriter {
	return &streamWriter{
		logger: r.logger,
		stream: stream,
	}
}

type streamWriter struct {
	logger *slog.Logger
	stream string
}

func (w *streamWriter) Write(p []byte) (n int, err error) {
	if w.stream == "STDERR" {
		w.logger.Warn("TS Engine Error", slog.String("msg", string(p)))
	} else {
		// FIX: Use Info level here so standard TS logs aren't swallowed by Go's default log filter
		w.logger.Info("TS Engine Output", slog.String("msg", string(p)))
	}
	return len(p), nil
}
