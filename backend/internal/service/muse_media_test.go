//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func museTestAccount() *Account {
	account := grokMediaContentTestAccount()
	account.Credentials["media_provider"] = "muse"
	return account
}

func museTestLookupContext() context.Context {
	return WithMuseVideoPendingBilling(context.Background(), &GrokVideoPendingBilling{
		Model: MuseVideoModel, BillingModel: MuseVideoModel, VideoDurationSeconds: 8,
		VideoResolution: VideoBillingResolution480P,
	})
}

func TestParseMuseVideoRequestDuration(t *testing.T) {
	for _, tc := range []struct {
		fields string
		want   int
	}{
		{``, 6}, {`,"seconds":"8"`, 8}, {`,"duration":10`, 10},
		{`,"duration":5,"seconds":5`, 5}, {`,"duration":8,"seconds":"10"`, 0},
		{`,"seconds":0`, 0}, {`,"seconds":-1`, 0}, {`,"seconds":15`, 0},
		{`,"seconds":6.5`, 0}, {`,"seconds":null`, 0}, {`,"seconds":true`, 0},
		{`,"duration":"6.0"`, 0}, {`,"n":2`, 0}, {`,"resolution":"1080p"`, 0},
	} {
		t.Run(tc.fields, func(t *testing.T) {
			info, body, err := ParseMuseVideoRequest("application/json", []byte(`{"model":"muse-video","prompt":"a sunrise"`+tc.fields+`}`))
			if tc.want == 0 {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, info.DurationSeconds)
			require.Equal(t, int64(tc.want), gjson.GetBytes(body, "duration").Int())
			require.False(t, gjson.GetBytes(body, "seconds").Exists())
			require.Equal(t, MuseVideoModel, info.Model)
		})
	}
	for _, invalid := range []string{`[]`, `null`, `{}`, `{"model":"sora-2","prompt":"x"}`, `{"model":"muse-video","prompt":false}`, `{"model":"muse-video","prompt":" "}`, `{"model":"muse-video","prompt":"x"} {}`, `{"model":"muse-video","prompt":"x","seconds":8,"seconds":10}`} {
		_, _, err := ParseMuseVideoRequest("application/json", []byte(invalid))
		require.Error(t, err, invalid)
	}
	info, body, err := ParseMuseVideoRequest("application/json", []byte(`{"model":"muse-video","prompt":"a sunrise","extra":"extra prompt instruction"}`))
	require.NoError(t, err)
	require.Contains(t, string(info.ModerationBody()), "extra prompt instruction")
	require.Equal(t, "a sunrise", gjson.GetBytes(body, "prompt").String())
}

func museTestPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 2, 2))))
	return buf.Bytes()
}

func museTestMultipart(t *testing.T, data []byte, declared string, files int) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	require.NoError(t, w.WriteField("model", MuseVideoModel))
	require.NoError(t, w.WriteField("prompt", "animate this image"))
	require.NoError(t, w.WriteField("seconds", "8"))
	for range files {
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", `form-data; name="input_reference"; filename="image.png"`)
		header.Set("Content-Type", declared)
		part, err := w.CreatePart(header)
		require.NoError(t, err)
		_, err = part.Write(data)
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	return buf.Bytes(), w.FormDataContentType()
}

func TestParseMuseVideoMultipartValidatesReference(t *testing.T) {
	pngData := museTestPNG(t)
	body, contentType := museTestMultipart(t, pngData, "image/png", 1)
	info, forward, err := ParseMuseVideoRequest(contentType, body)
	require.NoError(t, err)
	require.Equal(t, 8, info.DurationSeconds)
	require.Len(t, info.InputImageURLs, 1)
	wantImage := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngData)
	require.Equal(t, wantImage, gjson.GetBytes(forward, "image").String())
	require.Contains(t, string(info.ModerationBody()), "data:image/png;base64,")

	for _, tc := range []struct {
		name     string
		data     []byte
		declared string
		files    int
	}{
		{"wrong MIME", pngData, "image/jpeg", 1},
		{"nonimage", []byte("not an image"), "image/png", 1},
		{"too many", pngData, "image/png", 2},
		{"too large", make([]byte, museReferenceImageMaxBytes+1), "image/png", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, contentType := museTestMultipart(t, tc.data, tc.declared, tc.files)
			_, _, err := ParseMuseVideoRequest(contentType, body)
			require.Error(t, err)
		})
	}
}

