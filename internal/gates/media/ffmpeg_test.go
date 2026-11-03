package media

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	crosspostDomain "bloomary-api/internal/domain/crosspost"

	"github.com/stretchr/testify/require"
)

type memoryStore struct {
	objects map[string][]byte
	types   map[string]string
}

func (m *memoryStore) Open(_ context.Context, key string) (io.ReadCloser, int64, string, error) {
	data := m.objects[key]
	return io.NopCloser(bytes.NewReader(data)), int64(len(data)), m.types[key], nil
}

func (m *memoryStore) PutStream(_ context.Context, key, contentType string, body io.Reader, _ int64) error {
	data, err := io.ReadAll(body)
	m.objects[key], m.types[key] = data, contentType
	return err
}

// tools finds ffmpeg and ffprobe or skips: the MediaWorker test needs them installed locally.
func tools(t *testing.T) (string, string) {
	t.Helper()
	ffmpeg, err1 := exec.LookPath("ffmpeg")
	ffprobe, err2 := exec.LookPath("ffprobe")
	if err1 != nil || err2 != nil {
		t.Skip("ffmpeg/ffprobe not installed: skipping the MediaWorker sample test")
	}
	return ffmpeg, ffprobe
}

// sample makes a tiny synthetic file with ffmpeg's own generators (no network, no recorded media).
func sample(t *testing.T, ffmpeg, name string, args ...string) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	out, err := exec.Command(ffmpeg, append(append([]string{"-hide_banner", "-loglevel", "error"}, args...), "-y", path)...).CombinedOutput()
	require.NoError(t, err, string(out))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

func TestFFmpegProbesAndMakesVariants(t *testing.T) {
	// Verifies: BE-076
	ffmpeg, ffprobe := tools(t)
	video := sample(t, ffmpeg, "in.mp4", "-f", "lavfi", "-i", "testsrc=size=360x640:rate=25:duration=2",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest")
	image := sample(t, ffmpeg, "in.png", "-f", "lavfi", "-i", "testsrc=size=600x200", "-frames:v", "1")
	store := &memoryStore{
		objects: map[string][]byte{"v/master.mp4": video, "i/wide.png": image},
		types:   map[string]string{"v/master.mp4": "video/mp4", "i/wide.png": "image/png"},
	}
	worker, err := NewFFmpeg(Config{Store: store, FFmpeg: ffmpeg, FFprobe: ffprobe, WorkDir: filepath.Join(t.TempDir(), "media")})
	require.NoError(t, err)
	ctx := context.Background()

	info, err := worker.Probe(ctx, "v/master.mp4")
	require.NoError(t, err)
	require.Equal(t, crosspostDomain.MediaVideo, info.Kind)
	require.Equal(t, "h264", info.Codec)
	require.Equal(t, 360, info.Width)
	require.Equal(t, 640, info.Height)
	require.InDelta(t, 2.0, info.Seconds, 0.2)
	require.Equal(t, int64(len(video)), info.Bytes)

	// X variant: fit inside 720×1280, never upscaled.
	ref, err := worker.Variant(ctx, "v/master.mp4", crosspostDomain.VariantSpec{ContentType: "video/mp4", Width: 720, Height: 1280})
	require.NoError(t, err)
	require.Equal(t, "v/master.mp4.v720x1280.mp4", ref.Key)
	require.Equal(t, "video/mp4", store.types[ref.Key])
	require.Equal(t, 360, ref.Info.Width)
	require.Equal(t, 640, ref.Info.Height)
	require.Len(t, ref.SHA256, 64)

	// Instagram image fit: 3:1 is wider than 1.91:1, so it is padded to 600×315 (even height), as JPEG.
	ref, err = worker.Variant(ctx, "i/wide.png", crosspostDomain.VariantSpec{ContentType: "image/jpeg", Width: 1440, Height: 1800, AspectMin: 0.8, AspectMax: 1.91})
	require.NoError(t, err)
	require.Equal(t, "image/jpeg", store.types[ref.Key])
	require.Equal(t, 600, ref.Info.Width)
	require.InDelta(t, 600/1.91, float64(ref.Info.Height), 2)
	require.Equal(t, crosspostDomain.MediaImage, ref.Info.Kind)

	entries, err := os.ReadDir(filepath.Join(worker.cfg.WorkDir))
	require.NoError(t, err)
	require.Empty(t, entries, "job directories are removed after each job")
}
