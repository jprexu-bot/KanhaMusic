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
	"os/exec"
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
	// audioOnlyExtractors are sites without video streams; a video request is
	// silently downgraded to audio.
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

func (y *YtdlpPlatform) Name() state.PlatformName { return PlatformYtDlp }
func (y *YtdlpPlatform) Priority() int            { return 60 }

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
		tracks = []*state.Track{y.toTrack(info, video)}
	}

	return tracks, nil
}

// CanDownload keeps YouTube playback independent of any external streaming API.
// yt-dlp is bundled by the Docker image and is the primary playback downloader.
func (y *YtdlpPlatform) CanDownload(source state.PlatformName) bool {
	return source == PlatformYtDlp || source == PlatformYouTube
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

	// Keep the downloader independent of Meow API.  YouTube changes its
	// streaming clients frequently, so use yt-dlp's current JS/EJS support
	// and retry once with an available cookie file if the first attempt fails.
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
		"--remote-components", "ejs:github",
		"-o", getPath(track, ".%(ext)s"),
	}

	if y.isYouTubeURL(track.URL) {
		baseArgs = append(baseArgs, "--extractor-args", "youtube:player_client=android,web")
	}

	if track.Video {
		baseArgs = append(baseArgs,
			"-f", "bv*[height<=1080]+ba/b[height<=1080]/b",
		)
	} else {
		baseArgs = append(baseArgs,
			"-f", "ba/b",
			"-x",
			"--audio-format", "mp3",
			"--audio-quality", "0",
			"--concurrent-fragments", "4",
		)
	}

	args := append(append([]string{}, baseArgs...), "--", safeURL)

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "yt-dlp", args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	// If YouTube rejects the anonymous request, retry with a configured cookie.
	// This is still completely independent of any API key.
	if err != nil && y.isYouTubeURL(track.URL) {
		if cookieFile, cookieErr := cookies.GetRandomCookieFile(); cookieErr == nil && cookieFile != "" {
			findAndRemove(track)
			stdout.Reset()
			stderr.Reset()
			args = append(append([]string{}, baseArgs...), "--cookies", cookieFile, "--", safeURL)
			cmd = exec.CommandContext(ctx, "yt-dlp", args...)
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			err = cmd.Run()
		}
	}

	if err != nil {
		findAndRemove(track)
		if isDownloadCancelled(err) {
			return "", err
		}
		return "", fmt.Errorf(
			"yt-dlp failed: %w\nstdout: %s\nstderr: %s",
			err, strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()),
		)
	}

	p := findFile(track)
	if p == "" {
		return "", errors.New("yt-dlp produced no output file")
	}

	logger.Infof("YtDlp: downloaded %s", p)
	return p, nil
}

func (y *YtdlpPlatform) extractMetadata(urlStr string) (*ytdlpInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	args := []string{
		"-j", "--flat-playlist",
		"--no-warnings", "--no-check-certificate",
	}

	if y.isYouTubeURL(urlStr) {
		if cf, err := cookies.GetRandomCookieFile(); err == nil && cf != "" {
			args = append(args, "--cookies", cf)
		}
	}

	args = append(args, "--", urlStr)

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "yt-dlp", args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("metadata extraction failed: %w\n%s", err, stderr.String())
	}

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")

	if len(lines) > 1 {
		var info ytdlpInfo
		for _, line := range lines {
			var entry ytdlpInfo
			if err := json.Unmarshal([]byte(line), &entry); err != nil {
				logger.Debugf("YtDlp: skip bad entry: %v", err)
				continue
			}
			info.Entries = append(info.Entries, entry)
		}
		if len(info.Entries) == 0 {
			return nil, errors.New("no valid entries in playlist")
		}
		return &info, nil
	}

	var info ytdlpInfo
	if err := json.Unmarshal(stdout.Bytes(), &info); err != nil {
		return nil, fmt.Errorf("failed to parse metadata JSON: %w", err)
	}
	return &info, nil
}

func (y *YtdlpPlatform) toTrack(info *ytdlpInfo, video bool) *state.Track {
	if video && audioOnlyExtractors[strings.ToLower(info.Extractor)] {
		video = false
	}
	return &state.Track{
		ID:       info.ID,
		Title:    info.Title,
		Duration: int(info.Duration),
		Artwork:  info.Thumbnail,
		URL:      firstNonEmpty(info.OriginalURL, info.URL),
		Source:   PlatformYtDlp,
		Video:    video,
	}
}

func (y *YtdlpPlatform) isYouTubeURL(urlStr string) bool {
	for _, p := range ytURLPatterns {
		if p.MatchString(urlStr) {
			return true
		}
	}
	return false
}
