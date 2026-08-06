package logger

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

// Package logger emits one JSON object per line (level, time, msg, and an
// optional request_id for correlation — see WithRequestID) instead of
// plain text, so log output is directly ingestible by a log
// aggregator/query tool (project backlog item #10) without a parsing
// step. Package-level function signatures are unchanged from the old
// plain-text logger, so existing call sites across the codebase keep
// working as-is; only call sites that want request-ID correlation need to
// switch to the *Ctx variants (see internal/middleware/request_id.go for
// how the ID gets into context).

type contextKey string

const requestIDKey contextKey = "logger_request_id"

// WithRequestID returns a context carrying id for log correlation via the
// *Ctx logging functions below.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestIDFromContext returns the request ID stored via WithRequestID, if any.
func RequestIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(requestIDKey).(string)
	return id, ok && id != ""
}

var (
	stdout io.Writer = os.Stdout
	stderr io.Writer = os.Stderr

	enabled bool
)

// Init enables logging. level is currently unused (reserved for future
// level filtering) — kept so Init's call sites (main.go) don't need to change.
func Init(level string) {
	_ = level
	enabled = true
}

// SetOutput overrides the destination writers. For tests only — production
// code should rely on Init + the default os.Stdout/os.Stderr.
func SetOutput(outW, errW io.Writer) {
	stdout = outW
	stderr = errW
}

type entry struct {
	Time      string `json:"time"`
	Level     string `json:"level"`
	Msg       string `json:"msg"`
	RequestID string `json:"request_id,omitempty"`
}

func write(w io.Writer, level, requestID, msg string) {
	if !enabled {
		return
	}
	e := entry{
		Time:      time.Now().UTC().Format(time.RFC3339Nano),
		Level:     level,
		Msg:       msg,
		RequestID: requestID,
	}
	b, err := json.Marshal(e)
	if err != nil {
		// Never let a marshal failure swallow the log entry.
		fmt.Fprintf(w, "%s [%s] (log json marshal failed: %v) %s\n", e.Time, level, err, msg)
		return
	}
	_, _ = w.Write(append(b, '\n'))
}

func requestIDFrom(ctx context.Context) string {
	id, _ := RequestIDFromContext(ctx)
	return id
}

// ---- plain (no request-ID correlation) ----

func Info(v ...interface{})                  { write(stdout, "info", "", fmt.Sprint(v...)) }
func Infof(format string, v ...interface{})  { write(stdout, "info", "", fmt.Sprintf(format, v...)) }
func Warn(v ...interface{})                  { write(stdout, "warn", "", fmt.Sprint(v...)) }
func Warnf(format string, v ...interface{})  { write(stdout, "warn", "", fmt.Sprintf(format, v...)) }
func Error(v ...interface{})                 { write(stderr, "error", "", fmt.Sprint(v...)) }
func Errorf(format string, v ...interface{}) { write(stderr, "error", "", fmt.Sprintf(format, v...)) }
func Debug(v ...interface{})                 { write(stdout, "debug", "", fmt.Sprint(v...)) }
func Debugf(format string, v ...interface{}) { write(stdout, "debug", "", fmt.Sprintf(format, v...)) }

// ---- context-aware (includes request_id when present) ----

func InfoCtx(ctx context.Context, v ...interface{}) {
	write(stdout, "info", requestIDFrom(ctx), fmt.Sprint(v...))
}
func InfofCtx(ctx context.Context, format string, v ...interface{}) {
	write(stdout, "info", requestIDFrom(ctx), fmt.Sprintf(format, v...))
}
func WarnCtx(ctx context.Context, v ...interface{}) {
	write(stdout, "warn", requestIDFrom(ctx), fmt.Sprint(v...))
}
func WarnfCtx(ctx context.Context, format string, v ...interface{}) {
	write(stdout, "warn", requestIDFrom(ctx), fmt.Sprintf(format, v...))
}
func ErrorCtx(ctx context.Context, v ...interface{}) {
	write(stderr, "error", requestIDFrom(ctx), fmt.Sprint(v...))
}
func ErrorfCtx(ctx context.Context, format string, v ...interface{}) {
	write(stderr, "error", requestIDFrom(ctx), fmt.Sprintf(format, v...))
}
func DebugCtx(ctx context.Context, v ...interface{}) {
	write(stdout, "debug", requestIDFrom(ctx), fmt.Sprint(v...))
}
func DebugfCtx(ctx context.Context, format string, v ...interface{}) {
	write(stdout, "debug", requestIDFrom(ctx), fmt.Sprintf(format, v...))
}
