//go:build unit

package handler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestMuseVideoHandlerCreateFailsClosedAndPersistsMetadata(t *testing.T) {
	for _, scenario := range []string{"missing price", "no account", "accepted", "transport failure"} {
		t.Run(scenario, func(t *testing.T) {
			h, slots, bindings, upstream := newGrokMediaSlotHandler(t, false, false, "muse")
			c, rec := grokMediaSlotContext(context.Background(), true)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader(`{"model":"muse-video","prompt":"test","seconds":"8"}`))
			c.Request.Header.Set("Content-Type", "application/json")
			key, ok := middleware2.GetAPIKeyFromContext(c)
			require.True(t, ok)
			if scenario != "missing price" {
				key.Group.VideoModelPrices = map[string]map[string]float64{"muse-video": {"480p": 0}}
			}
			upstream.call = func(req *http.Request, _ int64) (*http.Response, error) {
				body := `{"status":"ready","accounts":1}`
				if req.URL.Path == "/readyz" && scenario == "no account" {
					body = `{"status":"no_account","accounts":0}`
				} else if req.Method == http.MethodPost {
					if scenario == "transport failure" {
						return nil, errors.New("connection lost")
					}
					data, err := io.ReadAll(req.Body)
					require.NoError(t, err)
					require.Equal(t, int64(8), gjson.GetBytes(data, "duration").Int())
					body = `{"id":"task_created","status":"queued","progress":10}`
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			}
			h.GrokVideoGeneration(c)
			slots.assertReleased(t)
			switch scenario {
			case "missing price":
				require.Equal(t, 503, rec.Code, rec.Body.String())
				require.Zero(t, upstream.calls)
			case "no account":
				require.Equal(t, 503, rec.Code, rec.Body.String())
				require.Equal(t, 1, upstream.calls)
			case "transport failure":
				require.Equal(t, 502, rec.Code, rec.Body.String())
				require.Equal(t, 2, upstream.calls, "readiness plus one POST; non-idempotent creates must not fail over")
			case "accepted":
				require.Equal(t, 200, rec.Code, rec.Body.String())
				require.Equal(t, 2, upstream.calls)
				require.Equal(t, "pending", gjson.Get(rec.Body.String(), "status").String())
				pending, err := h.gatewayService.LoadGrokVideoPendingBilling(context.Background(), "task_created", 10, 20)
				require.NoError(t, err)
				require.NotNil(t, pending)
				require.Equal(t, "muse-video", pending.Model)
				require.Equal(t, 8, pending.VideoDurationSeconds)
				require.Empty(t, bindings.billed, "accepted jobs are billed only on completion")
			}
		})
	}
}

func TestMuseVideoHandlerLookupRequiresOwnedMetadata(t *testing.T) {
	for _, hasPending := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing metadata", true: "pending task"}[hasPending], func(t *testing.T) {
			h, slots, _, upstream := newGrokMediaSlotHandler(t, false, false, "muse")
			c, rec := grokMediaSlotContext(context.Background(), false)
			key, ok := middleware2.GetAPIKeyFromContext(c)
			require.True(t, ok)
			key.Group.VideoModelPrices = map[string]map[string]float64{"muse-video": {"480p": 0}}
			if hasPending {
				require.NoError(t, h.gatewayService.StoreGrokVideoPendingBilling(context.Background(), "task", 10, 20,
					service.GrokVideoPendingBilling{Model: service.MuseVideoModel, VideoDurationSeconds: 8}))
			}
			upstream.call = func(*http.Request, int64) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}},
					Body: io.NopCloser(strings.NewReader(`{"id":"task","status":"processing","progress":15}`))}, nil
			}
			h.GrokVideoStatus(c)
			slots.assertReleased(t)
			if hasPending {
				require.Equal(t, 200, rec.Code, rec.Body.String())
				require.Equal(t, int64(8), gjson.Get(rec.Body.String(), "seconds").Int())
				require.Equal(t, 1, upstream.calls)
			} else {
				require.Equal(t, 503, rec.Code, rec.Body.String())
				require.Zero(t, upstream.calls)
			}
		})
	}
}
