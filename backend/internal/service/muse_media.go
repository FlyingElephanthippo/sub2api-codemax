package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const MuseVideoModel = "muse-video"

const museReferenceImageMaxBytes = 10 << 20

type museVideoPendingContextKey struct{}

// IsMuseMediaAccount is explicit: a custom Grok base URL alone never enables Muse semantics.
func IsMuseMediaAccount(account *Account) bool {
	return account != nil && account.IsGrok() && account.Type == AccountTypeAPIKey &&
		account.Credentials["media_provider"] == "muse"
}

func WithMuseVideoPendingBilling(ctx context.Context, pending *GrokVideoPendingBilling) context.Context {
	return context.WithValue(ctx, museVideoPendingContextKey{}, pending)
}

// ValidateMuseVideoPricing deliberately excludes Grok's flat/default prices.
// The 480p key is the configured base billing tier, not a guarantee of output dimensions.
func ValidateMuseVideoPricing(apiKey *APIKey) error {
	if apiKey == nil || apiKey.Group == nil {
		return errors.New("Muse video requires an explicit group muse-video/480p price")
	}
	price := LookupVideoModelPrice(apiKey.Group.VideoModelPrices, MuseVideoModel, VideoBillingResolution480P)
	if price == nil || *price < 0 || math.IsNaN(*price) || math.IsInf(*price, 0) {
		return errors.New("Muse video requires an explicit finite nonnegative group muse-video/480p price")
	}
	return nil
}