func TestForwardMuseVideoPreflightsReadinessAndConvertsCreate(t *testing.T) {
	for _, ready := range []bool{false, true} {
		t.Run(fmt.Sprint(ready), func(t *testing.T) {
			readiness := `{"status":"no_account","accounts":0,"browser_running":false}`
			if ready {
				readiness = `{"status":"ready","accounts":1,"browser_running":false}`
			}
			upstream := &grokMediaContentUpstreamStub{responses: []*http.Response{
				grokMediaContentStatusResponse(readiness),
				grokMediaContentStatusResponse(`{"id":"task_123","status":"queued","progress":10,"created_at":"today"}`),
			}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			c, rec := grokMediaContentTestContext(http.MethodPost, "https://api.example/v1/videos", nil)
			result, err := svc.ForwardGrokMedia(context.Background(), c, museTestAccount(), GrokMediaEndpointVideosGenerations,
				"", []byte(`{"model":"muse-video","prompt":"a sunrise","seconds":"8"}`), "application/json")
			if !ready {
				require.Error(t, err)
				require.Equal(t, 503, rec.Code)
				require.Contains(t, rec.Body.String(), "muse_no_account")
				require.Len(t, upstream.requests, 1)
				return
			}
			require.NoError(t, err)
			require.Len(t, upstream.requests, 2)
			require.Equal(t, "https://relay.example/readyz", upstream.requests[0].URL.String())
			require.Equal(t, "https://relay.example/v1/videos", upstream.requests[1].URL.String())
			body, err := io.ReadAll(upstream.requests[1].Body)
			require.NoError(t, err)
			require.Equal(t, int64(8), gjson.GetBytes(body, "duration").Int())
			require.False(t, gjson.GetBytes(body, "seconds").Exists())
			for _, req := range upstream.requests {
				require.Equal(t, "Bearer upstream-key", req.Header.Get("Authorization"))
				require.True(t, HTTPUpstreamRedirectsDisabled(req.Context()))
			}
			require.Equal(t, "pending", gjson.Get(rec.Body.String(), "status").String())
			require.Equal(t, "muse-video", result.BillingModel)
			require.Equal(t, 8, result.VideoDurationSeconds)
			require.Zero(t, result.VideoCount)
		})
	}
}

func TestForwardMuseVideoNormalizesStatesAndHidesUpstreamDetails(t *testing.T) {
	for _, tc := range []struct {
		state, want string
		count       int
	}{
		{"queued", "pending", 0}, {"processing", "pending", 0},
		{"completed", "done", 1}, {"succeeded", "done", 1}, {"failed", "failed", 0},
	} {
		t.Run(tc.state, func(t *testing.T) {
			body := fmt.Sprintf(`{"id":"task_123","kind":"video","status":%q,"progress":45,"account":"secret-account","error":"cookie secret at https://private.example/","video":{"url":"https://relay.example/v1/media/output.mp4"},"result":{"url":"https://relay.example/v1/media/output.mp4"}}`, tc.state)
			upstream := &grokMediaContentUpstreamStub{response: grokMediaContentStatusResponse(body)}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			c, rec := grokMediaContentTestContext(http.MethodGet, "https://api.example/v1/videos/task_123", nil)
			result, err := svc.ForwardGrokMedia(museTestLookupContext(), c, museTestAccount(), GrokMediaEndpointVideoStatus, "task_123", nil, "")
			require.NoError(t, err)
			require.Equal(t, tc.want, gjson.Get(rec.Body.String(), "status").String())
			require.Equal(t, int64(8), gjson.Get(rec.Body.String(), "seconds").Int())
			require.Equal(t, tc.count, result.VideoCount)
			require.NotContains(t, rec.Body.String(), "relay.example")
			require.NotContains(t, rec.Body.String(), "secret")
			require.NotContains(t, rec.Body.String(), "private.example")
			if tc.count > 0 {
				require.Equal(t, "/v1/videos/task_123/content", gjson.Get(rec.Body.String(), "video.url").String())
				require.True(t, IsGrokVideoStatusBillable(rec.Body.Bytes()))
			}
		})
	}
}

func TestMuseVideoMediaURLBoundary(t *testing.T) {
	base, err := url.Parse("https://relay.example")
	require.NoError(t, err)
	for _, raw := range []string{
		"https://evil.example/v1/media/x.mp4", "//evil.example/v1/media/x.mp4", "https://relay.example:444/v1/media/x.mp4",
		"https://user:pass@relay.example/v1/media/x.mp4", "/v1/media/../x.mp4", "/v1/media/%2e%2e%2fx.mp4",
		"/v1/media/%252e%252e%252fx.mp4", "/v1/media/a/b.mp4", "/v1/media/x.mp4?token=secret",
		"/v1/media/x.mp4#fragment", "/v1/media/x.html", "/v1/media/x.mp4\\evil", "v1/media/x.mp4",
	} {
		_, err := museVideoContentURL(base, raw)
		require.Error(t, err, raw)
	}
	for _, raw := range []string{"/v1/media/x_123.mp4", "https://relay.example/v1/media/x_123.mp4"} {
		got, err := museVideoContentURL(base, raw)
		require.NoError(t, err)
		require.Equal(t, "https://relay.example/v1/media/x_123.mp4", got)
	}
}

func TestForwardMuseVideoDownloadStreamsRange(t *testing.T) {
	upstream := &grokMediaContentUpstreamStub{responses: []*http.Response{
		grokMediaContentStatusResponse(`{"id":"task_123","status":"completed","video":{"url":"/v1/media/output.mp4"}}`),
		{StatusCode: http.StatusPartialContent, Header: http.Header{
			"Content-Type": {"video/mp4"}, "Content-Range": {"bytes 0-4/10"}, "Content-Length": {"5"},
			"Set-Cookie": {"secret-cookie"},
		}, Body: io.NopCloser(strings.NewReader("video"))},
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	c, rec := grokMediaContentTestContext(http.MethodGet, "https://api.example/v1/videos/task_123/content", map[string]string{"Range": "bytes=0-4"})
	result, err := svc.ForwardGrokMedia(museTestLookupContext(), c, museTestAccount(), GrokMediaEndpointVideoContent, "task_123", nil, "")
	require.NoError(t, err)
	require.Equal(t, 206, rec.Code)
	require.Equal(t, "video", rec.Body.String())
	require.Equal(t, "bytes 0-4/10", rec.Header().Get("Content-Range"))
	require.Empty(t, rec.Header().Get("Set-Cookie"))
	require.Len(t, upstream.requests, 2)
	require.Equal(t, "https://relay.example/v1/media/output.mp4", upstream.requests[1].URL.String())
	require.Equal(t, "bytes=0-4", upstream.requests[1].Header.Get("Range"))
	for _, req := range upstream.requests {
		require.True(t, HTTPUpstreamRedirectsDisabled(req.Context()))
		require.Equal(t, "Bearer upstream-key", req.Header.Get("Authorization"))
	}
	require.Equal(t, 1, result.VideoCount)
	require.Equal(t, 8, result.VideoDurationSeconds)
}

func TestForwardMuseVideoFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		content      *http.Response
		wantStatus   int
		wantRequests int
	}{
		{"pending content", `{"id":"task_123","status":"processing"}`, nil, 409, 1},
		{"external media", `{"id":"task_123","status":"completed","url":"https://evil.example/v1/media/x.mp4"}`, nil, 502, 1},
		{"wrong ID", `{"id":"other","status":"processing"}`, nil, 502, 1},
		{"nonvideo task", `{"id":"task_123","kind":"image","status":"processing"}`, nil, 502, 1},
		{"unknown state", `{"id":"task_123","status":"unknown"}`, nil, 502, 1},
		{"redirect", `{"id":"task_123","status":"completed","url":"/v1/media/x.mp4"}`, &http.Response{StatusCode: 302, Header: http.Header{"Location": {"https://evil.example"}}, Body: io.NopCloser(strings.NewReader(""))}, 502, 2},
		{"wrong MIME", `{"id":"task_123","status":"completed","url":"/v1/media/x.mp4"}`, &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/html"}}, Body: io.NopCloser(strings.NewReader("<html>"))}, 502, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			responses := []*http.Response{grokMediaContentStatusResponse(tc.status)}
			if tc.content != nil {
				responses = append(responses, tc.content)
			}
			upstream := &grokMediaContentUpstreamStub{responses: responses}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			c, rec := grokMediaContentTestContext(http.MethodGet, "https://api.example/v1/videos/task_123/content", nil)
			_, err := svc.ForwardGrokMedia(museTestLookupContext(), c, museTestAccount(), GrokMediaEndpointVideoContent, "task_123", nil, "")
			require.Error(t, err)
			require.Equal(t, tc.wantStatus, rec.Code)
			require.Len(t, upstream.requests, tc.wantRequests)
		})
	}
	upstream := &grokMediaContentUpstreamStub{}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	c, rec := grokMediaContentTestContext(http.MethodGet, "https://api.example/v1/videos/task_123", nil)
	_, err := svc.ForwardGrokMedia(context.Background(), c, museTestAccount(), GrokMediaEndpointVideoStatus, "task_123", nil, "")
	require.Error(t, err)
	require.Equal(t, 503, rec.Code)
	require.Empty(t, upstream.requests)
}

