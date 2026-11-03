// Package media is crosspost.MediaWorker: ffprobe and ffmpeg as sandboxed child processes (security.md §2 item 10):
// no network (-protocol_whitelist file, empty environment, no secrets), explicit input format, CPU time, file size
// and (Linux) memory limits through ulimit, a wall-clock timeout, an input size cap, and temp files in a dedicated
// directory removed after each job.
package media

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	crosspostDomain "bloomary-api/internal/domain/crosspost"
)

// Store is where media lives (implemented by *objectstore.R2ObjectStore).
type Store interface {
	Open(ctx context.Context, key string) (body io.ReadCloser, size int64, contentType string, err error)
	PutStream(ctx context.Context, key, contentType string, body io.Reader, size int64) error
}

// Config: FFmpeg and FFprobe are absolute paths; WorkDir is a directory only the API user can write (0700).
type Config struct {
	Store         Store
	FFmpeg        string
	FFprobe       string
	WorkDir       string
	MaxInputBytes int64         // 0 = 1 GiB
	Timeout       time.Duration // wall clock per child process; 0 = 10 minutes
	CPUSeconds    int           // RLIMIT_CPU; 0 = 600
	MemoryBytes   int64         // RLIMIT_AS (Linux only; macOS refuses it); 0 = 2 GiB
	Threads       int           // ffmpeg -threads; 0 = 2
}

// FFmpeg runs probes and variants.
type FFmpeg struct {
	cfg Config
}

var _ crosspostDomain.MediaWorker = (*FFmpeg)(nil)

// inputFormats: the only containers accepted, each with its explicit demuxer.
var inputFormats = map[string]string{
	"video/mp4":       "mov",
	"video/quicktime": "mov",
	"image/jpeg":      "image2",
	"image/png":       "image2",
	"image/webp":      "image2",
}

func NewFFmpeg(cfg Config) (*FFmpeg, error) {
	if cfg.Store == nil {
		return nil, fmt.Errorf("media worker: store is required")
	}
	for _, path := range []string{cfg.FFmpeg, cfg.FFprobe, cfg.WorkDir} {
		if !filepath.IsAbs(path) {
			return nil, fmt.Errorf("media worker: ffmpeg, ffprobe and work dir must be absolute paths")
		}
	}
	if err := os.MkdirAll(cfg.WorkDir, 0o700); err != nil {
		return nil, fmt.Errorf("media worker: work dir: %w", err)
	}
	if cfg.MaxInputBytes <= 0 {
		cfg.MaxInputBytes = 1 << 30
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Minute
	}
	if cfg.CPUSeconds <= 0 {
		cfg.CPUSeconds = 600
	}
	if cfg.MemoryBytes <= 0 {
		cfg.MemoryBytes = 2 << 30
	}
	if cfg.Threads <= 0 {
		cfg.Threads = 2
	}
	return &FFmpeg{cfg: cfg}, nil
}

// Probe reads seconds, size, codec and dimensions of a stored file.
func (f *FFmpeg) Probe(ctx context.Context, key string) (crosspostDomain.MediaInfo, error) {
	job, err := f.newJob()
	if err != nil {
		return crosspostDomain.MediaInfo{}, err
	}
	defer job.remove()
	input, contentType, err := f.fetch(ctx, key, job)
	if err != nil {
		return crosspostDomain.MediaInfo{}, err
	}
	return f.probeFile(ctx, input, contentType)
}

