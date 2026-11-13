package service

import "testing"

func TestImageGenerationHistoryContentAvailability(t *testing.T) {
	tests := []struct {
		name   string
		record *ImageGenerationHistoryRecord
		want   bool
	}{
		{name: "missing record"},
		{name: "missing bytes despite stale size", record: &ImageGenerationHistoryRecord{SizeBytes: 128}},
		{name: "list without loaded bytes", record: &ImageGenerationHistoryRecord{StoredContentAvailable: true}, want: true},
		{name: "inline image", record: &ImageGenerationHistoryRecord{Content: []byte("png")}, want: true},
		{name: "external image", record: &ImageGenerationHistoryRecord{ContentURL: "https://example.com/image.png"}, want: true},
		{name: "archived image", record: &ImageGenerationHistoryRecord{Archived: true, StoredContentAvailable: true, Content: []byte("png"), ContentURL: "https://example.com/image.png"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.record.ContentAvailable(); got != tt.want {
				t.Fatalf("ContentAvailable() = %v, want %v", got, tt.want)
			}
		})
	}
}
