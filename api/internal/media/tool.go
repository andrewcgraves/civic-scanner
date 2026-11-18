package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	"github.com/andrewcgraves/civic-scanner/api/internal/fetch"
	"github.com/andrewcgraves/civic-scanner/api/internal/limit"
)

const (
	maxStderrBytes     = 4096
	maxURLOutputBytes  = 64 << 10
	waitDelay          = 5 * time.Second
	youtubeTimeout     = 2 * time.Minute
	readTimeoutMicros  = "30000000"
	protocolWhitelist  = "http,https,tls,tcp,hls,crypto"
	formatWhitelist    = "hls,mpegts,mov,mp4,m4a,3gp,3g2,mj2,mp3,aac,matroska,webm,ogg,wav,flv,webvtt"
	defaultTimeout     = time.Hour
	defaultMaxDuration = 12 * time.Hour
	defaultMaxMemory   = 1 << 30
	defaultMaxBytes    = 256 << 20
)

type Tool struct {
	Limit       string
	FFmpeg      string
	YTDLP       string
	Timeout     time.Duration
	MaxDuration time.Duration
	MaxMemory   int64
	MaxBytes    int64
	UserAgent   string
	Env         []string
}

func NewTool(cfg config.Audio, userAgent string) Tool {
	return Tool{
		Limit:       ResolveLimitBinary(cfg.LimitBinary),
		FFmpeg:      cfg.FFmpegBinary,
		YTDLP:       cfg.YTDLPBinary,
		Timeout:     cfg.Timeout,
		MaxDuration: cfg.MaxDuration,
		MaxMemory:   cfg.MaxMemory,
		MaxBytes:    cfg.MaxBytes,
		UserAgent:   userAgent,
	}
}

func NewDownloader(cfg config.Audio, fetcher fetch.Fetcher, userAgent string) Downloader {
	tool := NewTool(cfg, userAgent)
	return Downloader{
		Resolver: Resolver{Fetcher: fetcher, YouTube: tool.YouTubeAudioURL, Hosts: DefaultHosts},
		Tool:     tool,
	}
}

func ResolveLimitBinary(configured string) string {
	if configured != "" {
		return configured
	}
	self, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(self), limit.BinaryName)
}

func (t Tool) withDefaults() Tool {
	if t.FFmpeg == "" {
		t.FFmpeg = "ffmpeg"
	}
	if t.YTDLP == "" {
		t.YTDLP = "yt-dlp"
	}
	if t.Timeout <= 0 {
		t.Timeout = defaultTimeout
	}
	if t.MaxDuration <= 0 {
		t.MaxDuration = defaultMaxDuration
	}
	if t.MaxMemory <= 0 {
		t.MaxMemory = defaultMaxMemory
	}
	if t.MaxBytes <= 0 {
		t.MaxBytes = defaultMaxBytes
	}
	return t
}

func (t Tool) ExtractAudio(ctx context.Context, srcURL, dst string) error {
	t = t.withDefaults()
	args := []string{
		t.FFmpeg,
		"-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-protocol_whitelist", protocolWhitelist,
		"-format_whitelist", formatWhitelist,
		"-rw_timeout", readTimeoutMicros,
	}
	if t.UserAgent != "" {
		args = append(args, "-user_agent", t.UserAgent)
	}
	args = append(args,
		"-i", srcURL,
		"-map", "0:a:0", "-vn", "-sn", "-dn",
		"-t", strconv.FormatFloat(t.MaxDuration.Seconds(), 'f', 0, 64),
		"-ac", "1", "-ar", "16000", "-c:a", "libmp3lame", "-b:a", "32k",
		"-f", "mp3", dst)
	_, err := t.run(ctx, t.Timeout, 0, args)
	if err != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("extract audio: %w", err)
	}
	return nil
}