// Variant makes a network's copy: fit inside Width×Height (aspect kept), padded into [AspectMin, AspectMax] when
// set; images become JPEG, videos H.264/AAC MP4. The result is stored next to the source and hashed.
func (f *FFmpeg) Variant(ctx context.Context, key string, spec crosspostDomain.VariantSpec) (crosspostDomain.MediaRef, error) {
	if spec.Width <= 0 || spec.Height <= 0 || spec.Width > 8192 || spec.Height > 8192 {
		return crosspostDomain.MediaRef{}, fmt.Errorf("media worker: variant size out of range")
	}
	job, err := f.newJob()
	if err != nil {
		return crosspostDomain.MediaRef{}, err
	}
	defer job.remove()
	input, contentType, err := f.fetch(ctx, key, job)
	if err != nil {
		return crosspostDomain.MediaRef{}, err
	}
	video := strings.HasPrefix(contentType, "video/")
	outType, ext := "image/jpeg", "jpg"
	if video {
		outType, ext = "video/mp4", "mp4"
	}
	if spec.ContentType != "" && spec.ContentType != outType {
		return crosspostDomain.MediaRef{}, fmt.Errorf("media worker: can't make %s from %s", spec.ContentType, contentType)
	}
	output := filepath.Join(job.dir, "out."+ext)
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-protocol_whitelist", "file",
		"-threads", strconv.Itoa(f.cfg.Threads), "-f", inputFormats[contentType], "-i", input,
		"-vf", filter(spec), "-map_metadata", "-1"}
	if video {
		args = append(args, "-c:v", "libx264", "-preset", "medium", "-crf", "23", "-pix_fmt", "yuv420p",
			"-c:a", "aac", "-b:a", "128k", "-movflags", "+faststart", "-f", "mp4")
	} else {
		args = append(args, "-frames:v", "1", "-q:v", "2", "-f", "image2", "-c:v", "mjpeg")
	}
	args = append(args, "-y", output)
	if _, err := f.run(ctx, f.cfg.FFmpeg, args...); err != nil {
		return crosspostDomain.MediaRef{}, err
	}
	info, err := f.probeFile(ctx, output, outType)
	if err != nil {
		return crosspostDomain.MediaRef{}, err
	}
	if spec.MaxBytes > 0 && info.Bytes > spec.MaxBytes {
		return crosspostDomain.MediaRef{}, fmt.Errorf("media worker: variant is %d bytes, over %d", info.Bytes, spec.MaxBytes)
	}
	sum, err := fileSHA256(output)
	if err != nil {
		return crosspostDomain.MediaRef{}, err
	}
	variantKey := fmt.Sprintf("%s.v%dx%d.%s", key, spec.Width, spec.Height, ext)
	file, err := os.Open(output)
	if err != nil {
		return crosspostDomain.MediaRef{}, err
	}
	defer func() { _ = file.Close() }()
	if err := f.cfg.Store.PutStream(ctx, variantKey, outType, file, info.Bytes); err != nil {
		return crosspostDomain.MediaRef{}, fmt.Errorf("media worker: store variant: %w", err)
	}
	return crosspostDomain.MediaRef{Key: variantKey, SHA256: sum, Info: info}, nil
}

// filter scales down to fit (never up), pads into the aspect range when asked, and keeps even dimensions (H.264).
func filter(spec crosspostDomain.VariantSpec) string {
	parts := []string{fmt.Sprintf("scale=w='min(%d,iw)':h='min(%d,ih)':force_original_aspect_ratio=decrease", spec.Width, spec.Height)}
	if spec.AspectMin > 0 && spec.AspectMax >= spec.AspectMin {
		lo, hi := strconv.FormatFloat(spec.AspectMin, 'f', 4, 64), strconv.FormatFloat(spec.AspectMax, 'f', 4, 64)
		// Too tall (w/h < lo): widen to h*lo. Too wide (w/h > hi): heighten to w/hi. Centred, black bars.
		parts = append(parts, fmt.Sprintf("pad=w='if(lt(iw/ih,%[1]s),ceil(ih*%[1]s),iw)':h='if(gt(iw/ih,%[2]s),ceil(iw/%[2]s),ih)':x='(ow-iw)/2':y='(oh-ih)/2':color=black", lo, hi))
	}
	parts = append(parts, "scale=w='trunc(iw/2)*2':h='trunc(ih/2)*2'", "setsar=1")
	return strings.Join(parts, ",")
}

