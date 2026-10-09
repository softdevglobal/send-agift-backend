package repository

import (
	"context"

	"github.com/google/uuid"
)

// GiftReviewItem is one line of a delivered gift as the review page shows it.
type GiftReviewItem struct {
	ID               uuid.UUID  `json:"id"`
	ProductID        uuid.UUID  `json:"product_id"`
	ProductName      string     `json:"product_name"`
	ProductImageURL  *string    `json:"product_image_url,omitempty"`
	ShopName         string     `json:"shop_name"`
	Quantity         int        `json:"quantity"`
	FulfilmentStatus string     `json:"fulfilment_status"`
	ReviewID         *uuid.UUID `json:"review_id,omitempty"`
}

// GiftReviewItems lists the lines of an order that can be reviewed.
func (r *OrderRepository) GiftReviewItems(ctx context.Context, orderID uuid.UUID) ([]GiftReviewItem, error) {
	rows, err := r.db.Query(ctx, `
		select oi.id, oi.product_id, p.name, p.image_url, shp.name, oi.quantity,
		       oi.fulfilment_status, pr.id
		from marketplace.order_items oi
		inner join seller.products p on p.id = oi.product_id
		inner join seller.shops shp on shp.id = oi.shop_id
		left join marketplace.product_reviews pr on pr.order_item_id = oi.id
		where oi.order_id = $1 and oi.fulfilment_status <> 'cancelled'
		order by oi.created_at`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GiftReviewItem{}
	for rows.Next() {
		var it GiftReviewItem
		if err := rows.Scan(&it.ID, &it.ProductID, &it.ProductName, &it.ProductImageURL, &it.ShopName,
			&it.Quantity, &it.FulfilmentStatus, &it.ReviewID); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}
