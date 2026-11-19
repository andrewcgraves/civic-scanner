package jobs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/andrewcgraves/civic-scanner/api/internal/storage"
	"github.com/andrewcgraves/civic-scanner/api/internal/transcribe"
)

const (
	chunkCheckpointRoot     = "transcripts/"
	chunkCheckpointVersion  = "v2"
	chunkCheckpointMaxBytes = 4 << 20
	checkpointMaxItems      = 20000
	checkpointMaxTextBytes  = 1 << 20
	checkpointTimeSlack     = 2 * time.Second
	checkpointSaveTimeout   = 15 * time.Second
	fingerprintBytes        = 8
)

var errInvalidCheckpoint = errors.New("checkpoint does not fit its chunk")

type CheckpointInputs struct {
	Model           string
	ProviderOptions string
}

func TranscriptObjectsPrefix(recordingID uuid.UUID) string {
	return chunkCheckpointRoot + recordingID.String() + "/"
}

func ChunkCheckpointPrefix(recordingID uuid.UUID, audio storage.Info, in CheckpointInputs) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s\x00%s\x00%d\x00%d\x00%s\x00%s",
		chunkCheckpointVersion, audio.Key, audio.Size, audio.LastModified.UnixNano(), in.Model, in.ProviderOptions))
	return TranscriptObjectsPrefix(recordingID) + "chunks/" + hex.EncodeToString(sum[:fingerprintBytes]) + "/"
}

type checkpointItem struct {
	StartNS int64  `json:"start_ns"`
	EndNS   int64  `json:"end_ns"`
	Text    string `json:"text"`
	Speaker string `json:"speaker,omitempty"`
}

type checkpointChunk struct {
	Text       string           `json:"text"`
	DurationNS int64            `json:"duration_ns"`
	Segments   []checkpointItem `json:"segments"`
	Words      []checkpointItem `json:"words,omitempty"`
}

type chunkCheckpoint struct {
	storage storage.Store
	logger  *slog.Logger
	root    string
	prefix  string

	mu     sync.Mutex
	keys   map[string]bool
	reuses int
}

func newChunkCheckpoint(st storage.Store, logger *slog.Logger, root, prefix string) *chunkCheckpoint {
	return &chunkCheckpoint{storage: st, logger: logger, root: root, prefix: prefix, keys: map[string]bool{}}
}

func (c *chunkCheckpoint) key(ch transcribe.Chunk) string {
	return fmt.Sprintf("%s%05d-%d-%d.json", c.prefix, ch.Index, ch.Start.Milliseconds(), ch.Length.Milliseconds())
}

func (c *chunkCheckpoint) remember(key string, reused bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.keys[key] = true
	if reused {
		c.reuses++
	}
}

func (c *chunkCheckpoint) reused() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reuses
}

func (c *chunkCheckpoint) Load(ctx context.Context, ch transcribe.Chunk) (transcribe.Transcript, bool) {
	key := c.key(ch)
	body, err := c.storage.Get(ctx, key)
	if errors.Is(err, storage.ErrNotFound) {
		return transcribe.Transcript{}, false
	}
	if err != nil {
		c.logger.WarnContext(ctx, "transcript chunk checkpoint not read; transcribing the chunk", "checkpoint", key, "error", err)
		return transcribe.Transcript{}, false
	}
	defer func() { _ = body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(body, chunkCheckpointMaxBytes+1))
	if err != nil || len(raw) > chunkCheckpointMaxBytes {
		c.logger.WarnContext(ctx, "transcript chunk checkpoint unreadable; transcribing the chunk", "checkpoint", key, "error", err)
		return transcribe.Transcript{}, false
	}
	t, err := decodeCheckpoint(raw, ch)
	if err != nil {
		c.logger.WarnContext(ctx, "transcript chunk checkpoint invalid; transcribing the chunk", "checkpoint", key, "error", err)
		return transcribe.Transcript{}, false
	}
	c.remember(key, true)
	return t, true
}

func (c *chunkCheckpoint) Save(ctx context.Context, ch transcribe.Chunk, t transcribe.Transcript) {
	key := c.key(ch)
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), checkpointSaveTimeout)
	defer cancel()
	raw, err := encodeCheckpoint(t)
	if err == nil {
		err = c.storage.Put(saveCtx, key, bytes.NewReader(raw))
	}
	if err != nil {
		c.logger.WarnContext(ctx, "transcript chunk checkpoint not saved; a retry transcribes the chunk again", "checkpoint", key, "error", err)
		return
	}
	c.remember(key, false)
}

