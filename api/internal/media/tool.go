package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	"github.com/andrewcgraves/civic-scanner/api/internal/limit"
)

const (
	maxDetailBytes      = 4096
	maxImportantLines   = 20
	maxURLOutputBytes   = 64 << 10
	maxProbeOutputBytes = 1024
	waitDelay           = 5 * time.Second
	youtubeTimeout      = 2 * time.Minute
	probeTimeout        = 2 * time.Minute
	readTimeoutHeadroom = 30 * time.Second
	fileProtocols       = "http,tcp"
	pipeProtocols       = "pipe"
	formatWhitelist     = "mpegts,mov,mp4,m4a,3gp,3g2,mj2,mp3,aac,matroska,webm,ogg,wav,flv"
	segmentAttempts     = 3
	segmentRetryDelay   = time.Second
	defaultTimeout      = time.Hour
	defaultMaxDuration  = 12 * time.Hour
	defaultMaxMemory    = 1 << 30
	defaultMaxBytes     = 256 << 20
	fileRequestBudget   = 2000
	exactSlack          = 2 * time.Second
	exactSlackRatio     = 0.001
	containerSlack      = 3 * time.Second
	containerSlackRatio = 0.005
)

var errReaderClosed = errors.New("media: ffmpeg stopped reading")

var statusPattern = regexp.MustCompile(`(?i)(?:http error|server returned) ([1-5][0-9][0-9]|[45]xx)`)

type Tool struct {
	Limit       string
	FFmpeg      string
	FFprobe     string
	YTDLP       string
	Timeout     time.Duration
	IdleTimeout time.Duration
	MaxDuration time.Duration
	MaxMemory   int64
	MaxBytes    int64
	UserAgent   string
	Hosts       []string
	Guard       Guard
	Env         []string
}

type expectation struct {
	duration time.Duration
	exact    bool
}

func NewTool(cfg config.Audio, userAgent string) Tool {
	return Tool{
		Limit:       ResolveLimitBinary(cfg.LimitBinary),
		FFmpeg:      cfg.FFmpegBinary,
		FFprobe:     cfg.FFprobeBinary,
		YTDLP:       cfg.YTDLPBinary,
		Timeout:     cfg.Timeout,
		MaxDuration: cfg.MaxDuration,
		MaxMemory:   cfg.MaxMemory,
		MaxBytes:    cfg.MaxBytes,
		UserAgent:   userAgent,
		Hosts:       DefaultHosts.Media,
	}
}