func (t Tool) YouTubeAudioURL(ctx context.Context, pageURL string) (string, error) {
	t = t.withDefaults()
	out, err := t.run(ctx, min(t.Timeout, youtubeTimeout), maxURLOutputBytes, []string{
		t.YTDLP,
		"--ignore-config", "--no-playlist", "--no-cache-dir", "--no-warnings", "--no-progress",
		"-f", "bestaudio", "--get-url", "--", pageURL,
	})
	if err != nil {
		return "", fmt.Errorf("resolve youtube audio: %w", err)
	}
	line, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	if line == "" {
		return "", fmt.Errorf("%w: yt-dlp printed no url", ErrNoMedia)
	}
	return strings.TrimSpace(line), nil
}

func (t Tool) run(ctx context.Context, timeout time.Duration, maxStdout int, command []string) (string, error) {
	if t.Limit == "" {
		return "", fmt.Errorf("%w: no limit binary configured, set AUDIO_LIMIT_BINARY", ErrUnavailable)
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stdout := &capped{max: maxStdout, onOverflow: cancel}
	stderr := &capped{max: maxStderrBytes, truncate: true}
	args := append(limit.Limits{MaxMemory: t.MaxMemory, MaxFileBytes: t.MaxBytes}.Args(), command...)
	cmd := exec.CommandContext(runCtx, t.Limit, args...) //nolint:gosec
	cmd.Env = append(append(os.Environ(), "MALLOC_ARENA_MAX=2"), t.Env...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = waitDelay
	err := cmd.Run()

	detail := strings.TrimSpace(stderr.String())
	switch {
	case ctx.Err() != nil:
		return "", ctx.Err()
	case stdout.overflowed():
		return "", fmt.Errorf("%w: %s printed more than %d bytes", ErrLimitExceeded, filepath.Base(command[0]), maxStdout)
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		return "", fmt.Errorf("%w: %s took longer than %s", ErrLimitExceeded, filepath.Base(command[0]), timeout)
	case err == nil:
		return stdout.String(), nil
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return "", fmt.Errorf("%w: run %s: %w", ErrUnavailable, t.Limit, err)
	}
	if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		switch status.Signal() {
		case syscall.SIGXFSZ:
			return "", fmt.Errorf("%w: output passed %d bytes", ErrLimitExceeded, t.MaxBytes)
		case syscall.SIGKILL:
			return "", fmt.Errorf("%w: %s killed: %s", ErrLimitExceeded, filepath.Base(command[0]), detail)
		}
		return "", fmt.Errorf("%w: %s crashed with %s: %s", ErrMalformed, filepath.Base(command[0]), status.Signal(), detail)
	}
	switch exitErr.ExitCode() {
	case limit.ExitUsage, limit.ExitLimits, limit.ExitExec:
		return "", fmt.Errorf("%w: %s", ErrUnavailable, detail)
	}
	return "", fmt.Errorf("%w: %s", classify(detail), detail)
}

func classify(detail string) error {
	text := strings.ToLower(detail)
	has := func(markers ...string) bool {
		for _, m := range markers {
			if strings.Contains(text, m) {
				return true
			}
		}
		return false
	}
	switch {
	case has("cannot allocate memory", "out of memory", "file too large", "memoryerror"):
		return ErrLimitExceeded
	case has("not a bot", "confirm you're not a robot"):
		return ErrBlocked
	case has("server returned 5", "server returned 429", "server returned 408", "http error 5", "http error 429",
		"connection timed out", "timed out", "connection refused", "connection reset", "network is unreachable",
		"temporary failure in name resolution", "i/o error", "broken pipe"):
		return ErrNetwork
	case has("server returned 4", "http error 4"):
		return ErrRefused
	case has("matches no streams", "does not contain any stream", "output file is empty", "requested format is not available",
		"video unavailable", "private video", "no video formats", "this live event"):
		return ErrNoAudio
	}
	return ErrMalformed
}

type capped struct {
	mu         sync.Mutex
	buf        bytes.Buffer
	max        int
	truncate   bool
	over       bool
	onOverflow func()
}

func (c *capped) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.buf.Len()+len(p) <= c.max {
		return c.buf.Write(p)
	}
	if c.truncate {
		c.buf.Write(p[:c.max-c.buf.Len()])
		return len(p), nil
	}
	c.over = true
	if c.onOverflow != nil {
		c.onOverflow()
	}
	return 0, ErrLimitExceeded
}

func (c *capped) overflowed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.over
}

func (c *capped) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}
