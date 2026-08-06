package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// resetForTest points stdout/stderr at fresh buffers and enables logging,
// restoring the previous state (including `enabled`) after the test so
// package-level state doesn't leak between tests in this file.
func resetForTest(t *testing.T) (out, errOut *bytes.Buffer) {
	t.Helper()
	prevOut, prevErr, prevEnabled := stdout, stderr, enabled
	out, errOut = &bytes.Buffer{}, &bytes.Buffer{}
	SetOutput(out, errOut)
	enabled = true
	t.Cleanup(func() {
		stdout, stderr, enabled = prevOut, prevErr, prevEnabled
	})
	return out, errOut
}

func decodeLine(t *testing.T, buf *bytes.Buffer) map[string]interface{} {
	t.Helper()
	line := strings.TrimSpace(buf.String())
	if line == "" {
		t.Fatalf("expected a log line, got empty output")
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("log output is not valid JSON: %v\nraw: %s", err, line)
	}
	return m
}

func TestLogger_EmitsStructuredJSON(t *testing.T) {
	out, _ := resetForTest(t)

	Infof("payment %s created for merchant %s", "PAY-1", "acme")

	m := decodeLine(t, out)
	if m["level"] != "info" {
		t.Errorf("expected level=info, got %v", m["level"])
	}
	if m["msg"] != "payment PAY-1 created for merchant acme" {
		t.Errorf("unexpected msg: %v", m["msg"])
	}
	if _, ok := m["time"]; !ok {
		t.Errorf("expected a time field, got %+v", m)
	}
	if _, hasID := m["request_id"]; hasID {
		t.Errorf("plain Infof must not include request_id, got %+v", m)
	}
}

func TestLogger_ErrorGoesToStderr(t *testing.T) {
	out, errOut := resetForTest(t)

	Errorf("failed to reconcile payment %s: %v", "PAY-1", "timeout")

	if out.Len() != 0 {
		t.Errorf("expected nothing on stdout, got %q", out.String())
	}
	m := decodeLine(t, errOut)
	if m["level"] != "error" {
		t.Errorf("expected level=error, got %v", m["level"])
	}
}

func TestLogger_DisabledBeforeInit_NoOutput(t *testing.T) {
	out, errOut := resetForTest(t)
	enabled = false // simulate pre-Init state

	Info("should not appear")
	Errorf("should not appear either: %v", "x")

	if out.Len() != 0 || errOut.Len() != 0 {
		t.Errorf("expected no output before Init, got stdout=%q stderr=%q", out.String(), errOut.String())
	}
}

func TestLogger_CtxVariants_IncludeRequestID(t *testing.T) {
	out, _ := resetForTest(t)

	ctx := WithRequestID(context.Background(), "req-abc-123")
	InfofCtx(ctx, "handled request")

	m := decodeLine(t, out)
	if m["request_id"] != "req-abc-123" {
		t.Errorf("expected request_id=req-abc-123, got %v", m["request_id"])
	}
}

func TestLogger_CtxVariants_NoRequestIDInContext_OmitsField(t *testing.T) {
	out, _ := resetForTest(t)

	InfofCtx(context.Background(), "handled request")

	m := decodeLine(t, out)
	if _, hasID := m["request_id"]; hasID {
		t.Errorf("expected request_id to be omitted when not set in context, got %+v", m)
	}
}

func TestRequestIDFromContext_RoundTrip(t *testing.T) {
	ctx := WithRequestID(context.Background(), "req-xyz")
	id, ok := RequestIDFromContext(ctx)
	if !ok || id != "req-xyz" {
		t.Fatalf("expected (req-xyz, true), got (%q, %v)", id, ok)
	}

	_, ok = RequestIDFromContext(context.Background())
	if ok {
		t.Fatalf("expected ok=false for a context with no request ID")
	}
}
