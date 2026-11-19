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
	chunkCheckpointMaxBytes = 4 << 20
	fingerprintBytes        = 8
)

func ChunkCheckpointPrefix(recordingID uuid.UUID, audio storage.Info, model string) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s\x00%d\x00%d\x00%s", audio.Key, audio.Size, audio.LastModified.UnixNano(), model))
	return chunkCheckpointRoot + recordingID.String() + "/chunks/" + hex.EncodeToString(sum[:fingerprintBytes]) + "/"
}

type checkpointSegment struct {
	StartNS int64  `json:"start_ns"`
	EndNS   int64  `json:"end_ns"`
	Text    string `json:"text"`
	Speaker string `json:"speaker,omitempty"`
}

type checkpointChunk struct {
	Text       string              `json:"text"`
	DurationNS int64               `json:"duration_ns"`
	Segments   []checkpointSegment `json:"segments"`
}

type chunkCheckpoint struct {
	storage storage.Store
	logger  *slog.Logger
	prefix  string

	mu     sync.Mutex
	keys   map[string]bool
	reuses int
}

func newChunkCheckpoint(st storage.Store, logger *slog.Logger, prefix string) *chunkCheckpoint {
	return &chunkCheckpoint{storage: st, logger: logger, prefix: prefix, keys: map[string]bool{}}
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
	t, err := decodeCheckpoint(raw)
	if err != nil {
		c.logger.WarnContext(ctx, "transcript chunk checkpoint invalid; transcribing the chunk", "checkpoint", key, "error", err)
		return transcribe.Transcript{}, false
	}
	c.remember(key, true)
	return t, true
}

func (c *chunkCheckpoint) Save(ctx context.Context, ch transcribe.Chunk, t transcribe.Transcript) {
	key := c.key(ch)
	raw, err := encodeCheckpoint(t)
	if err == nil {
		err = c.storage.Put(ctx, key, bytes.NewReader(raw))
	}
	if err != nil {
		c.logger.WarnContext(ctx, "transcript chunk checkpoint not saved; a retry transcribes the chunk again", "checkpoint", key, "error", err)
		return
	}
	c.remember(key, false)
}

func (c *chunkCheckpoint) clear(ctx context.Context) {
	c.mu.Lock()
	keys := make([]string, 0, len(c.keys))
	for key := range c.keys {
		keys = append(keys, key)
	}
	c.mu.Unlock()
	slices.Sort(keys)
	for _, key := range keys {
		if err := c.storage.Delete(ctx, key); err != nil {
			c.logger.WarnContext(ctx, "transcript chunk checkpoint not deleted", "checkpoint", key, "error", err)
		}
	}
}

func encodeCheckpoint(t transcribe.Transcript) ([]byte, error) {
	out := checkpointChunk{Text: t.Text, DurationNS: t.Duration.Nanoseconds(), Segments: make([]checkpointSegment, len(t.Segments))}
	for i, s := range t.Segments {
		out.Segments[i] = checkpointSegment{StartNS: s.Start.Nanoseconds(), EndNS: s.End.Nanoseconds(), Text: s.Text, Speaker: s.Speaker}
	}
	return json.Marshal(out)
}

func decodeCheckpoint(raw []byte) (transcribe.Transcript, error) {
	var in checkpointChunk
	if err := json.Unmarshal(raw, &in); err != nil {
		return transcribe.Transcript{}, err
	}
	t := transcribe.Transcript{Text: in.Text, Duration: time.Duration(in.DurationNS)}
	for _, s := range in.Segments {
		if s.StartNS < 0 || s.EndNS < s.StartNS {
			return transcribe.Transcript{}, fmt.Errorf("segment %d to %d ns is out of order", s.StartNS, s.EndNS)
		}
		t.Segments = append(t.Segments, transcribe.Segment{
			Start:   time.Duration(s.StartNS),
			End:     time.Duration(s.EndNS),
			Text:    s.Text,
			Speaker: s.Speaker,
		})
	}
	return t, nil
}
