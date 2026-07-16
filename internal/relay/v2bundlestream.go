package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/pyrycode/pyrycode/internal/protocol"
)

// bundleChunkBytes bounds the raw bundle bytes per debug_bundle_chunk so the
// sealed noise_msg ciphertext stays under maxNoisePayloadBytes: base64 expands
// the payload 4/3, the JSON Envelope wrapper adds ~200 B, and the AEAD tag adds
// 16 B. 48000 → 64000 B base64 → ~64200 B envelope JSON → ~64216 B ciphertext,
// ~1.3 KB under 65535. The invariant is ENFORCED by
// TestStreamBundle_EveryFrameWithinCap; if that ever fails, LOWER this constant
// — never raise it. The conservative constant is the belt; the deterministic
// per-frame cap test is the suspenders (different fabric).
const bundleChunkBytes = 48000

// bundleEnvelopes splits blob into ceil(len/bundleChunkBytes) debug_bundle_chunk
// envelopes with ascending 0-based Seq, then appends one debug_bundle_done
// envelope carrying the exact chunk count as Total. An empty blob yields zero
// chunks plus a done{total:0} (a valid stream that reassembles to empty).
//
// EventID is left nil on every envelope, so forwardEnvelope's reconnect-replay
// dedup guard is inert for bundle frames. ID is non-load-bearing (set to
// Seq/Total for debuggability only; the receiver keys on Type+Seq+Total, never
// ID). Content bytes never touch the logs — the raw slice lives only in the
// sealed payload.
func bundleEnvelopes(blob []byte) ([]protocol.Envelope, error) {
	n := (len(blob) + bundleChunkBytes - 1) / bundleChunkBytes // ceil division
	now := time.Now().UTC()
	envs := make([]protocol.Envelope, 0, n+1)
	for seq := 0; seq < n; seq++ {
		start := seq * bundleChunkBytes
		end := start + bundleChunkBytes
		if end > len(blob) {
			end = len(blob)
		}
		payload, err := json.Marshal(protocol.DebugBundleChunkPayload{
			Seq:  seq,
			Data: blob[start:end],
		})
		if err != nil {
			// Defensive: a closed struct of an int and a byte slice does not
			// fail to marshal in practice. No content bytes in the error.
			return nil, fmt.Errorf("marshal bundle chunk %d: %w", seq, err)
		}
		envs = append(envs, protocol.Envelope{
			ID:      uint64(seq),
			Type:    protocol.TypeDebugBundleChunk,
			TS:      now,
			Payload: payload,
		})
	}
	donePayload, err := json.Marshal(protocol.DebugBundleDonePayload{Total: n})
	if err != nil {
		return nil, fmt.Errorf("marshal bundle done: %w", err)
	}
	envs = append(envs, protocol.Envelope{
		ID:      uint64(n),
		Type:    protocol.TypeDebugBundleDone,
		TS:      now,
		Payload: donePayload,
	})
	return envs, nil
}

// StreamBundle moves an arbitrarily-large byte blob to the addressed, open,
// authenticated v2 conn as ordered, cap-respecting chunks ending in a
// completion marker. It builds the envelopes with bundleEnvelopes, then enqueues
// each in order via Push — the manager's own asynchronous send path (Push →
// drainOnce → forwardEnvelope), the same path every unsolicited daemon → client
// frame uses. It deliberately does NOT use the per-frame handler-reply channel
// (handlerOutboundBuf = 8, a small fixed buffer sized for the 1-few-reply
// request/response case), so a bundle of any size cannot overrun it.
//
// Safe to call from any goroutine, including a future handler on the Run
// goroutine: Push never blocks and never touches s.send, so this sidesteps both
// the frame-cap wall and the handler-buffer wall. Bundle frames are
// control-class (not TypeAssistantDelta), so the pushQueue drop policy never
// evicts them — all chunks are delivered, in order.
//
// Returns on successful ENQUEUE, not delivery (delivery is async on Run; a
// per-frame seal/forward failure is logged at debug by drainOnce, matching the
// package's fire-and-forget push posture). Returns the first Push error and
// stops on it: Push returns ErrConnNotFound for a conn that is not open, so
// there is no point continuing. Logs one content-free debug line on success —
// counts only, never the streamed bytes (AC#4).
func (m *V2SessionManager) StreamBundle(ctx context.Context, connID string, blob []byte) error {
	envs, err := bundleEnvelopes(blob)
	if err != nil {
		return err
	}
	for _, env := range envs {
		if err := m.Push(ctx, connID, env); err != nil {
			return err
		}
	}
	m.cfg.Logger.Debug("relay: v2 bundle stream enqueued",
		"event", "v2.bundle.stream",
		"conn_id", connID,
		"chunks", len(envs)-1, // envs = N chunks + 1 done marker
		"bytes", len(blob))
	return nil
}

// ReassembleBundle is the receiver-contract reference and test oracle: it walks
// frames in arrival order and reconstructs the original blob, or fails cleanly.
// It is pure and exported — run in production only phone-side (out of repo); the
// daemon has no inbound caller. It is what proves AC#2 and the "fail cleanly"
// correctness requirement (you cannot test clean-failure without a reassembler).
//
// A debug_bundle_chunk must carry Seq == the count of chunks already seen
// (0-based contiguous ascending), else a reorder/gap/duplicate is rejected. A
// debug_bundle_done must carry Total == the count of chunks seen, else a
// truncated/count-mismatch stream is rejected. Any other frame type is skipped
// (tolerates interleaved assistant_delta etc.). Frames exhausted with no done
// marker is an incomplete stream. On ANY failure it returns (nil, err) — never
// partial or corrupted bytes.
func ReassembleBundle(frames []protocol.Envelope) ([]byte, error) {
	var out []byte
	next := 0
	for _, f := range frames {
		switch f.Type {
		case protocol.TypeDebugBundleChunk:
			var p protocol.DebugBundleChunkPayload
			if err := json.Unmarshal(f.Payload, &p); err != nil {
				return nil, fmt.Errorf("reassemble bundle: decode chunk: %w", err)
			}
			if p.Seq != next {
				return nil, fmt.Errorf("reassemble bundle: chunk seq %d out of order, want %d", p.Seq, next)
			}
			out = append(out, p.Data...)
			next++
		case protocol.TypeDebugBundleDone:
			var p protocol.DebugBundleDonePayload
			if err := json.Unmarshal(f.Payload, &p); err != nil {
				return nil, fmt.Errorf("reassemble bundle: decode done: %w", err)
			}
			if p.Total != next {
				return nil, fmt.Errorf("reassemble bundle: done total %d, received %d chunks", p.Total, next)
			}
			return out, nil
		default:
			// Non-bundle frame interleaved by the transport (e.g. assistant_delta);
			// the real phone filters by type, so skip it.
		}
	}
	return nil, fmt.Errorf("reassemble bundle: incomplete, missing completion marker after %d chunks", next)
}