// ParseMuseVideoRequest converts the supported OpenAI multipart/seconds form to
// Muse JSON before moderation, balance estimation, and upstream submission.
func ParseMuseVideoRequest(contentType string, body []byte) (GrokMediaRequestInfo, []byte, error) {
	info := GrokMediaRequestInfo{Model: MuseVideoModel, N: 1, DurationSeconds: 6, Resolution: VideoBillingResolution480P}
	fields := map[string]any{}
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return info, nil, errors.New("invalid video Content-Type")
	}
	if mediaType == "application/json" {
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.UseNumber()
		start, err := decoder.Token()
		if err != nil || start != json.Delim('{') {
			return info, nil, errors.New("video body must be a JSON object")
		}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return info, nil, errors.New("invalid video JSON body")
			}
			name, ok := key.(string)
			if !ok {
				return info, nil, errors.New("invalid video JSON field")
			}
			if _, exists := fields[name]; exists {
				return info, nil, fmt.Errorf("duplicate video field %s", name)
			}
			var value any
			if err := decoder.Decode(&value); err != nil {
				return info, nil, errors.New("invalid video JSON value")
			}
			fields[name] = value
		}
		if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
			return info, nil, errors.New("invalid video JSON body")
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return info, nil, errors.New("video body must contain one JSON object")
		}
	} else if mediaType == "multipart/form-data" {
		if params["boundary"] == "" {
			return info, nil, errors.New("video multipart boundary is required")
		}
		reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return info, nil, errors.New("invalid video multipart body")
			}
			name := part.FormName()
			if part.FileName() != "" {
				if name != "input_reference" && name != "image" {
					_ = part.Close()
					return info, nil, errors.New("only input_reference/image video uploads are supported")
				}
				data, readErr := io.ReadAll(io.LimitReader(part, museReferenceImageMaxBytes+1))
				_ = part.Close()
				if readErr != nil || len(data) > museReferenceImageMaxBytes {
					return info, nil, errors.New("video reference image exceeds 10 MiB")
				}
				dataURL, err := museImageDataURL(data, part.Header.Get("Content-Type"))
				if err != nil {
					return info, nil, err
				}
				if _, exists := fields["input_reference"]; exists {
					return info, nil, errors.New("only one video reference image is supported")
				}
				fields["input_reference"] = dataURL
			} else {
				data, readErr := io.ReadAll(io.LimitReader(part, 65537))
				_ = part.Close()
				if readErr != nil || len(data) > 65536 {
					return info, nil, errors.New("video form field exceeds 64 KiB")
				}
				if _, exists := fields[name]; exists {
					return info, nil, fmt.Errorf("duplicate video field %s", name)
				}
				fields[name] = string(data)
			}
		}
	} else {
		return info, nil, errors.New("video requests require application/json or multipart/form-data")
	}
	stringField := func(name string) (string, error) {
		value, exists := fields[name]
		if !exists {
			return "", nil
		}
		text, ok := value.(string)
		if !ok {
			return "", fmt.Errorf("%s must be a string", name)
		}
		return strings.TrimSpace(text), nil
	}
	model, err := stringField("model")
	if err != nil || model != MuseVideoModel {
		return info, nil, errors.New("model must be muse-video")
	}
	info.Prompt, err = stringField("prompt")
	if err != nil || info.Prompt == "" {
		return info, nil, errors.New("prompt is required")
	}
	duration, hasDuration, err := museIntegerField(fields, "duration")
	if err != nil {
		return info, nil, err
	}
	seconds, hasSeconds, err := museIntegerField(fields, "seconds")
	if err != nil {
		return info, nil, err
	}
	if hasDuration && hasSeconds && duration != seconds {
		return info, nil, errors.New("duration and seconds must match")
	}
	if hasDuration {
		info.DurationSeconds = duration
	} else if hasSeconds {
		info.DurationSeconds = seconds
	}
	if !validMuseVideoDuration(info.DurationSeconds) {
		return info, nil, errors.New("Muse video requested duration must be 5, 6, 8, or 10 seconds")
	}
	if n, exists, err := museIntegerField(fields, "n"); err != nil || (exists && n != 1) {
		return info, nil, errors.New("Muse video supports n=1 only")
	}
	payload := map[string]any{"model": MuseVideoModel, "prompt": info.Prompt, "duration": info.DurationSeconds}
	for _, name := range []string{"size", "aspect_ratio", "extra", "resolution"} {
		value, err := stringField(name)
		if err != nil {
			return info, nil, err
		}
		if value != "" {
			payload[name] = value
		}
	}
	info.Size, _ = stringField("size")
	info.AspectRatio, _ = stringField("aspect_ratio")
	if resolution, _ := stringField("resolution"); resolution != "" && resolution != VideoBillingResolution480P {
		return info, nil, errors.New("Muse video currently supports only the base 480p billing tier; output dimensions are determined by Muse")
	}
	if timeout, exists, err := museIntegerField(fields, "timeout"); err != nil {
		return info, nil, err
	} else if exists {
		if timeout < 1 || timeout > 1800 {
			return info, nil, errors.New("video timeout must be between 1 and 1800 seconds")
		}
		payload["timeout"] = timeout
	}
	for _, name := range []string{"input_reference", "image", "image_url", "reference_image", "images", "reference_images"} {
		value, exists := fields[name]
		if !exists {
			continue
		}
		raw, _ := json.Marshal(value)
		parsed := gjson.ParseBytes(raw)
		values := []gjson.Result{parsed}
		if parsed.IsArray() {
			values = parsed.Array()
		}
		for _, item := range values {
			reference := extractGrokMediaImageURL(item)
			if reference == "" {
				return info, nil, errors.New("video reference image must be a URL or data URL")
			}
			if strings.HasPrefix(reference, "data:") {
				if err := validateMuseImageDataURL(reference); err != nil {
					return info, nil, err
				}
			} else {
				u, err := url.Parse(reference)
				if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
					return info, nil, errors.New("video reference image must use HTTPS or a PNG/JPEG/WebP data URL")
				}
			}
			info.InputImageURLs = append(info.InputImageURLs, reference)
		}
	}
	if len(info.InputImageURLs) > 1 {
		return info, nil, errors.New("only one video reference image is supported")
	}
	if len(info.InputImageURLs) == 1 {
		payload["image"] = info.InputImageURLs[0]
	}
	if extra, _ := payload["extra"].(string); strings.TrimSpace(extra) != "" {
		// Muse appends extra to the upstream prompt; include it in moderation too.
		info.Prompt += "\n" + extra
	}
	forwardBody, err := json.Marshal(payload)
	return info, forwardBody, err
}

