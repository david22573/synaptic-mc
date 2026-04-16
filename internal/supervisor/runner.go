package supervisor

import (
	"context"
	"log/slog"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"time"
)

// NodeRunner treats the TS process as a disposable worker with exponential backoff.
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

// Start begins the process loop with exponential backoff.
func (r *NodeRunner) Start(ctx context.Context) {
	crashCount := 0
	lastStartTime := time.Now()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Reset crash counter if stable for 5 minutes
		if crashCount > 0 && time.Since(lastStartTime) > 5*time.Minute {
			r.logger.Info("Process stable for 5m, resetting crash counter", slog.Int("previous_crashes", crashCount))
			crashCount = 0
		}

		r.Ping() // Reset on startup
		lastStartTime = time.Now()

		// Resolve absolute path
		absScriptPath, err := filepath.Abs(r.scriptPath)
		if err != nil {
			r.logger.Error("Failed to resolve absolute path", slog.String("path", r.scriptPath), slog.Any("error", err))
			time.Sleep(5 * time.Second)
			continue
		}

		workDir := filepath.Dir(absScriptPath)
		r.logger.Info("Spawning Node.js bot process", slog.String("work_dir", workDir), slog.Int("attempt", crashCount+1))
		
		cmd := exec.CommandContext(ctx, "node", absScriptPath)
		cmd.Dir = workDir
		cmd.Stdout = r.loggerWriter("STDOUT")
		cmd.Stderr = r.loggerWriter("STDERR")

		if err := cmd.Start(); err != nil {
			r.logger.Error("Failed to start TS process", slog.Any("error", err))
			r.waitBeforeRestart(&crashCount)
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
						r.logger.Warn("TS process starved (no state tick for 45s). SIGKILL.")
						_ = cmd.Process.Kill()
						return
					}
				}
			}
		}()

		err = cmd.Wait()
		cancelWatchdog()

		if ctx.Err() != nil {
			return
		}

		r.logger.Warn("TS process died or was killed", slog.Any("error", err))
		r.waitBeforeRestart(&crashCount)
	}
}

func (r *NodeRunner) waitBeforeRestart(crashCount *int) {
	*crashCount++
	
	var delay time.Duration
	switch *crashCount {
	case 1:
		delay = 1 * time.Second
	case 2:
		delay = 3 * time.Second
	case 3:
		delay = 10 * time.Second
	default:
		delay = 30 * time.Second // Cap at 30s
	}

	r.logger.Info("Waiting before restart", slog.Duration("delay", delay), slog.Int("crash_count", *crashCount))
	time.Sleep(delay)
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
		w.logger.Info("TS Engine Output", slog.String("msg", string(p)))
	}
	return len(p), nil
}
