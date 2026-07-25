package mcprouter

import (
	"context"
	"log/slog"
	"testing"

	extprochttp "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/ext_proc/v3"
)

func newObserver() *a2aSSEObserver {
	return &a2aSSEObserver{logger: slog.New(slog.DiscardHandler), agent: "mcp-test/weather", requestID: "req-1"}
}

// the three StreamResponse oneof shapes the observer must read.
const (
	evtTask     = `data: {"jsonrpc":"2.0","id":1,"result":{"task":{"id":"a2a-task-1","contextId":"a2a-ctx-1","status":{"state":"TASK_STATE_SUBMITTED"}}}}` + "\n"
	evtWorking  = `data: {"jsonrpc":"2.0","id":1,"result":{"statusUpdate":{"taskId":"a2a-task-1","contextId":"a2a-ctx-1","status":{"state":"TASK_STATE_WORKING"}}}}` + "\n"
	evtArtifact = `data: {"jsonrpc":"2.0","id":1,"result":{"artifactUpdate":{"taskId":"a2a-task-1","contextId":"a2a-ctx-1"}}}` + "\n"
	evtDone     = `data: {"jsonrpc":"2.0","id":1,"result":{"statusUpdate":{"taskId":"a2a-task-1","contextId":"a2a-ctx-1","status":{"state":"TASK_STATE_COMPLETED"}}}}` + "\n"
)

func TestA2ASSEObserver_ExtractsIDsAcrossEventShapes(t *testing.T) {
	ctx := context.Background()
	cases := []struct{ name, event string }{
		{"task", evtTask},
		{"statusUpdate", evtWorking},
		{"artifactUpdate", evtArtifact},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := newObserver()
			o.Process(ctx, []byte(c.event))
			if o.taskID != "a2a-task-1" {
				t.Errorf("taskID = %q, want a2a-task-1", o.taskID)
			}
			if o.contextID != "a2a-ctx-1" {
				t.Errorf("contextID = %q, want a2a-ctx-1", o.contextID)
			}
		})
	}
}

func TestA2ASSEObserver_FirstIDsWin(t *testing.T) {
	ctx := context.Background()
	o := newObserver()
	o.Process(ctx, []byte(evtTask))
	// a later event with different ids must not overwrite the first-seen ones
	o.Process(ctx, []byte(`data: {"jsonrpc":"2.0","id":1,"result":{"statusUpdate":{"taskId":"other","contextId":"other-ctx","status":{"state":"TASK_STATE_WORKING"}}}}`+"\n"))
	if o.taskID != "a2a-task-1" || o.contextID != "a2a-ctx-1" {
		t.Errorf("first ids should win, got task=%q context=%q", o.taskID, o.contextID)
	}
}

func TestA2ASSEObserver_LineBufferingAcrossChunks(t *testing.T) {
	ctx := context.Background()
	o := newObserver()
	// deliver a single event split mid-JSON across two chunks
	full := evtTask
	split := len(full) / 2
	o.Process(ctx, []byte(full[:split]))
	if o.taskID != "" {
		t.Fatalf("partial line must not be observed yet, got %q", o.taskID)
	}
	o.Process(ctx, []byte(full[split:]))
	if o.taskID != "a2a-task-1" {
		t.Errorf("taskID after completing the line = %q, want a2a-task-1", o.taskID)
	}
}

func TestA2ASSEObserver_FlushObservesTrailingPartial(t *testing.T) {
	ctx := context.Background()
	o := newObserver()
	// an event with no trailing newline stays buffered until Flush
	noNewline := `data: {"jsonrpc":"2.0","id":1,"result":{"task":{"id":"a2a-task-9","contextId":"c9","status":{"state":"TASK_STATE_COMPLETED"}}}}`
	o.Process(ctx, []byte(noNewline))
	if o.taskID != "" {
		t.Fatalf("buffered line must not be observed before Flush, got %q", o.taskID)
	}
	o.Flush(ctx)
	if o.taskID != "a2a-task-9" {
		t.Errorf("taskID after Flush = %q, want a2a-task-9", o.taskID)
	}
	// Flush is idempotent
	o.Flush(ctx)
}

func TestA2ASSEObserver_TracksTerminalState(t *testing.T) {
	ctx := context.Background()
	o := newObserver()
	o.Process(ctx, []byte(evtTask+evtWorking+evtWorking+evtWorking+evtDone))
	if o.lastState != "TASK_STATE_COMPLETED" {
		t.Errorf("lastState = %q, want TASK_STATE_COMPLETED", o.lastState)
	}
}

func TestA2ASSEObserver_IgnoresNonEventLines(t *testing.T) {
	ctx := context.Background()
	o := newObserver()
	// SSE comments and non-json data lines must be ignored, not panic
	o.Process(ctx, []byte(": keep-alive\n"))
	o.Process(ctx, []byte("data: not json at all\n"))
	o.Process(ctx, []byte("\n"))
	if o.taskID != "" || o.contextID != "" {
		t.Errorf("no ids should be observed from non-event lines, got task=%q context=%q", o.taskID, o.contextID)
	}
}

func TestA2AObserveMode_StreamsResponseBody(t *testing.T) {
	m := a2aObserveMode()
	if m.ResponseBodyMode != extprochttp.ProcessingMode_STREAMED {
		t.Errorf("ResponseBodyMode = %v, want STREAMED", m.ResponseBodyMode)
	}
	// the request body is not streamed and trailers are skipped
	if m.ResponseTrailerMode != extprochttp.ProcessingMode_SKIP {
		t.Errorf("ResponseTrailerMode = %v, want SKIP", m.ResponseTrailerMode)
	}
}
