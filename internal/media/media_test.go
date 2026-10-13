package media

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestThumbnail_CreatedNonEmptyAndMode0644(t *testing.T) {
	if !Available() {
		t.Skip("ffmpeg/ffprobe not available on system")
	}

	tempDir := t.TempDir()
	SetTempDir(tempDir)

	// Create a synthetic MP4 test video using ffmpeg
	videoPath := filepath.Join(tempDir, "test_video.mp4")
	cmd := exec.Command("ffmpeg", "-y", "-f", "lavfi", "-i", "testsrc=duration=2:size=320x240:rate=1", "-c:v", "libx264", videoPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot generate synthetic video with ffmpeg: %v (%s)", err, string(out))
	}

	ctx := context.Background()
	thumb := VideoThumbnail(ctx, videoPath, 2)
	if thumb == "" {
		t.Fatal("VideoThumbnail returned empty path")
	}
	defer os.Remove(thumb)

	st, err := os.Stat(thumb)
	if err != nil {
		t.Fatalf("stat thumbnail file failed: %v", err)
	}
	if st.Size() == 0 {
		t.Fatalf("thumbnail file is empty")
	}
	if perm := st.Mode().Perm(); perm != 0o644 {
		t.Errorf("expected thumbnail permissions 0644, got %04o", perm)
	}

	// Create a synthetic image and test ImageThumbnail
	imgPath := filepath.Join(tempDir, "test_image.jpg")
	imgCmd := exec.Command("ffmpeg", "-y", "-f", "lavfi", "-i", "color=c=blue:s=320x240:d=1", "-frames:v", "1", imgPath)
	if _, err := imgCmd.CombinedOutput(); err == nil {
		imgThumb := ImageThumbnail(ctx, imgPath)
		if imgThumb == "" {
			t.Fatal("ImageThumbnail returned empty path")
		}
		defer os.Remove(imgThumb)

		stImg, err := os.Stat(imgThumb)
		if err != nil {
			t.Fatalf("stat image thumbnail file failed: %v", err)
		}
		if stImg.Size() == 0 {
			t.Fatalf("image thumbnail file is empty")
		}
		if perm := stImg.Mode().Perm(); perm != 0o644 {
			t.Errorf("expected image thumbnail permissions 0644, got %04o", perm)
		}
	}
}
