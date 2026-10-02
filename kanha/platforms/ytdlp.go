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
			if entry.IsLive ||
				bannedExtractors[strings.ToLower(entry.Extractor)] {
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

// YouTube playback is handled directly through yt-dlp.
func (y *YtdlpPlatform) CanDownload(source state.PlatformName) bool {
	return source == PlatformYtDlp || source == PlatformYouTube
}

func (y *YtdlpPlatform) Download(
	ctx context.Context,
	track *state.Track,
	_ *td.Message,
) (string, error) {

	// Use already downloaded file if available.
	if f := findFile(track); f != "" {
		logger.Debug("YtDlp: cache hit " + f)
		return f, nil
	}

	safeURL, err := sanitizeMediaURL(track.URL)
	if err != nil {
		return "", errUnsafeURL
	}

	/*
		Keep yt-dlp on its normal supported extraction path.

		No browser cookies, account authentication,
		PO-token extraction or alternate-client tricks
		are injected here.
	*/
	baseArgs := []string{
		"--no-playlist",
		"--no-part",
		"--geo-bypass",
		"--no-warnings",
		"--no-check-certificate",

		"--retries", "3",
		"--fragment-retries", "3",
		"--extractor-retries", "2",

		"--socket-timeout", "30",

		// yt-dlp JavaScript/EJS support.
		"--js-runtimes", "deno:/usr/local/bin/deno",

		// Avoid hammering the source.
		"--sleep-requests", "5",
		"--sleep-interval", "5",
		"--max-sleep-interval", "10",

		// Exponential retry delays.
		"--retry-sleep", "http:exp=5:30",
		"--retry-sleep", "fragment:exp=2:10",

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
			"--concurrent-fragments", "2",
		)
	}

	// Only one retry after the initial attempt.
	// Repeating a server-side rejection many times
	// does not make the request more likely to succeed.
	attempts := [][]string{
		append([]string{}, baseArgs...),
		append([]string{}, baseArgs...),
	}

	var lastErr error
	var lastStdout string
	var lastStderr string

	for i, attempt := range attempts {

		if i > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()

			case <-time.After(10 * time.Second):
			}
		}

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

		// Detect YouTube-side rejection.
		if isYouTubeBotCheck(lastStderr) ||
			isYouTubeBotCheck(lastStdout) {

			logger.Warnf(
				"YtDlp: YouTube rejected the request. URL: %s",
				safeURL,
			)

			findAndRemove(track)

			return "",
				errors.New(
					"YouTube temporarily rejected this request; please try another song or try again later",
				)
		}

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

	return "",
		fmt.Errorf(
			"yt-dlp failed after %d attempts: %w\nstdout: %s\nstderr: %s",
			len(attempts),
			lastErr,
			lastStdout,
			lastStderr,
		)
}

func isYouTubeBotCheck(output string) bool {
	v := strings.ToLower(output)

	patterns := []string{
		"sign in to confirm you're not a bot",
		"sign in to confirm you’re not a bot",
		"confirm you're not a bot",
		"confirm you’re not a bot",

		"http error 429",
		"too many requests",
	}

	for _, pattern := range patterns {
		if strings.Contains(v, pattern) {
			return true
		}
	}

	return false
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

		"--extractor-retries", "2",
		"--socket-timeout", "30",

		"--js-runtimes", "deno:/usr/local/bin/deno",

		"--sleep-requests", "3",
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

		msg := strings.TrimSpace(stderr.String())

		if isYouTubeBotCheck(msg) {
			return nil,
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
			if entry.IsLive ||
				bannedExtractors[strings.ToLower(entry.Extractor)] {
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

// YouTube playback is handled directly through yt-dlp.
func (y *YtdlpPlatform) CanDownload(source state.PlatformName) bool {
	return source == PlatformYtDlp || source == PlatformYouTube
}

func (y *YtdlpPlatform) Download(
	ctx context.Context,
	track *state.Track,
	_ *td.Message,
) (string, error) {

	// Use already downloaded file if available.
	if f := findFile(track); f != "" {
		logger.Debug("YtDlp: cache hit " + f)
		return f, nil
	}

	safeURL, err := sanitizeMediaURL(track.URL)
	if err != nil {
		return "", errUnsafeURL
	}

	/*
		Keep yt-dlp on its normal supported extraction path.

		No browser cookies, account authentication,
		PO-token extraction or alternate-client tricks
		are injected here.
	*/
	baseArgs := []string{
		"--no-playlist",
		"--no-part",
		"--geo-bypass",
		"--no-warnings",
		"--no-check-certificate",

		"--retries", "3",
		"--fragment-retries", "3",
		"--extractor-retries", "2",

		"--socket-timeout", "30",

		// yt-dlp JavaScript/EJS support.
		"--js-runtimes", "deno:/usr/local/bin/deno",

		// Avoid hammering the source.
		"--sleep-requests", "5",
		"--sleep-interval", "5",
		"--max-sleep-interval", "10",

		// Exponential retry delays.
		"--retry-sleep", "http:exp=5:30",
		"--retry-sleep", "fragment:exp=2:10",

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
			"--concurrent-fragments", "2",
		)
	}

	// Only one retry after the initial attempt.
	// Repeating a server-side rejection many times
	// does not make the request more likely to succeed.
	attempts := [][]string{
		append([]string{}, baseArgs...),
		append([]string{}, baseArgs...),
	}

	var lastErr error
	var lastStdout string
	var lastStderr string

	for i, attempt := range attempts {

		if i > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()

			case <-time.After(10 * time.Second):
			}
		}

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

		// Detect YouTube-side rejection.
		if isYouTubeBotCheck(lastStderr) ||
			isYouTubeBotCheck(lastStdout) {

			logger.Warnf(
				"YtDlp: YouTube rejected the request. URL: %s",
				safeURL,
			)

			findAndRemove(track)

			return "",
				errors.New(
					"YouTube temporarily rejected this request; please try another song or try again later",
				)
		}

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

	return "",
		fmt.Errorf(
			"yt-dlp failed after %d attempts: %w\nstdout: %s\nstderr: %s",
			len(attempts),
			lastErr,
			lastStdout,
			lastStderr,
		)
}

func isYouTubeBotCheck(output string) bool {
	v := strings.ToLower(output)

	patterns := []string{
		"sign in to confirm you're not a bot",
		"sign in to confirm you’re not a bot",
		"confirm you're not a bot",
		"confirm you’re not a bot",

		"http error 429",
		"too many requests",
	}

	for _, pattern := range patterns {
		if strings.Contains(v, pattern) {
			return true
		}
	}

	return false
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

		"--extractor-retries", "2",
		"--socket-timeout", "30",

		"--js-runtimes", "deno:/usr/local/bin/deno",

		"--sleep-requests", "3",
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

		msg := strings.TrimSpace(stderr.String())

		if isYouTubeBotCheck(msg) {
			return nil,
				errors.New(
					"YouTube temporarily rejected metadata extraction; please try again later",
				)
		}

		if len(msg) > 4000 {
			msg = msg[len(msg)-4000:]
		}

		return nil,
			fmt.Errorf(
				"metadata extraction failed: %w\n%s",
				err,
				msg,
			)
	}

	lines := strings.Split(
		strings.TrimSpace(stdout.String()),
		"\n",
	)

	// Playlist / multiple JSON entries.
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
			return nil,
				errors.New(
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
		return nil,
			fmt.Errorf(
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
		audioOnlyExtractors[
			strings.ToLower(info.Extractor)
		] {
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
