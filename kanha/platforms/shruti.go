package platforms

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"KanhaMusic/kanha/logger"

	state "KanhaMusic/kanha/core/models"
	td "github.com/Kanha/Meow"
)

const PlatformShruti state.PlatformName = "Shruti"

type ShrutiPlatform struct {
	client *http.Client
}

func init() {
	Register(&ShrutiPlatform{
		client: &http.Client{Timeout: 10 * time.Minute},
	})
}

func (s *ShrutiPlatform) Name() state.PlatformName { return PlatformShruti }
func (s *ShrutiPlatform) Priority() int            { return 80 }
func (s *ShrutiPlatform) CanGet(_ string) bool     { return false }

func (s *ShrutiPlatform) Get(_ string, _ bool) ([]*state.Track, error) {
	return nil, errors.New("Shruti is a downloader-only platform")
}

func (s *ShrutiPlatform) CanDownload(source state.PlatformName) bool {
	return source == PlatformYouTube || source == PlatformYtDlp
}

func (s *ShrutiPlatform) Download(ctx context.Context, track *state.Track, _ *td.Message) (string, error) {
	if track == nil || track.URL == "" {
		return "", errors.New("empty track URL")
	}

	apiKey := strings.TrimSpace(os.Getenv("SHRUTI_API_KEY"))
	if apiKey == "" || apiKey == "YOUR_API_KEY" {
		return "", errors.New("SHRUTI_API_KEY is not configured")
	}

	base := strings.TrimRight(strings.TrimSpace(os.Getenv("SHRUTI_API_URL")), "/")
	if base == "" {
		base = "https://api.shrutibots.site"
	}

	videoID := extractYouTubeID(track.URL)
	if videoID == "" {
		return "", errors.New("could not extract YouTube video ID")
	}

	kind, ext := "audio", ".mp3"
	if track.Video {
		kind, ext = "video", ".mp4"
	}

	if f := findFile(track); f != "" {
		logger.Debug("Shruti: cache hit " + f)
		return f, nil
	}

	if err := os.MkdirAll("downloads", 0755); err != nil {
		return "", fmt.Errorf("create downloads directory: %w", err)
	}

	out := getPath(track, ext)
	if out == "" {
		out = "downloads/" + videoID + ext
	}

	u, err := url.Parse(base + "/download")
	if err != nil {
		return "", fmt.Errorf("invalid Shruti API URL: %w", err)
	}
	q := u.Query()
	q.Set("url", videoID)
	q.Set("type", kind)
	q.Set("api_key", apiKey)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", fmt.Errorf("create Shruti request: %w", err)
	}

	logger.Info("Shruti: downloading " + videoID + " (" + kind + ")")
	resp, err := s.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("Shruti request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return "", fmt.Errorf("Shruti API returned %s: %s", resp.Status, msg)
	}

	tmp := out + ".part"
	_ = os.Remove(tmp)
	f, err := os.Create(tmp)
	if err != nil {
		return "", fmt.Errorf("create output file: %w", err)
	}

	_, copyErr := io.Copy(f, resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("download from Shruti failed: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("close downloaded file: %w", closeErr)
	}

	info, err := os.Stat(tmp)
	if err != nil || info.Size() == 0 {
		_ = os.Remove(tmp)
		return "", errors.New("Shruti returned an empty file")
	}

	_ = os.Remove(out)
	if err := os.Rename(tmp, out); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("finalize downloaded file: %w", err)
	}

	logger.Info("Shruti: downloaded " + out)
	return out, nil
}

func extractYouTubeID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if m := videoIDRe1.FindStringSubmatch(raw); len(m) > 1 {
		return m[1]
	}
	if u, err := url.Parse(raw); err == nil {
		if v := u.Query().Get("v"); len(v) == 11 {
			return v
		}
	}
	return ""
}
