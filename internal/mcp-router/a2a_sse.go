package mcprouter

// a2a streaming observation: a read-only observer of an A2A SSE response
// (SendStreamingMessage / SubscribeToTask). It reads each server-sent event
// envelope-only to surface the task/context ids and status the agent emits, for
// logging and tracing. It never mutates the stream — the body is forwarded to the
// client byte-for-byte — and it makes no ownership decision; binding an observed id
// to a principal is a separate concern. see docs/design/a2a/a2a-design.md.

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"

	extprochttp "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/ext_proc/v3"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// a2aObserveMode makes envoy deliver the response body to ext_proc in streaming
// mode so the observer can read it. Only the response body is streamed; nothing is
// mutated, so no content-length handling is required.
func a2aObserveMode() *extprochttp.ProcessingMode {
	return &extprochttp.ProcessingMode{
		RequestHeaderMode:   extprochttp.ProcessingMode_SEND,
		ResponseHeaderMode:  extprochttp.ProcessingMode_SEND,
		ResponseBodyMode:    extprochttp.ProcessingMode_STREAMED,
		RequestTrailerMode:  extprochttp.ProcessingMode_SKIP,
		ResponseTrailerMode: extprochttp.ProcessingMode_SKIP,
	}
}

// a2aStreamEnvelope is the envelope-only view of a StreamResponse event: only the
// ids and state are decoded. The oneof is task (initial Task) / statusUpdate /
// artifactUpdate; artifacts, parts and history are never decoded.
type a2aStreamEnvelope struct {
	Result struct {
		Task *struct {
			ID        string `json:"id"`
			ContextID string `json:"contextId"`
			Status    struct {
				State string `json:"state"`
			} `json:"status"`
		} `json:"task"`
		StatusUpdate *struct {
			TaskID    string `json:"taskId"`
			ContextID string `json:"contextId"`
			Status    struct {
				State string `json:"state"`
			} `json:"status"`
		} `json:"statusUpdate"`
		ArtifactUpdate *struct {
			TaskID    string `json:"taskId"`
			ContextID string `json:"contextId"`
		} `json:"artifactUpdate"`
	} `json:"result"`
}

// a2aSSEObserver reads an A2A streaming response line by line (SSE is line-based)
// and records the task lifecycle it observes. taskID/contextID hold the first ids
// seen, exposed for later ownership binding; they are never used to mutate the stream.
type a2aSSEObserver struct {
	buf       []byte
	logger    *slog.Logger
	agent     string
	requestID string

	taskID    string
	contextID string
	lastState string
}

// Process consumes a chunk of the SSE response and observes any complete event
// lines. It does not return anything: the caller forwards the body unchanged.
func (o *a2aSSEObserver) Process(ctx context.Context, chunk []byte) {
	o.buf = append(o.buf, chunk...)
	for {
		idx := bytes.IndexByte(o.buf, '\n')
		if idx == -1 {
			break // no complete line yet — hold the remainder for the next chunk
		}
		line := o.buf[:idx+1]
		o.buf = o.buf[idx+1:]
		if bytes.HasPrefix(bytes.TrimSpace(line), dataPrefix) {
			o.observe(ctx, line)
		}
	}
}

// Flush observes any trailing partial line held in the buffer. Safe to call
// multiple times; subsequent calls are no-ops.
func (o *a2aSSEObserver) Flush(ctx context.Context) {
	remaining := o.buf
	o.buf = nil
	if len(remaining) > 0 && bytes.HasPrefix(bytes.TrimSpace(remaining), dataPrefix) {
		o.observe(ctx, remaining)
	}
}

func (o *a2aSSEObserver) observe(ctx context.Context, line []byte) {
	jsonData := bytes.TrimSpace(bytes.TrimPrefix(bytes.TrimSpace(line), dataPrefix))
	var env a2aStreamEnvelope
	if err := json.Unmarshal(jsonData, &env); err != nil {
		return // not a json-rpc event (e.g. an SSE comment) — nothing to observe
	}

	var taskID, contextID, state string
	switch {
	case env.Result.Task != nil:
		taskID, contextID, state = env.Result.Task.ID, env.Result.Task.ContextID, env.Result.Task.Status.State
	case env.Result.StatusUpdate != nil:
		taskID, contextID, state = env.Result.StatusUpdate.TaskID, env.Result.StatusUpdate.ContextID, env.Result.StatusUpdate.Status.State
	case env.Result.ArtifactUpdate != nil:
		taskID, contextID = env.Result.ArtifactUpdate.TaskID, env.Result.ArtifactUpdate.ContextID
	default:
		return
	}
	if taskID == "" && contextID == "" {
		return
	}
	if o.taskID == "" && taskID != "" {
		o.taskID = taskID
	}
	if o.contextID == "" && contextID != "" {
		o.contextID = contextID
	}

	// dedupe repeated states (e.g. successive WORKING events) to keep logs and
	// spans quiet on a chatty stream.
	if state != "" && state == o.lastState {
		return
	}
	o.lastState = state

	o.logger.DebugContext(ctx, "a2a: observed stream event",
		"request id", o.requestID, "agent", o.agent, "task", taskID, "context", contextID, "state", state)
	trace.SpanFromContext(ctx).AddEvent("a2a.stream.event", trace.WithAttributes(
		attribute.String("a2a.task_id", taskID),
		attribute.String("a2a.context_id", contextID),
		attribute.String("a2a.task_state", state),
	))
}