func NewDownloader(cfg config.Audio, userAgent string) Downloader {
	tool := NewTool(cfg, userAgent)
	return Downloader{
		Resolver: Resolver{
			Fetcher: PageFetcher{Hosts: DefaultHosts.Players, UserAgent: userAgent},
			YouTube: tool.YouTubeAudioURL,
			Hosts:   DefaultHosts,
		},
		Tool: tool,
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
	if t.FFprobe == "" {
		t.FFprobe = "ffprobe"
	}
	if t.YTDLP == "" {
		t.YTDLP = "yt-dlp"
	}
	if t.Timeout <= 0 {
		t.Timeout = defaultTimeout
	}
	if t.IdleTimeout <= 0 {
		t.IdleTimeout = DefaultIdleTimeout
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

func (t Tool) fileArgs(input string) []string {
	return []string{
		"-protocol_whitelist", fileProtocols,
		"-format_whitelist", formatWhitelist,
		"-rw_timeout", strconv.FormatInt((t.IdleTimeout + readTimeoutHeadroom).Microseconds(), 10),
		"-i", input,
	}
}

func pipeArgs() []string {
	return []string{"-protocol_whitelist", pipeProtocols, "-format_whitelist", formatWhitelist, "-i", "pipe:0"}
}

func (t Tool) ffmpegArgs(input []string, dst string) []string {
	args := append([]string{t.FFmpeg, "-nostdin", "-hide_banner", "-loglevel", "warning", "-y"}, input...)
	return append(args,
		"-map", "0:a:0", "-vn", "-sn", "-dn",
		"-t", strconv.FormatFloat(t.MaxDuration.Seconds(), 'f', 0, 64),
		"-ac", "1", "-ar", "16000", "-c:a", "libmp3lame", "-b:a", "32k",
		"-f", "mp3", dst)
}

func noProxyEnv() []string {
	return []string{"http_proxy=", "HTTP_PROXY=", "https_proxy=", "HTTPS_PROXY=", "all_proxy=", "ALL_PROXY=", "no_proxy=*", "NO_PROXY=*"}
}

func (t Tool) gateway(budget int64) (*Gateway, error) {
	return StartGateway(GatewayConfig{
		Hosts:       t.Hosts,
		Guard:       t.Guard,
		UserAgent:   t.UserAgent,
		MaxRequests: budget,
		IdleTimeout: t.IdleTimeout,
	})
}

func (t Tool) ExtractAudio(ctx context.Context, srcURL, dst string) error {
	t = t.withDefaults()
	err := t.extract(ctx, srcURL, dst)
	if err != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("extract audio: %w", err)
	}
	return nil
}

func (t Tool) extract(ctx context.Context, srcURL, dst string) error {
	if t.Limit == "" {
		return fmt.Errorf("%w: no limit binary configured, set AUDIO_LIMIT_BINARY", ErrUnavailable)
	}
	if isHLS(srcURL) {
		return t.extractHLS(ctx, srcURL, dst)
	}
	return t.extractFile(ctx, srcURL, dst)
}

func (t Tool) extractHLS(ctx context.Context, srcURL, dst string) error {
	pages := PageFetcher{Hosts: t.Hosts, Guard: t.Guard, UserAgent: t.UserAgent}
	playlist, err := InspectPlaylist(ctx, pages, t.Hosts, t.Guard, srcURL)
	if err != nil {
		return err
	}
	expected := expectation{duration: playlist.Duration}
	if expected.duration > t.MaxDuration {
		return fmt.Errorf("%w: recording lasts %s, longer than %s", ErrLimitExceeded, expected.duration.Round(time.Second), t.MaxDuration)
	}
	gw, err := t.gateway(int64(segmentAttempts*len(playlist.Parts) + 10))
	if err != nil {
		return err
	}
	defer gw.Close()

	feedCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	reader, writer := io.Pipe()
	fed := make(chan error, 1)
	go func() {
		err := feed(feedCtx, gw, playlist.Parts, writer)
		_ = writer.CloseWithError(err)
		fed <- err
	}()
	_, log, runErr := t.run(ctx, t.Timeout, 0, t.ffmpegArgs(pipeArgs(), dst), noProxyEnv(), reader)
	_ = reader.CloseWithError(errReaderClosed)
	cancel()
	if feedErr := <-fed; feedErr != nil && !errors.Is(feedErr, errReaderClosed) && !errors.Is(feedErr, context.Canceled) {
		return feedErr
	}
	if gerr := gw.Err(); gerr != nil {
		return gerr
	}
	if runErr != nil {
		return runErr
	}
	return t.verify(ctx, log, dst, expected)
}

func feed(ctx context.Context, gw *Gateway, parts []string, w io.Writer) error {
	for _, part := range parts {
		var err error
		for attempt := 1; attempt <= segmentAttempts; attempt++ {
			counted := &countingWriter{w: w}
			err = gw.Copy(ctx, part, counted)
			if err == nil || counted.n > 0 || !errors.Is(err, ErrNetwork) || attempt == segmentAttempts {
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * segmentRetryDelay):
			}
		}
		if err != nil {
			return fmt.Errorf("segment %s: %w", part, err)
		}
	}
	return nil
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

func (t Tool) extractFile(ctx context.Context, srcURL, dst string) error {
	u, err := url.Parse(srcURL)
	if err != nil {
		return fmt.Errorf("%w: %q", ErrInvalidURL, srcURL)
	}
	if err := t.Guard.checkURL(u, t.Hosts); err != nil {
		return err
	}
	gw, err := t.gateway(fileRequestBudget)
	if err != nil {
		return err
	}
	defer gw.Close()
	input := gw.Register(srcURL)
	expected, err := t.probe(ctx, gw, input)
	if err != nil {
		return err
	}
	if expected.duration > t.MaxDuration {
		return fmt.Errorf("%w: recording lasts %s, longer than %s", ErrLimitExceeded, expected.duration.Round(time.Second), t.MaxDuration)
	}
	_, log, err := t.run(ctx, t.Timeout, 0, t.ffmpegArgs(t.fileArgs(input), dst), noProxyEnv(), nil)
	if gerr := gw.Err(); gerr != nil {
		return gerr
	}
	if err != nil {
		return err
	}
	return t.verify(ctx, log, dst, expected)
}

func (t Tool) verify(ctx context.Context, log *stderrLog, dst string, expected expectation) error {
	if err := log.segmentError(); err != nil {
		return err
	}
	got, err := t.localDuration(ctx, dst)
	if err != nil {
		return err
	}
	return t.checkComplete(expected, got)
}

func (t Tool) probe(ctx context.Context, gw *Gateway, input string) (expectation, error) {
	args := append([]string{t.FFprobe, "-v", "error"}, t.fileArgs(input)[:6]...)
	args = append(args, "-select_streams", "a:0", "-show_entries", "stream=duration:format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", "--", input)
	out, _, err := t.run(ctx, min(t.Timeout, probeTimeout), maxProbeOutputBytes, args, noProxyEnv(), nil)
	if gerr := gw.Err(); gerr != nil {
		return expectation{}, gerr
	}
	if err != nil {
		return expectation{}, fmt.Errorf("probe media: %w", err)
	}
	lines := strings.Fields(out)
	switch len(lines) {
	case 0:
		return expectation{}, nil
	case 1:
		return expectation{duration: parseSeconds(lines[0])}, nil
	}
	if d := parseSeconds(lines[0]); d > 0 {
		return expectation{duration: d, exact: true}, nil
	}
	return expectation{duration: parseSeconds(lines[len(lines)-1])}, nil
}

func (t Tool) checkComplete(expected expectation, got time.Duration) error {
	if expected.duration <= 0 {
		if got >= t.MaxDuration-time.Second {
			return fmt.Errorf("%w: audio of unknown length reached the %s cap", ErrLimitExceeded, t.MaxDuration)
		}
		return nil
	}
	slack := containerSlack + time.Duration(float64(expected.duration)*containerSlackRatio)
	if expected.exact {
		slack = exactSlack + time.Duration(float64(expected.duration)*exactSlackRatio)
	}
	if got+slack < expected.duration {
		return fmt.Errorf("%w: %s of audio from a %s recording", ErrIncomplete, got.Round(time.Second), expected.duration.Round(time.Second))
	}
	return nil
}

func (t Tool) localDuration(ctx context.Context, path string) (time.Duration, error) {
	out, _, err := t.run(ctx, probeTimeout, maxProbeOutputBytes, []string{
		t.FFprobe, "-v", "error", "-protocol_whitelist", "file",
		"-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", "--", path,
	}, nil, nil)
	if err != nil {
		return 0, fmt.Errorf("probe audio: %w", err)
	}
	d := parseSeconds(out)
	if d <= 0 {
		return 0, fmt.Errorf("%w: output has no duration", ErrNoAudio)
	}
	return d, nil
}

func parseSeconds(out string) time.Duration {
	secs, err := strconv.ParseFloat(strings.TrimSpace(out), 64)
	if err != nil || secs <= 0 || math.IsInf(secs, 0) || math.IsNaN(secs) {
		return 0
	}
	return time.Duration(secs * float64(time.Second))
}

func (t Tool) YouTubeAudioURL(ctx context.Context, pageURL string) (string, error) {
	t = t.withDefaults()
	if t.Limit == "" {
		return "", fmt.Errorf("%w: no limit binary configured, set AUDIO_LIMIT_BINARY", ErrUnavailable)
	}
	out, _, err := t.run(ctx, min(t.Timeout, youtubeTimeout), maxURLOutputBytes, []string{
		t.YTDLP,
		"--ignore-config", "--no-playlist", "--no-cache-dir", "--no-warnings", "--no-progress",
		"--use-extractors", "youtube",
		"-f", "bestaudio", "--get-url", "--", pageURL,
	}, nil, nil)
	if err != nil {
		return "", fmt.Errorf("resolve youtube audio: %w", err)
	}
	line, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	if line == "" {
		return "", fmt.Errorf("%w: yt-dlp printed no url", ErrNoMedia)
	}
	return strings.TrimSpace(line), nil
}

func (t Tool) run(ctx context.Context, timeout time.Duration, maxStdout int, command, env []string, stdin io.Reader) (string, *stderrLog, error) {
	if t.Limit == "" {
		return "", nil, fmt.Errorf("%w: no limit binary configured, set AUDIO_LIMIT_BINARY", ErrUnavailable)
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stdout := &capped{max: maxStdout, onOverflow: cancel}
	stderr := &stderrLog{}
	args := append(limit.Limits{MaxMemory: t.MaxMemory, MaxFileBytes: t.MaxBytes}.Args(), command...)
	cmd := exec.CommandContext(runCtx, t.Limit, args...) //nolint:gosec
	cmd.Env = append(append(append(os.Environ(), "MALLOC_ARENA_MAX=2"), env...), t.Env...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = waitDelay
	err := cmd.Run()

	name := filepath.Base(command[0])
	detail := stderr.detail()
	switch {
	case ctx.Err() != nil:
		return "", stderr, ctx.Err()
	case stdout.overflowed():
		return "", stderr, fmt.Errorf("%w: %s printed more than %d bytes", ErrLimitExceeded, name, maxStdout)
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		return "", stderr, fmt.Errorf("%w: %s took longer than %s", ErrLimitExceeded, name, timeout)
	case err == nil:
		return stdout.String(), stderr, nil
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return "", stderr, fmt.Errorf("%w: run %s: %w", ErrUnavailable, t.Limit, err)
	}
	if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		switch status.Signal() {
		case syscall.SIGXFSZ:
			return "", stderr, fmt.Errorf("%w: output passed %d bytes", ErrLimitExceeded, t.MaxBytes)
		case syscall.SIGKILL:
			return "", stderr, fmt.Errorf("%w: %s killed: %s", ErrLimitExceeded, name, detail)
		}
		return "", stderr, fmt.Errorf("%w: %s crashed with %s: %s", ErrMalformed, name, status.Signal(), detail)
	}
	switch exitErr.ExitCode() {
	case limit.ExitUsage, limit.ExitLimits, limit.ExitExec:
		return "", stderr, fmt.Errorf("%w: %s", ErrUnavailable, detail)
	}
	return "", stderr, fmt.Errorf("%w: %s", classify(detail), detail)
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
	case has("server returned 5", "server returned 429", "server returned 408", "http error 5", "http error 429", "http error 408",
		"connection timed out", "timed out", "connection refused", "connection reset", "network is unreachable",
		"temporary failure in name resolution", "i/o error", "broken pipe"):
		return ErrNetwork
	case has("server returned 4", "http error 4"):
		return ErrRefused
	case has("matches no streams", "does not contain any stream", "output file is empty", "requested format is not available",
		"video unavailable", "private video", "no video formats", "this live event"):
		return ErrNoAudio
	case has("invalid data found when processing input", "moov atom not found", "could not find codec parameters"):
		return ErrMalformed
	}
	return ErrUnrecognized
}

type stderrLog struct {
	mu            sync.Mutex
	partial       []byte
	head          bytes.Buffer
	important     []string
	segmentFailed bool
	statuses      []string
	timedOut      bool
}

func (s *stderrLog) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.partial = append(s.partial, p...)
	for {
		i := bytes.IndexByte(s.partial, '\n')
		if i < 0 {
			break
		}
		s.line(string(s.partial[:i]))
		s.partial = s.partial[i+1:]
	}
	if len(s.partial) > maxDetailBytes {
		s.line(string(s.partial))
		s.partial = s.partial[:0]
	}
	return len(p), nil
}

func (s *stderrLog) line(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	if s.head.Len()+len(line)+1 <= maxDetailBytes {
		s.head.WriteString(line + "\n")
	}
	lower := strings.ToLower(line)
	marker := false
	if strings.Contains(lower, "failed to open segment") || strings.Contains(lower, "failed too many times, skipping") {
		s.segmentFailed, marker = true, true
	}
	if m := statusPattern.FindStringSubmatch(line); m != nil {
		s.statuses, marker = append(s.statuses, strings.ToLower(m[1])), true
	}
	if strings.Contains(lower, "timed out") || strings.Contains(lower, "connection reset") {
		s.timedOut, marker = true, true
	}
	if marker && len(s.important) < maxImportantLines {
		s.important = append(s.important, line)
	}
}

func (s *stderrLog) detail() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.partial) > 0 {
		s.line(string(s.partial))
		s.partial = nil
	}
	text := strings.TrimSpace(s.head.String())
	for _, l := range s.important {
		if !strings.Contains(text, l) {
			text += "\n" + l
		}
	}
	return text
}

func (s *stderrLog) segmentError() error {
	detail := s.detail()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.segmentFailed {
		return nil
	}
	transient, refused := s.timedOut, false
	for _, code := range s.statuses {
		switch {
		case code == "408" || code == "429" || strings.HasPrefix(code, "5"):
			transient = true
		case strings.HasPrefix(code, "4"):
			refused = true
		}
	}
	switch {
	case transient:
		return fmt.Errorf("%w: a segment failed: %s", ErrNetwork, detail)
	case refused:
		return fmt.Errorf("%w: a segment was refused: %s", ErrRefused, detail)
	}
	return fmt.Errorf("%w: a segment was skipped: %s", ErrIncomplete, detail)
}

type capped struct {
	mu         sync.Mutex
	buf        bytes.Buffer
	max        int
	over       bool
	onOverflow func()
}

func (c *capped) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.buf.Len()+len(p) <= c.max {
		return c.buf.Write(p)
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
