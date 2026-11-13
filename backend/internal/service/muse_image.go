package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// ParseMuseImageRequest retains Muse geometry and sends exactly one reference
// representation. Grok's image+images and resolution transforms do not apply.
func ParseMuseImageRequest(contentType string, body []byte) (GrokMediaRequestInfo, []byte, error) {
	info := GrokMediaRequestInfo{Model: "muse-image", N: 1}
	fields := map[string]any{}
	references := []string{}
	totalBytes := 0
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return info, nil, errors.New("invalid image Content-Type")
	}
	if mediaType == "application/json" {
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.UseNumber()
		if err := decoder.Decode(&fields); err != nil || fields == nil {
			return info, nil, errors.New("image body must be a JSON object")
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return info, nil, errors.New("image body must contain one JSON object")
		}
	} else if mediaType == "multipart/form-data" && params["boundary"] != "" {
		reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return info, nil, errors.New("invalid image multipart body")
			}
			if part.FileName() != "" {
				name := part.FormName()
				if name != "image" && name != "image[]" && name != "images" && !strings.HasPrefix(name, "image[") {
					_ = part.Close()
					return info, nil, errors.New("Muse image edits support reference images only; masks are unavailable")
				}
				data, readErr := io.ReadAll(io.LimitReader(part, museReferenceImageMaxBytes+1))
				_ = part.Close()
				if readErr != nil || len(data) > museReferenceImageMaxBytes {
					return info, nil, errors.New("Muse reference image exceeds 10 MiB")
				}
				totalBytes += len(data)
				dataURL, err := museImageDataURL(data, part.Header.Get("Content-Type"))
				if err != nil {
					return info, nil, err
				}
				references = append(references, dataURL)
			} else {
				data, readErr := io.ReadAll(io.LimitReader(part, 65537))
				_ = part.Close()
				if readErr != nil || len(data) > 65536 {
					return info, nil, errors.New("image form field exceeds 64 KiB")
				}
				name := part.FormName()
				if _, exists := fields[name]; exists {
					return info, nil, errors.New("duplicate image form field")
				}
				fields[name] = string(data)
			}
		}
	} else {
		return info, nil, errors.New("image request requires JSON or multipart/form-data")
	}
	if fields["model"] != "muse-image" {
		return info, nil, errors.New("model must be muse-image")
	}
	if async, exists := fields["async"]; exists && async != false && async != "false" && async != "0" {
		return info, nil, errors.New("Muse image requests must be synchronous; async is unavailable")
	}
	if n, exists, err := museIntegerField(fields, "n"); err != nil || (exists && n != 1) {
		return info, nil, errors.New("Muse image supports n=1 only")
	}
	if _, exists := fields["mask"]; exists {
		return info, nil, errors.New("Muse image masks are unavailable")
	}
	payload := map[string]any{"model": "muse-image", "async": false}
	for _, name := range []string{"prompt", "size", "aspect_ratio", "response_format", "extra"} {
		if value, exists := fields[name]; exists {
			text, ok := value.(string)
			if !ok {
				return info, nil, errors.New("Muse image text fields must be strings")
			}
			payload[name] = text
		}
	}
	if timeout, exists, err := museIntegerField(fields, "timeout"); err != nil {
		return info, nil, err
	} else if exists {
		if timeout < 1 || timeout > 600 {
			return info, nil, errors.New("Muse image timeout must be between 1 and 600 seconds")
		}
		payload["timeout"] = timeout
	}
	for _, name := range []string{"image", "images", "image_url", "reference_image", "reference_images"} {
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
				return info, nil, errors.New("invalid Muse image reference")
			}
			if strings.HasPrefix(reference, "data:") {
				if err := validateMuseImageDataURL(reference); err != nil {
					return info, nil, err
				}
				totalBytes += len(reference) * 3 / 4
			} else {
				u, err := url.Parse(reference)
				if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
					return info, nil, errors.New("Muse image references require HTTPS or image data URLs")
				}
			}
			references = append(references, reference)
		}
	}
	if len(references) > 4 || totalBytes > 24<<20 {
		return info, nil, errors.New("Muse supports at most four reference images totaling 24 MiB")
	}
	if len(references) > 0 {
		payload["images"] = references
	}
	forwardBody, err := json.Marshal(payload)
	if err != nil {
		return info, nil, err
	}
	info = ParseGrokMediaRequest("application/json", forwardBody)
	if extra, _ := payload["extra"].(string); strings.TrimSpace(extra) != "" {
		info.Prompt += "\n" + extra
	}
	return info, forwardBody, nil
}

func (s *OpenAIGatewayService) forwardMuseImage(ctx context.Context, c *gin.Context, account *Account, endpoint GrokMediaEndpoint, body []byte, contentType string) (*OpenAIForwardResult, error) {
	started := time.Now()
	info, body, err := ParseMuseImageRequest(contentType, body)
	if err != nil {
		return nil, museMediaError(c, 400, "invalid_request_error", err.Error())
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
	path := "/v1/images/generations"
	if endpoint == GrokMediaEndpointImagesEdits {
		path = "/v1/images/edits"
	}
	upstreamCtx, cancel := detachUpstreamContext(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(WithHTTPUpstreamRedirectsDisabled(upstreamCtx), http.MethodPost,
		base.ResolveReference(&url.URL{Path: path}).String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	account.ApplyHeaderOverrides(req.Header)
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
	SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(started).Milliseconds())
	if err != nil {
		return nil, museMediaError(c, 502, "muse_upstream_error", "Muse image request failed; the request was not automatically retried")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		status := resp.StatusCode
		if status < 400 || status > 599 {
			status = http.StatusBadGateway
		}
		return nil, museMediaError(c, status, "muse_upstream_error", "Muse image upstream rejected the request")
	}
	responseBody, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	if err != nil {
		return nil, err
	}
	usage := grokMediaUsageFromResponse(endpoint, info, responseBody)
	if usage.ImageCount <= 0 {
		return nil, museMediaError(c, 502, "muse_invalid_response", "Muse returned no image output")
	}
	writeGrokMediaResponse(c, resp, responseBody, s.responseHeaderFilter)
	return &OpenAIForwardResult{
		RequestID: resp.Header.Get("x-request-id"), Model: "muse-image", BillingModel: "muse-image", UpstreamModel: "muse-image",
		UpstreamHeaders: resp.Header, ResponseHeaders: resp.Header.Clone(), Duration: time.Since(started), Usage: usage.Usage,
		ImageCount: usage.ImageCount, ImageSize: usage.ImageSize, ImageInputSize: usage.ImageInputSize, ImageOutputSizes: usage.ImageOutputSizes,
	}, nil
}
