package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// MediaAsset maps to media.media_assets.
type MediaAsset struct {
	ID               uuid.UUID       `json:"id"`
	OwnerType        string          `json:"owner_type"`
	OwnerID          *uuid.UUID      `json:"owner_id,omitempty"`
	AssetType        string          `json:"asset_type"`
	Bucket           string          `json:"bucket"`
	ObjectPath       string          `json:"object_path"`
	CDNURL           *string         `json:"cdn_url,omitempty"`
	MimeType         string          `json:"mime_type"`
	SizeBytes        int64           `json:"size_bytes"`
	ProcessingStatus string          `json:"processing_status"`
	ModerationStatus string          `json:"moderation_status"`
	Metadata         json.RawMessage `json:"metadata,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}