func validMuseVideoDuration(seconds int) bool {
	return seconds == 5 || seconds == 6 || seconds == 8 || seconds == 10
}

func museIntegerField(fields map[string]any, name string) (int, bool, error) {
	value, exists := fields[name]
	if !exists {
		return 0, false, nil
	}
	var text string
	switch v := value.(type) {
	case string:
		text = strings.TrimSpace(v)
	case json.Number:
		text = string(v)
	default:
		return 0, true, fmt.Errorf("%s must be an integer", name)
	}
	n, err := strconv.Atoi(text)
	if err != nil {
		return 0, true, fmt.Errorf("%s must be an integer", name)
	}
	return n, true, nil
}

func museImageDataURL(data []byte, declared string) (string, error) {
	actual := http.DetectContentType(data)
	if actual != "image/png" && actual != "image/jpeg" && actual != "image/webp" {
		return "", errors.New("video reference must be a PNG, JPEG, or WebP image")
	}
	if declared != "" {
		declared, _, _ = mime.ParseMediaType(declared)
		if declared != actual {
			return "", errors.New("video reference image Content-Type does not match its contents")
		}
	}
	return "data:" + actual + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func validateMuseImageDataURL(raw string) error {
	prefix, encoded, ok := strings.Cut(raw, ",")
	if !ok || (prefix != "data:image/png;base64" && prefix != "data:image/jpeg;base64" && prefix != "data:image/webp;base64") || len(encoded) > base64.StdEncoding.EncodedLen(museReferenceImageMaxBytes) {
		return errors.New("video reference must be a PNG/JPEG/WebP base64 data URL up to 10 MiB")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data) > museReferenceImageMaxBytes {
		return errors.New("invalid video reference image data")
	}
	_, err = museImageDataURL(data, strings.TrimSuffix(strings.TrimPrefix(prefix, "data:"), ";base64"))
	return err
}

var museTaskIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var museMediaFilePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+\.(mp4|webm|mov)$`)

func museBaseURL(account *Account, cfgValidator func(string) (string, error)) (*url.URL, error) {
	validated, err := cfgValidator(account.GetGrokMediaBaseURL())
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(validated)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/" && u.Path != "/v1" && u.Path != "/v1/") {
		return nil, errors.New("Muse base URL must be an origin with an optional /v1 path")
	}
	u.Path, u.RawPath = "", ""
	return u, nil
}

// museVideoContentURL accepts one exact media path on the configured origin.
// It never follows signed/external URLs or decodes path traversal into a request.
func museVideoContentURL(base *url.URL, raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || strings.Contains(raw, "%") || strings.Contains(raw, "\\") {
		return "", errors.New("Muse returned an unsupported video media URL")
	}
	if u.IsAbs() {
		if !strings.EqualFold(u.Scheme, base.Scheme) || !strings.EqualFold(u.Host, base.Host) {
			return "", errors.New("Muse video media URL must match the configured origin")
		}
	} else if u.Host != "" {
		return "", errors.New("Muse video media URL must match the configured origin")
	}
	filename := strings.TrimPrefix(u.Path, "/v1/media/")
	if filename == u.Path || !museMediaFilePattern.MatchString(filename) {
		return "", errors.New("Muse video media URL must identify a video in /v1/media")
	}
	return base.ResolveReference(&url.URL{Path: "/v1/media/" + filename}).String(), nil
}

func museMediaError(c *gin.Context, status int, code, message string) error {
	c.JSON(status, gin.H{"error": gin.H{"type": code, "code": code, "message": message}})
	return errors.New(message)
}

func (s *OpenAIGatewayService) forwardMuseVideo(ctx context.Context, c *gin.Context, account *Account, endpoint GrokMediaEndpoint, requestID string, body []byte, contentType string) (*OpenAIForwardResult, error) {
	started := time.Now()
	if endpoint != GrokMediaEndpointVideosGenerations && endpoint != GrokMediaEndpointVideoStatus && endpoint != GrokMediaEndpointVideoContent {
		return nil, museMediaError(c, http.StatusBadRequest, "invalid_request_error", "Muse supports video generation and retrieval; video edits/extensions are unavailable")
	}
	validator, err := grokBaseURLValidator(account, s.cfg)
	if err != nil {
		return nil, err
	}
	base, err := museBaseURL(account, validator)
	if err != nil {
		return nil, err
	}
	token, _, err := s.getRequestCredential(ctx, c, account)
	if err != nil {
		return nil, err
	}
	info := GrokMediaRequestInfo{Model: MuseVideoModel, Resolution: VideoBillingResolution480P}
	if endpoint.RequiresRequestBody() {
		info, body, err = ParseMuseVideoRequest(contentType, body)
		if err != nil {
			return nil, museMediaError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		}
	} else {
		if !museTaskIDPattern.MatchString(requestID) {
			return nil, museMediaError(c, http.StatusNotFound, "not_found_error", "Video request not found")
		}
		pending, _ := ctx.Value(museVideoPendingContextKey{}).(*GrokVideoPendingBilling)
		if pending == nil || pending.Model != MuseVideoModel || !validMuseVideoDuration(pending.VideoDurationSeconds) {
			return nil, museMediaError(c, http.StatusServiceUnavailable, "muse_video_metadata_unavailable", "Muse video task metadata is unavailable; retry later")
		}
		info.DurationSeconds = pending.VideoDurationSeconds
	}
	upstreamCtx, cancel := detachUpstreamContext(ctx)
	defer cancel()
	upstreamCtx = WithHTTPUpstreamRedirectsDisabled(upstreamCtx)
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	request := func(method, path string, data []byte) (*http.Response, error) {
		req, err := http.NewRequestWithContext(upstreamCtx, method, base.ResolveReference(&url.URL{Path: path}).String(), bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		if method == http.MethodPost {
			req.Header.Set("Content-Type", "application/json")
		}
		if endpoint == GrokMediaEndpointVideoContent && strings.HasPrefix(path, "/v1/media/") {
			req.Header.Set("Accept", "*/*")
			if rangeHeader := c.GetHeader("Range"); rangeHeader != "" {
				req.Header.Set("Range", rangeHeader)
			}
		}
		account.ApplyHeaderOverrides(req.Header)
		return s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
	}
	readJSON := func(resp *http.Response) ([]byte, error) {
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			// No retries/failover: Muse POST has no idempotency support.
			status := resp.StatusCode
			if status < 400 || status > 599 {
				status = http.StatusBadGateway
			}
			return nil, museMediaError(c, status, "muse_upstream_error", "Muse upstream rejected the request")
		}
		return ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	}
	if endpoint.RequiresRequestBody() {
		ready, err := request(http.MethodGet, "/readyz", nil)
		if err != nil {
			return nil, museMediaError(c, http.StatusServiceUnavailable, "muse_unavailable", "Muse readiness check failed")
		}
		readyBody, err := readJSON(ready)
		if err != nil {
			return nil, err
		}
		if gjson.GetBytes(readyBody, "status").String() != "ready" || gjson.GetBytes(readyBody, "accounts").Int() <= 0 {
			return nil, museMediaError(c, http.StatusServiceUnavailable, "muse_no_account", "Muse has no enabled account; import a Muse account before generating videos")
		}
	}
	path, method := "/v1/videos", http.MethodPost
	if endpoint.IsVideoLookupRequest() {
		path, method = "/v1/videos/"+requestID, http.MethodGet
	}
	resp, err := request(method, path, body)
	SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(started).Milliseconds())
	if err != nil {
		return nil, museMediaError(c, http.StatusBadGateway, "muse_upstream_error", "Muse upstream request failed; the request was not automatically retried")
	}
	respBody, err := readJSON(resp)
	if err != nil {
		return nil, err
	}
	id := firstNonEmpty(gjson.GetBytes(respBody, "id").String(), gjson.GetBytes(respBody, "task_id").String())
	if !museTaskIDPattern.MatchString(id) || (requestID != "" && id != requestID) {
		return nil, museMediaError(c, http.StatusBadGateway, "muse_invalid_response", "Muse returned an invalid video task ID")
	}
	if kind := gjson.GetBytes(respBody, "kind").String(); kind != "" && kind != "video" {
		return nil, museMediaError(c, http.StatusBadGateway, "muse_invalid_response", "Muse returned a non-video task")
	}
	state := ""
	switch gjson.GetBytes(respBody, "status").String() {
	case "queued", "pending", "processing", "in_progress":
		state = "pending"
	case "completed", "succeeded", "success", "done":
		state = "done"
	case "failed", "error", "cancelled", "canceled":
		state = "failed"
	default:
		return nil, museMediaError(c, http.StatusBadGateway, "muse_invalid_response", "Muse returned an unknown video task status")
	}
	contentURL := ""
	if state == "done" {
		rawURL := firstNonEmpty(gjson.GetBytes(respBody, "video.url").String(), gjson.GetBytes(respBody, "url").String(), gjson.GetBytes(respBody, "result.url").String())
		contentURL, err = museVideoContentURL(base, rawURL)
		if err != nil {
			return nil, museMediaError(c, http.StatusBadGateway, "muse_invalid_response", err.Error())
		}
	}
	result := &OpenAIForwardResult{
		RequestID: resp.Header.Get("x-request-id"), ResponseID: id, Model: MuseVideoModel,
		BillingModel: MuseVideoModel, UpstreamModel: MuseVideoModel, UpstreamHeaders: resp.Header,
		ResponseHeaders: resp.Header.Clone(), Duration: time.Since(started),
		VideoResolution: VideoBillingResolution480P, VideoDurationSeconds: info.DurationSeconds,
	}
	if state == "done" && endpoint.IsVideoLookupRequest() {
		result.VideoCount = 1
	}
	if endpoint == GrokMediaEndpointVideoContent {
		if state != "done" {
			return nil, museMediaError(c, http.StatusConflict, "video_not_ready", "Video content is not ready")
		}
		u, _ := url.Parse(contentURL)
		contentResp, err := request(http.MethodGet, u.Path, nil)
		if err != nil {
			return nil, museMediaError(c, http.StatusBadGateway, "muse_upstream_error", "Muse video download failed")
		}
		defer func() { _ = contentResp.Body.Close() }()
		if (contentResp.StatusCode >= 300 && contentResp.StatusCode < 400) || contentResp.StatusCode < 200 || (contentResp.StatusCode >= 400 && contentResp.StatusCode != http.StatusRequestedRangeNotSatisfiable) {
			return nil, museMediaError(c, http.StatusBadGateway, "muse_upstream_error", "Muse video download returned an unexpected status")
		}
		if contentResp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
			contentType, _, _ := mime.ParseMediaType(contentResp.Header.Get("Content-Type"))
			if contentType != "video/mp4" && contentType != "video/webm" && contentType != "video/quicktime" {
				return nil, museMediaError(c, http.StatusBadGateway, "muse_invalid_response", "Muse download did not return video content")
			}
		}
		if err := writeGrokMediaContentResponse(c, contentResp); err != nil {
			return nil, err
		}
		return result, nil
	}
	// Construct a public response from allowlisted fields so task/account internals
	// and every upstream media URL remain private.
	public := gin.H{"id": id, "task_id": id, "request_id": id, "object": "video", "model": MuseVideoModel,
		"status": state, "seconds": info.DurationSeconds, "requested_duration": info.DurationSeconds,
		"progress": gjson.GetBytes(respBody, "progress").Int()}
	if created := gjson.GetBytes(respBody, "created_at"); created.Exists() {
		public["created_at"] = created.Value()
	}
	if state == "done" {
		public["progress"] = 100
		public["video"] = gin.H{"url": grokMediaContentProxyURL(c, id), "duration": info.DurationSeconds}
		public["url"] = grokMediaContentProxyURL(c, id)
	} else if state == "failed" {
		// Upstream errors can contain account details or external URLs.
		public["error"] = gin.H{"code": "muse_generation_failed", "message": "Muse video generation failed"}
	}
	c.JSON(resp.StatusCode, public)
	return result, nil
}