type probeAnswer struct {
	Streams []struct {
		CodecType string `json:"codec_type"`
		CodecName string `json:"codec_name"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
		Size     string `json:"size"`
	} `json:"format"`
}

func (f *FFmpeg) probeFile(ctx context.Context, path, contentType string) (crosspostDomain.MediaInfo, error) {
	out, err := f.run(ctx, f.cfg.FFprobe, "-hide_banner", "-loglevel", "error", "-protocol_whitelist", "file",
		"-f", inputFormats[contentType], "-print_format", "json", "-show_format", "-show_streams", path)
	if err != nil {
		return crosspostDomain.MediaInfo{}, err
	}
	var answer probeAnswer
	if err := json.Unmarshal(out, &answer); err != nil {
		return crosspostDomain.MediaInfo{}, fmt.Errorf("media worker: read ffprobe output: %w", err)
	}
	info := crosspostDomain.MediaInfo{Kind: crosspostDomain.MediaImage, ContentType: contentType}
	if strings.HasPrefix(contentType, "video/") {
		info.Kind = crosspostDomain.MediaVideo
	}
	info.Seconds, _ = strconv.ParseFloat(answer.Format.Duration, 64)
	info.Bytes, _ = strconv.ParseInt(answer.Format.Size, 10, 64)
	if stat, err := os.Stat(path); err == nil {
		info.Bytes = stat.Size()
	}
	for _, stream := range answer.Streams {
		if stream.CodecType == "video" {
			info.Codec, info.Width, info.Height = stream.CodecName, stream.Width, stream.Height
			break
		}
	}
	if info.Width == 0 || info.Height == 0 {
		return crosspostDomain.MediaInfo{}, fmt.Errorf("media worker: no picture stream")
	}
	if info.Kind == crosspostDomain.MediaImage {
		info.Seconds = 0
	}
	return info, nil
}

// fetch copies the object into the job dir, refusing unknown types and anything over the input cap.
func (f *FFmpeg) fetch(ctx context.Context, key string, job job) (string, string, error) {
	body, size, contentType, err := f.cfg.Store.Open(ctx, key)
	if err != nil {
		return "", "", fmt.Errorf("media worker: open %s: %w", key, err)
	}
	defer func() { _ = body.Close() }()
	contentType = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	if _, ok := inputFormats[contentType]; !ok {
		return "", "", fmt.Errorf("media worker: unsupported type %q", contentType)
	}
	if size > f.cfg.MaxInputBytes {
		return "", "", fmt.Errorf("media worker: input is %d bytes, over %d", size, f.cfg.MaxInputBytes)
	}
	path := filepath.Join(job.dir, "in")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", "", err
	}
	written, err := io.Copy(file, io.LimitReader(body, f.cfg.MaxInputBytes+1))
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", "", fmt.Errorf("media worker: copy input: %w", err)
	}
	if written > f.cfg.MaxInputBytes {
		return "", "", fmt.Errorf("media worker: input over %d bytes", f.cfg.MaxInputBytes)
	}
	return path, contentType, nil
}

// run starts the tool through /bin/sh only to set ulimits, then execs it with an empty environment.
func (f *FFmpeg) run(ctx context.Context, tool string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, f.cfg.Timeout)
	defer cancel()
	limits := fmt.Sprintf("ulimit -t %d && ulimit -f %d", f.cfg.CPUSeconds, (f.cfg.MaxInputBytes*2)/512)
	if runtime.GOOS == "linux" {
		limits += fmt.Sprintf(" && ulimit -v %d", f.cfg.MemoryBytes/1024)
	}
	script := limits + ` && exec "$0" "$@"`
	cmd := exec.CommandContext(ctx, "/bin/sh", append([]string{"-c", script, tool}, args...)...)
	cmd.Env = []string{} // no secrets, no proxy settings
	cmd.Dir = f.cfg.WorkDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &limitedWriter{buf: &stderr, max: 4096}
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("media worker: %s timed out after %s", filepath.Base(tool), f.cfg.Timeout)
		}
		return nil, fmt.Errorf("media worker: %s failed: %w: %s", filepath.Base(tool), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

type job struct{ dir string }

func (f *FFmpeg) newJob() (job, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return job{}, err
	}
	dir := filepath.Join(f.cfg.WorkDir, "job-"+hex.EncodeToString(buf))
	if err := os.Mkdir(dir, 0o700); err != nil {
		return job{}, fmt.Errorf("media worker: job dir: %w", err)
	}
	return job{dir: dir}, nil
}

func (j job) remove() { _ = os.RemoveAll(j.dir) }

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// limitedWriter keeps the first max bytes of stderr for the error message.
type limitedWriter struct {
	buf *bytes.Buffer
	max int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if room := w.max - w.buf.Len(); room > 0 {
		w.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}