func (c *chunkCheckpoint) clear(ctx context.Context) {
	keys, err := c.storage.ListPrefix(ctx, c.root)
	if err != nil {
		c.logger.WarnContext(ctx, "transcript chunk checkpoints not listed; deleting the ones this run used", "prefix", c.root, "error", err)
		c.mu.Lock()
		for key := range c.keys {
			keys = append(keys, key)
		}
		c.mu.Unlock()
	}
	slices.Sort(keys)
	for _, key := range keys {
		if err := c.storage.Delete(ctx, key); err != nil {
			c.logger.WarnContext(ctx, "transcript chunk checkpoint not deleted", "checkpoint", key, "error", err)
		}
	}
}

func encodeItems[T any](items []T, item func(T) checkpointItem) []checkpointItem {
	out := make([]checkpointItem, len(items))
	for i, it := range items {
		out[i] = item(it)
	}
	return out
}

func encodeCheckpoint(t transcribe.Transcript) ([]byte, error) {
	return json.Marshal(checkpointChunk{
		Text:       t.Text,
		DurationNS: t.Duration.Nanoseconds(),
		Segments: encodeItems(t.Segments, func(s transcribe.Segment) checkpointItem {
			return checkpointItem{StartNS: s.Start.Nanoseconds(), EndNS: s.End.Nanoseconds(), Text: s.Text, Speaker: s.Speaker}
		}),
		Words: encodeItems(t.Words, func(w transcribe.Word) checkpointItem {
			return checkpointItem{StartNS: w.Start.Nanoseconds(), EndNS: w.End.Nanoseconds(), Text: w.Text, Speaker: w.Speaker}
		}),
	})
}

func decodeCheckpoint(raw []byte, ch transcribe.Chunk) (transcribe.Transcript, error) {
	var in checkpointChunk
	if err := json.Unmarshal(raw, &in); err != nil {
		return transcribe.Transcript{}, err
	}
	limit := ch.Length + checkpointTimeSlack
	size := len(in.Text)
	if len(in.Segments) > checkpointMaxItems || len(in.Words) > checkpointMaxItems {
		return transcribe.Transcript{}, fmt.Errorf("%w: %d segments, %d words", errInvalidCheckpoint, len(in.Segments), len(in.Words))
	}
	if in.DurationNS < 0 || time.Duration(in.DurationNS) > limit {
		return transcribe.Transcript{}, fmt.Errorf("%w: duration %s", errInvalidCheckpoint, time.Duration(in.DurationNS))
	}
	t := transcribe.Transcript{Text: in.Text, Duration: time.Duration(in.DurationNS)}
	for _, s := range in.Segments {
		if err := checkItem(s, limit); err != nil {
			return transcribe.Transcript{}, err
		}
		size += len(s.Text)
		t.Segments = append(t.Segments, transcribe.Segment{Start: time.Duration(s.StartNS), End: time.Duration(s.EndNS), Text: s.Text, Speaker: s.Speaker})
	}
	for _, w := range in.Words {
		if err := checkItem(w, limit); err != nil {
			return transcribe.Transcript{}, err
		}
		size += len(w.Text)
		t.Words = append(t.Words, transcribe.Word{Start: time.Duration(w.StartNS), End: time.Duration(w.EndNS), Text: w.Text, Speaker: w.Speaker})
	}
	if size > 2*checkpointMaxTextBytes {
		return transcribe.Transcript{}, fmt.Errorf("%w: %d bytes of text", errInvalidCheckpoint, size)
	}
	return t, nil
}

func checkItem(it checkpointItem, limit time.Duration) error {
	start, end := time.Duration(it.StartNS), time.Duration(it.EndNS)
	switch {
	case start < 0 || end < start || end > limit:
		return fmt.Errorf("%w: %s to %s outside 0 to %s", errInvalidCheckpoint, start, end, limit)
	case len(it.Text) > checkpointMaxTextBytes || len(it.Speaker) > checkpointMaxTextBytes:
		return fmt.Errorf("%w: item text of %d bytes", errInvalidCheckpoint, len(it.Text))
	}
	return nil
}
