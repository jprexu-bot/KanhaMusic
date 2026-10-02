/*
 * ● KanhaMusic
 * ○ A high-performance engine for streaming music in Telegram voicechats.
 *
 * Copyright (C) 2026 Kanha
 *
 * This program is free software: you can redistribute it and/or modify it under the
 * terms of the GNU General Public License as published by the Free Software Foundation,
 * either version 3 of the License, or (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful, but WITHOUT ANY
 * WARRANTY; without even the implied warranty of MERCHANTABILITY or FITNESS FOR A
 * PARTICULAR PURPOSE. See the GNU General Public License for more details.
 *
 * Repository: https://github.com/Oyekanhaa/KanhaMusic
 */

package platforms

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"KanhaMusic/kanha/logger"

	td "github.com/Kanha/Meow"

	"KanhaMusic/config/cookies"
	state "KanhaMusic/kanha/core/models"
)

const PlatformYtDlp state.PlatformName = "YtDlp"

type YtdlpPlatform struct{}

type ytdlpInfo struct {
	ID          string      `json:"id"`
	Title       string      `json:"title"`
	Duration    float64     `json:"duration"`
	Thumbnail   string      `json:"thumbnail"`
	URL         string      `json:"webpage_url"`
	OriginalURL string      `json:"original_url"`
	Uploader    string      `json:"uploader"`
	IsLive      bool        `json:"is_live"`
	Extractor   string      `json:"extractor"`
	Entries     []ytdlpInfo `json:"entries"`
}

