package repository

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"myapp/internal/models"
)

type MediaRepository struct {
	db *pgxpool.Pool
}

func NewMediaRepository(db *pgxpool.Pool) *MediaRepository {
	return &MediaRepository{db: db}
}

func (r *MediaRepository) Create(ctx context.Context, asset *models.MediaAsset) error {
	if asset.OwnerType == "" {
		asset.OwnerType = "system"
	}
	if asset.AssetType == "" {
		asset.AssetType = "document"
	}
	if asset.ProcessingStatus == "" {
		asset.ProcessingStatus = "uploaded"
	}
	if asset.ModerationStatus == "" {
		asset.ModerationStatus = "pending"
	}
	if len(asset.Metadata) == 0 {
		asset.Metadata = []byte(`{}`)
	}
	return r.db.QueryRow(ctx, `
		insert into media.media_assets (
			owner_type, owner_id, asset_type, bucket, object_path, cdn_url,
			mime_type, size_bytes, processing_status, moderation_status, metadata
		) values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		returning id, created_at, updated_at`,
		asset.OwnerType, asset.OwnerID, asset.AssetType, asset.Bucket, asset.ObjectPath, asset.CDNURL,
		asset.MimeType, asset.SizeBytes, asset.ProcessingStatus, asset.ModerationStatus, asset.Metadata,
	).Scan(&asset.ID, &asset.CreatedAt, &asset.UpdatedAt)
}
