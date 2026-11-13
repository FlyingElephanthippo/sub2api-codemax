//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestParseMuseImagePreservesGeometryAndRejectsAsync(t *testing.T) {
	info, body, err := ParseMuseImageRequest("application/json", []byte(`{"model":"muse-image","prompt":"a sunrise","extra":"extra prompt instruction","size":"16:9","image":{"url":"https://images.example/ref.png"}}`))
	require.NoError(t, err)
	require.Equal(t, "16:9", info.Size)
	require.Equal(t, "16:9", gjson.GetBytes(body, "size").String())
	require.False(t, gjson.GetBytes(body, "image").Exists())
	require.Equal(t, "https://images.example/ref.png", gjson.GetBytes(body, "images.0").String())
	require.Contains(t, string(info.ModerationBody()), "https://images.example/ref.png")
	require.Contains(t, string(info.ModerationBody()), "extra prompt instruction")
	require.Equal(t, "a sunrise", gjson.GetBytes(body, "prompt").String())
	for _, fields := range []string{`,"async":true`, `,"async":"true"`, `,"n":2`, `,"mask":"https://images.example/mask.png"`} {
		_, _, err := ParseMuseImageRequest("application/json", []byte(`{"model":"muse-image","prompt":"x"`+fields+`}`))
		require.Error(t, err, fields)
	}
}

func TestParseMuseImageMultipartKeepsOneReferenceRepresentation(t *testing.T) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	require.NoError(t, w.WriteField("model", "muse-image"))
	require.NoError(t, w.WriteField("prompt", "edit these images"))
	require.NoError(t, w.WriteField("size", "9:16"))
	pngData := museTestPNG(t)
	for range 2 {
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", `form-data; name="image[]"; filename="ref.png"`)
		header.Set("Content-Type", "image/png")
		part, err := w.CreatePart(header)
		require.NoError(t, err)
		_, err = part.Write(pngData)
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	info, body, err := ParseMuseImageRequest(w.FormDataContentType(), buf.Bytes())
	require.NoError(t, err)
	require.Len(t, info.InputImageURLs, 2)
	require.Len(t, gjson.GetBytes(body, "images").Array(), 2)
	require.False(t, gjson.GetBytes(body, "image").Exists())
	require.Equal(t, "9:16", gjson.GetBytes(body, "size").String())
}

func TestForwardMuseImageBypassesGrokGeometryAndDoesNotRetry(t *testing.T) {
	for _, success := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty image", true: "success"}[success], func(t *testing.T) {
			response := `{"id":"task_created","status":"queued"}`
			if success {
				response = `{"created":1,"data":[{"url":"https://relay.example/v1/media/image.png"}]}`
			}
			upstream := &grokMediaContentUpstreamStub{response: grokMediaContentStatusResponse(response)}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			c, rec := grokMediaContentTestContext(http.MethodPost, "https://api.example/v1/images/generations", nil)
			result, err := svc.ForwardGrokMedia(context.Background(), c, museTestAccount(), GrokMediaEndpointImagesGenerations,
				"", []byte(`{"model":"muse-image","prompt":"a sunrise","size":"16:9"}`), "application/json")
			require.Len(t, upstream.requests, 1)
			require.Equal(t, "https://relay.example/v1/images/generations", upstream.request.URL.String())
			require.True(t, HTTPUpstreamRedirectsDisabled(upstream.request.Context()))
			body, readErr := io.ReadAll(upstream.request.Body)
			require.NoError(t, readErr)
			require.Equal(t, "16:9", gjson.GetBytes(body, "size").String())
			require.False(t, gjson.GetBytes(body, "resolution").Exists())
			if success {
				require.NoError(t, err)
				require.Equal(t, 200, rec.Code)
				require.Equal(t, 1, result.ImageCount)
				require.Equal(t, "muse-image", result.BillingModel)
			} else {
				require.Error(t, err)
				require.Equal(t, 502, rec.Code)
				require.NotContains(t, rec.Body.String(), "task_created")
			}
		})
	}
	upstream := &grokMediaContentUpstreamStub{}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	c, rec := grokMediaContentTestContext(http.MethodPost, "https://api.example/v1/images/generations", nil)
	_, err := svc.ForwardGrokMedia(context.Background(), c, museTestAccount(), GrokMediaEndpointImagesGenerations,
		"", []byte(`{"model":"muse-image","prompt":"a sunrise","async":true}`), "application/json")
	require.Error(t, err)
	require.Equal(t, 400, rec.Code)
	require.Empty(t, upstream.requests)
	require.False(t, strings.Contains(rec.Body.String(), "upstream"))
}