var (
	bannedExtractors = map[string]bool{
		"alphaporno": true, "beeg": true, "behindkink": true, "bongacams": true,
		"cam4": true, "cammodels": true, "camsoda": true, "chaturbate": true,
		"drtuber": true, "eporner": true, "erocast": true, "eroprofile": true,
		"fourtube": true, "goshgay": true, "hellporno": true, "iwara": true,
		"lovehomeporn": true, "manyvids": true, "motherless": true, "murrtube": true,
		"nonktube": true, "noodlemagazine": true, "nubilesporn": true, "nuvid": true,
		"oftv": true, "peekvids": true, "pornbox": true, "pornflip": true,
		"pornhub": true, "pornotube": true, "pornovoisines": true, "pornoxo": true,
		"redgifs": true, "redtube": true, "rule34video": true, "sauceplus": true,
		"sexu": true, "slutload": true, "spankbang": true, "stripchat": true,
		"sunporno": true, "thisvid": true, "tnaflix": true, "toypics": true,
		"txxx": true, "xhamster": true, "xnxx": true, "xvideos": true,
		"xxxymovies": true, "youjizz": true, "youporn": true, "zenporn": true,
	}

	audioOnlyExtractors = map[string]bool{
		"soundcloud": true,
	}

	ytURLPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)(youtube\.com|youtu\.be|music\.youtube\.com)`),
	}
)

func init() {
	Register(&YtdlpPlatform{})
}

func (y *YtdlpPlatform) Name() state.PlatformName {
	return PlatformYtDlp
}

func (y *YtdlpPlatform) Priority() int {
	return 60
}

func (y *YtdlpPlatform) CanGet(query string) bool {
	if _, err := sanitizeMediaURL(query); err != nil {
		return false
	}

	parsed, err := url.Parse(query)
	if err != nil {
		return false
	}

	host := strings.ToLower(parsed.Host)

	return host != "t.me" &&
		host != "telegram.me" &&
		host != "telegram.dog" &&
		!strings.HasSuffix(host, ".t.me")
}

func (y *YtdlpPlatform) Get(query string, video bool) ([]*state.Track, error) {
	safeURL, err := sanitizeMediaURL(query)
	if err != nil {
		return nil, errUnsafeURL
	}

	info, err := y.extractMetadata(safeURL)
	if err != nil {
		return nil, fmt.Errorf("failed to extract metadata: %w", err)
	}

	if info.IsLive {
		return nil, errors.New("live streams are not supported")
	}

	if bannedExtractors[strings.ToLower(info.Extractor)] {
		return nil, errors.New("adult content is not allowed")
	}

	var tracks []*state.Track

	if len(info.Entries) > 0 {
		for _, entry := range info.Entries {
			if entry.IsLive || bannedExtractors[strings.ToLower(entry.Extractor)] {
				continue
			}

			tracks = append(tracks, y.toTrack(&entry, video))
		}
	} else {
		tracks = []*state.Track{
			y.toTrack(info, video),
		}
	}

	return tracks, nil
}

// CanDownload keeps YouTube playback independent of any external streaming API.
func (y *YtdlpPlatform) CanDownload(source state.PlatformName) bool {
	return source == PlatformYtDlp || source == PlatformYouTube
}

// youtubeCookieFile checks for an explicitly configured cookie file,
// then falls back to the repository cookie manager.
func youtubeCookieFile() string {
	for _, key := range []string{
		"YTDLP_COOKIES_FILE",
		"YOUTUBE_COOKIES_FILE",
		"YTDLP_COOKIE_FILE",
	} {
		if path := strings.TrimSpace(os.Getenv(key)); path != "" {
			if st, err := os.Stat(path); err == nil &&
				!st.IsDir() && st.Size() > 0 {
				return path
			}

			if abs, err := filepath.Abs(path); err == nil {
				if st, err := os.Stat(abs); err == nil &&
					!st.IsDir() && st.Size() > 0 {
					return abs
				}
			}
		}
	}

	if cf, err := cookies.GetRandomCookieFile(); err == nil &&
		strings.TrimSpace(cf) != "" {

		if st, err := os.Stat(cf); err == nil &&
			!st.IsDir() && st.Size() > 0 {
			return cf
		}
	}

	return ""
}

func (y *YtdlpPlatform) Download(
	ctx context.Context,
	track *state.Track,
	_ *td.Message,
) (string, error) {

	if f := findFile(track); f != "" {
		logger.Debug("YtDlp: cache hit " + f)
		return f, nil
	}

	safeURL, err := sanitizeMediaURL(track.URL)
	if err != nil {
		return "", errUnsafeURL
	}

	baseArgs := []string{
		"--no-playlist",
		"--no-part",
		"--geo-bypass",
		"--no-warnings",
		"--no-check-certificate",
		"--retries", "3",
		"--fragment-retries", "3",
		"--socket-timeout", "30",
		"--js-runtimes", "deno:/usr/local/bin/deno",
		"-o", getPath(track, ".%(ext)s"),
	}

	if track.Video {
		baseArgs = append(
			baseArgs,
			"-f",
			"bv*[height<=1080]+ba/b[height<=1080]/b",
		)
	} else {
		baseArgs = append(
			baseArgs,
			"-f", "ba/b",
			"-x",
			"--audio-format", "mp3",
			"--audio-quality", "0",
			"--concurrent-fragments", "4",
		)
	}

	baseArgs = append(
		baseArgs,
		"--sleep-requests", "1",
		"--sleep-interval", "2",
		"--max-sleep-interval", "5",
	)

	attempts := make([][]string, 0, 4)

	cookieFile := ""

	if y.isYouTubeURL(track.URL) {
		cookieFile = youtubeCookieFile()

		if cookieFile != "" {
			withCookie := append([]string{}, baseArgs...)
			withCookie = append(
				withCookie,
				"--cookies", cookieFile,
			)

			attempts = append(attempts, withCookie)
		}
	}

	attempts = append(
		attempts,
		append([]string{}, baseArgs...),
	)

	if y.isYouTubeURL(track.URL) {
		variant := append([]string{}, baseArgs...)

		variant = append(
			variant,
			"--extractor-args",
			"youtube:player_js_variant=main",
		)

		attempts = append(attempts, variant)

		if cookieFile != "" {
			cookieVariant := append([]string{}, variant...)

			cookieVariant = append(
				cookieVariant,
				"--cookies", cookieFile,
			)

			attempts = append(attempts, cookieVariant)
		}
	}

	var lastErr error
	var lastStdout string
	var lastStderr string

	for i, attempt := range attempts {
		findAndRemove(track)

		args := append([]string{}, attempt...)
		args = append(args, "--", safeURL)

		var stdout bytes.Buffer
		var stderr bytes.Buffer

		cmd := exec.CommandContext(
			ctx,
			"yt-dlp",
			args...,
		)

		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err = cmd.Run()

		lastStdout = strings.TrimSpace(stdout.String())
		lastStderr = strings.TrimSpace(stderr.String())

		if err == nil {
			if p := findFile(track); p != "" {
				logger.Infof(
					"YtDlp: downloaded %s (attempt %d)",
					p,
					i+1,
				)

				return p, nil
			}

			lastErr = errors.New(
				"yt-dlp produced no output file",
			)

			continue
		}

		lastErr = err

		logStderr := lastStderr
		if len(logStderr) > 6000 {
			logStderr = logStderr[len(logStderr)-6000:]
		}

		logStdout := lastStdout
		if len(logStdout) > 3000 {
			logStdout = logStdout[len(logStdout)-3000:]
		}

		logger.Errorf(
			"YtDlp: DOWNLOAD FAILED (attempt %d/%d)\n"+
				"URL: %s\n"+
				"Command error: %v\n"+
				"STDERR:\n%s\n"+
				"STDOUT:\n%s",
			i+1,
			len(attempts),
			safeURL,
			err,
			logStderr,
			logStdout,
		)

		if isDownloadCancelled(err) {
			return "", err
		}
	}

	findAndRemove(track)

	return "", fmt.Errorf(
		"yt-dlp failed after %d attempts: %w\nstdout: %s\nstderr: %s",
		len(attempts),
		lastErr,
		lastStdout,
		lastStderr,
	)
}

func (y *YtdlpPlatform) extractMetadata(
	urlStr string,
) (*ytdlpInfo, error) {

	ctx, cancel := context.WithTimeout(
		context.Background(),
		5*time.Minute,
	)
	defer cancel()

	args := []string{
		"-j",
		"--flat-playlist",
		"--no-warnings",
		"--no-check-certificate",
	}

	if y.isYouTubeURL(urlStr) {
		if cf := youtubeCookieFile(); cf != "" {
			args = append(
				args,
				"--cookies", cf,
			)
		}
	}

	args = append(args, "--", urlStr)

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	cmd := exec.CommandContext(
		ctx,
		"yt-dlp",
		args...,
	)

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf(
			"metadata extraction failed: %w\n%s",
			err,
			stderr.String(),
		)
	}

	lines := strings.Split(
		strings.TrimSpace(stdout.String()),
		"\n",
	)

	if len(lines) > 1 {
		var info ytdlpInfo

		for _, line := range lines {
			var entry ytdlpInfo

			if err := json.Unmarshal(
				[]byte(line),
				&entry,
			); err != nil {
				logger.Debugf(
					"YtDlp: skip bad entry: %v",
					err,
				)

				continue
			}

			info.Entries = append(
				info.Entries,
				entry,
			)
		}

		if len(info.Entries) == 0 {
			return nil, errors.New(
				"no valid entries in playlist",
			)
		}

		return &info, nil
	}

	var info ytdlpInfo

	if err := json.Unmarshal(
		stdout.Bytes(),
		&info,
	); err != nil {
		return nil, fmt.Errorf(
			"failed to parse metadata JSON: %w",
			err,
		)
	}

	return &info, nil
}

func (y *YtdlpPlatform) toTrack(
	info *ytdlpInfo,
	video bool,
) *state.Track {

	if video &&
		audioOnlyExtractors[strings.ToLower(info.Extractor)] {
		video = false
	}

	return &state.Track{
		ID:       info.ID,
		Title:    info.Title,
		Duration: int(info.Duration),
		Artwork:  info.Thumbnail,
		URL:      firstNonEmpty(
			info.OriginalURL,
			info.URL,
		),
		Source: PlatformYtDlp,
		Video:  video,
	}
}

func (y *YtdlpPlatform) isYouTubeURL(
	urlStr string,
) bool {

	for _, p := range ytURLPatterns {
		if p.MatchString(urlStr) {
			return true
		}
	}

	return false
}