func TestMuseVideoPricingNeverInheritsGrokDefaults(t *testing.T) {
	price := 1.0
	for _, invalid := range []*APIKey{nil, {}, {Group: &Group{}}, {Group: &Group{VideoPrice480P: &price}},
		{Group: &Group{VideoModelPrices: map[string]map[string]float64{"grok-imagine-video": {"480p": 1}}}},
		{Group: &Group{VideoModelPrices: map[string]map[string]float64{MuseVideoModel: {"480p": math.NaN()}}}},
	} {
		err := ValidateMuseVideoPricing(invalid)
		require.Error(t, err)
		require.False(t, isUsagePricingUnavailableError(err), "unpriced Muse jobs must not silently record zero token cost")
	}
	svc := &OpenAIGatewayService{billingService: NewBillingService(&config.Config{}, nil)}
	for _, price := range []float64{0, 0.2} {
		key := &APIKey{Group: &Group{VideoModelPrices: map[string]map[string]float64{MuseVideoModel: {"480p": price}}}}
		require.NoError(t, ValidateMuseVideoPricing(key))
		cost, err := svc.calculateOpenAIRecordUsageCost(context.Background(), &OpenAIForwardResult{
			Model: MuseVideoModel, BillingModel: MuseVideoModel, VideoCount: 1, VideoDurationSeconds: 8,
		}, key, []string{MuseVideoModel}, 1, 1, 2, 1, UsageTokens{}, "", nil, false, time.Now())
		require.NoError(t, err)
		require.InDelta(t, price*8*2, cost.ActualCost, 0.00001)
	}
}
