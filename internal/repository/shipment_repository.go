package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"myapp/internal/models"
)

var (
	ErrShipmentNotFound = errors.New("shipment not found")
	// ErrShipmentLabelNotFound means the shipment exists but no label has been
	// bought for it yet, so there is no PDF to hand back.
	ErrShipmentLabelNotFound = errors.New("shipment label not found")
)

// ShipmentLabel is the stored label PDF for one order item.
type ShipmentLabel struct {
	ObjectPath     string
	MimeType       string
	TrackingNumber *string
	Provider       *string
}

// ShipmentRepository persists shipping quotes/labels on marketplace.shipments.
type ShipmentRepository struct {
	db *pgxpool.Pool
}

func NewShipmentRepository(db *pgxpool.Pool) *ShipmentRepository {
	return &ShipmentRepository{db: db}
}

// ShippingContext holds everything needed to call Shippo for one order item:
// fulfilment status, seller ship-from, recipient ship-to, and any pending
// parcel/customs already stored on marketplace.shipments.
type ShippingContext struct {
	OrderID          uuid.UUID
	OrderItemID      uuid.UUID
	SellerID         uuid.UUID
	FulfilmentStatus string
	FromName         string
	FromEmail        string
	FromPhone        string
	FromStreet1      string
	FromStreet2      string
	FromCity         string
	FromRegion       string
	FromPostalCode   string
	FromCountryISO   string
	ToName           string
	ToEmail          string
	ToPhone          string
	ToStreet1        string
	ToStreet2        string
	ToCity           string
	ToRegion         string
	ToPostalCode     string
	ToCountryISO     string
	StoredParcel     json.RawMessage // marketplace.shipments.parcel_details (pending)
	StoredCustoms    json.RawMessage // marketplace.shipments.customs_declaration (pending)
}

// GetShippingContext loads ship-from (shop address), ship-to (recipient address),
// and any pending shipment parcel/customs for the seller's order item.
func (r *ShipmentRepository) GetShippingContext(ctx context.Context, sellerID, orderItemID string) (*ShippingContext, error) {
	sc := &ShippingContext{}
	err := r.db.QueryRow(ctx, `
		select
			o.id, oi.id, oi.seller_id, oi.fulfilment_status,
			coalesce(se.trading_name, s.name, se.legal_name), se.email, coalesce(se.phone, ''),
			sa.line1, coalesce(sa.line2, ''), sa.city, coalesce(sa.region, ''), coalesce(sa.postal_code, ''),
			coalesce(fc.iso_code, ''),
			coalesce(r.name, ''), coalesce(r.email::text, ''), coalesce(r.phone, ''),
			coalesce(ra.line1, ''), coalesce(ra.line2, ''), coalesce(ra.city, ''), coalesce(ra.region, ''), coalesce(ra.postal_code, ''),
			coalesce(tc.iso_code, ''),
			sh.parcel_details, sh.customs_declaration
		from marketplace.order_items oi
		inner join marketplace.orders o on o.id = oi.order_id
		inner join seller.sellers se on se.id = oi.seller_id
		inner join seller.shops s on s.id = oi.shop_id
		left join marketplace.shipments sh on sh.order_item_id = oi.id and sh.status = 'pending'
		left join seller.seller_addresses sa on sa.id = coalesce(s.return_address_id, s.address_id)
		left join customer.recipients r on r.id = o.recipient_id
		left join customer.recipient_addresses ra on ra.id = coalesce(
			r.default_address_id,
			(select id from customer.recipient_addresses where recipient_id = r.id order by is_default desc, created_at asc limit 1)
		)
		left join core.countries fc on fc.id = sa.country_id
		left join core.countries tc on tc.id = ra.country_id
		where oi.id = $1 and oi.seller_id = $2`,
		orderItemID, sellerID,
	).Scan(
		&sc.OrderID, &sc.OrderItemID, &sc.SellerID, &sc.FulfilmentStatus,
		&sc.FromName, &sc.FromEmail, &sc.FromPhone,
		&sc.FromStreet1, &sc.FromStreet2, &sc.FromCity, &sc.FromRegion, &sc.FromPostalCode, &sc.FromCountryISO,
		&sc.ToName, &sc.ToEmail, &sc.ToPhone,
		&sc.ToStreet1, &sc.ToStreet2, &sc.ToCity, &sc.ToRegion, &sc.ToPostalCode, &sc.ToCountryISO,
		&sc.StoredParcel, &sc.StoredCustoms,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrOrderNotFound
	}
	return sc, err
}

// UpsertQuote inserts or updates the pending shipment row created at /shipping/rates.
// One pending shipment per order_item_id (unique partial index).
func (r *ShipmentRepository) UpsertQuote(ctx context.Context, s *models.Shipment) error {
	meta := s.ProviderMetadata
	if len(meta) == 0 {
		meta = json.RawMessage(`{}`)
	}
	parcel := s.ParcelDetails
	if len(parcel) == 0 {
		parcel = json.RawMessage(`null`)
	}
	customs := s.CustomsDeclaration
	if len(customs) == 0 {
		customs = json.RawMessage(`null`)
	}
	return r.db.QueryRow(ctx, `
		insert into marketplace.shipments (
			order_id, order_item_id, seller_id, delivery_mode, status, is_international,
			parcel_details, customs_declaration, provider_shipment_id, provider_customs_declaration_id,
			provider_metadata
		) values ($1,$2,$3,$4,'pending',$5,$6,$7,$8,$9,$10)
		on conflict (order_item_id) where status = 'pending' do update set
			is_international = excluded.is_international,
			parcel_details = excluded.parcel_details,
			customs_declaration = excluded.customs_declaration,
			provider_shipment_id = excluded.provider_shipment_id,
			provider_customs_declaration_id = excluded.provider_customs_declaration_id,
			provider_metadata = excluded.provider_metadata,
			updated_at = now()
		returning id, created_at, updated_at`,
		s.OrderID, s.OrderItemID, s.SellerID, s.DeliveryMode, s.IsInternational,
		parcel, customs, s.ProviderShipmentID, s.ProviderCustomsID, meta,
	).Scan(&s.ID, &s.CreatedAt, &s.UpdatedAt)
}

// CompleteLabel updates the pending shipment after a label is purchased
// (tracking, label media, status → label_created).
func (r *ShipmentRepository) CompleteLabel(ctx context.Context, orderItemID uuid.UUID, s *models.Shipment) error {
	meta := s.ProviderMetadata
	if len(meta) == 0 {
		meta = json.RawMessage(`{}`)
	}
	err := r.db.QueryRow(ctx, `
		update marketplace.shipments
		set courier_provider = $2,
		    tracking_number = $3,
		    label_media_id = $4,
		    status = $5,
		    provider_shipment_id = coalesce($6, provider_shipment_id),
		    provider_tracking_url = $7,
		    provider_metadata = $8,
		    updated_at = now()
		where order_item_id = $1 and status = 'pending'
		returning id, order_id, seller_id, is_international, parcel_details, customs_declaration,
		          delivery_mode, created_at, updated_at`,
		orderItemID, s.CourierProvider, s.TrackingNumber, s.LabelMediaID, s.Status,
		s.ProviderShipmentID, s.ProviderTrackingURL, meta,
	).Scan(
		&s.ID, &s.OrderID, &s.SellerID, &s.IsInternational, &s.ParcelDetails, &s.CustomsDeclaration,
		&s.DeliveryMode, &s.CreatedAt, &s.UpdatedAt,
	)
	return err
}

func (r *ShipmentRepository) Create(ctx context.Context, s *models.Shipment) error {
	meta := s.ProviderMetadata
	if len(meta) == 0 {
		meta = json.RawMessage(`{}`)
	}
	return r.db.QueryRow(ctx, `
		insert into marketplace.shipments (
			order_id, order_item_id, seller_id, courier_provider, tracking_number, label_media_id,
			delivery_mode, status, provider_shipment_id, provider_tracking_url, provider_metadata
		) values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		returning id, created_at, updated_at`,
		s.OrderID, s.OrderItemID, s.SellerID, s.CourierProvider, s.TrackingNumber, s.LabelMediaID,
		s.DeliveryMode, s.Status, s.ProviderShipmentID, s.ProviderTrackingURL, meta,
	).Scan(&s.ID, &s.CreatedAt, &s.UpdatedAt)
}

func (r *ShipmentRepository) GetByTrackingNumber(ctx context.Context, trackingNumber string) (*models.Shipment, error) {
	s := &models.Shipment{}
	err := r.db.QueryRow(ctx, `
		select id, order_id, order_item_id, seller_id, courier_provider, tracking_number, label_media_id,
		       delivery_mode, status, proof_of_delivery_media_id, delivered_at,
		       provider_shipment_id, provider_tracking_url, provider_metadata,
		       created_at, updated_at
		from marketplace.shipments
		where tracking_number = $1`, trackingNumber,
	).Scan(
		&s.ID, &s.OrderID, &s.OrderItemID, &s.SellerID, &s.CourierProvider, &s.TrackingNumber, &s.LabelMediaID,
		&s.DeliveryMode, &s.Status, &s.ProofOfDeliveryMediaID, &s.DeliveredAt,
		&s.ProviderShipmentID, &s.ProviderTrackingURL, &s.ProviderMetadata,
		&s.CreatedAt, &s.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrShipmentNotFound
	}
	return s, err
}

// UpdateTrackingStatus applies a Shippo webhook tracking update by tracking_number.
func (r *ShipmentRepository) UpdateTrackingStatus(ctx context.Context, trackingNumber, status string, deliveredAt *time.Time) error {
	tag, err := r.db.Exec(ctx, `
		update marketplace.shipments
		set status = $2,
		    delivered_at = coalesce($3, delivered_at),
		    updated_at = now()
		where tracking_number = $1`, trackingNumber, status, deliveredAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrShipmentNotFound
	}
	return nil
}

// MarkOrderItemDelivered sets one order_items.fulfilment_status = delivered.
// One seller's line completing does not complete the whole order — see
// MarkOrderDeliveredIfComplete.
func (r *ShipmentRepository) MarkOrderItemDelivered(ctx context.Context, orderItemID uuid.UUID) error {
	_, err := r.db.Exec(ctx, `
		update marketplace.order_items
		set fulfilment_status = 'delivered', updated_at = now()
		where id = $1 and fulfilment_status <> 'cancelled'`, orderItemID)
	return err
}

// MarkOrderDeliveredIfComplete sets marketplace.orders.status = delivered only when
// every line on the order is delivered or cancelled, and at least one was delivered.
// An order with several sellers stays open until the last parcel arrives.
// Returns true when this call completed the order.
func (r *ShipmentRepository) MarkOrderDeliveredIfComplete(ctx context.Context, orderID uuid.UUID) (bool, error) {
	tag, err := r.db.Exec(ctx, `
		update marketplace.orders o
		set status = 'delivered', updated_at = now()
		where o.id = $1
		  and o.status <> 'delivered'
		  and not exists (
		        select 1 from marketplace.order_items oi
		        where oi.order_id = o.id
		          and oi.fulfilment_status not in ('delivered', 'cancelled')
		      )
		  and exists (
		        select 1 from marketplace.order_items oi
		        where oi.order_id = o.id
		          and oi.fulfilment_status = 'delivered'
		      )`, orderID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// MarkOrderItemDispatched sets order_items.fulfilment_status = dispatched after label buy.
func (r *ShipmentRepository) MarkOrderItemDispatched(ctx context.Context, orderItemID uuid.UUID) error {
	_, err := r.db.Exec(ctx, `
		update marketplace.order_items
		set fulfilment_status = 'dispatched', updated_at = now()
		where id = $1`, orderItemID)
	return err
}

// GetLabelForSeller returns the stored label PDF for the seller's own order
// item. The seller id is part of the query rather than checked afterwards, so
// one seller can never read another's label by guessing an order item id.
func (r *ShipmentRepository) GetLabelForSeller(ctx context.Context, sellerID, orderItemID string) (*ShipmentLabel, error) {
	sellerUUID, err := uuid.Parse(sellerID)
	if err != nil {
		return nil, ErrShipmentNotFound
	}
	itemUUID, err := uuid.Parse(orderItemID)
	if err != nil {
		return nil, ErrShipmentNotFound
	}

	var (
		label      ShipmentLabel
		objectPath *string
		mimeType   *string
	)
	err = r.db.QueryRow(ctx, `
		select a.object_path, a.mime_type, s.tracking_number, s.courier_provider
		from marketplace.shipments s
		left join media.media_assets a on a.id = s.label_media_id
		where s.order_item_id = $1 and s.seller_id = $2`,
		itemUUID, sellerUUID,
	).Scan(&objectPath, &mimeType, &label.TrackingNumber, &label.Provider)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrShipmentNotFound
	}
	if err != nil {
		return nil, err
	}
	// A shipment row exists from the rate quote before any label is bought.
	if objectPath == nil || *objectPath == "" {
		return nil, ErrShipmentLabelNotFound
	}

	label.ObjectPath = *objectPath
	label.MimeType = "application/pdf"
	if mimeType != nil && *mimeType != "" {
		label.MimeType = *mimeType
	}
	return &label, nil
}

// GetLatestForOrderItem returns the newest shipment for an item that belongs to this seller.
func (r *ShipmentRepository) GetLatestForOrderItem(ctx context.Context, sellerID, orderItemID string) (*models.Shipment, error) {
	s := &models.Shipment{} // empty struct that Scan fills in

	err := r.db.QueryRow(ctx, `
		select id, order_id, order_item_id, seller_id, is_international,
		       courier_provider, tracking_number, label_media_id, delivery_mode, status,
		       proof_of_delivery_media_id, delivered_at, provider_shipment_id,
		       provider_customs_declaration_id, provider_tracking_url, provider_metadata,
		       parcel_details, customs_declaration, created_at, updated_at
		from marketplace.shipments
		where order_item_id = $1 and seller_id = $2
		order by created_at desc
		limit 1`, orderItemID, sellerID,
	).Scan(
		&s.ID, &s.OrderID, &s.OrderItemID, &s.SellerID, &s.IsInternational,
		&s.CourierProvider, &s.TrackingNumber, &s.LabelMediaID, &s.DeliveryMode, &s.Status,
		&s.ProofOfDeliveryMediaID, &s.DeliveredAt, &s.ProviderShipmentID,
		&s.ProviderCustomsID, &s.ProviderTrackingURL, &s.ProviderMetadata,
		&s.ParcelDetails, &s.CustomsDeclaration, &s.CreatedAt, &s.UpdatedAt,
	)

	// No row found: return a clear "not found" error.
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrShipmentNotFound
	}
	return s, err // on success err is nil; on any other error the caller checks err first
}

// MarkLocalDelivered updates shipment, item, and order atomically:
// either all of the changes happen or none of them do.
func (r *ShipmentRepository) MarkLocalDelivered(ctx context.Context, shipmentID, orderItemID, orderID uuid.UUID, deliveredAt time.Time) error {
	tx, err := r.db.Begin(ctx) // start a transaction
	if err != nil {
		return err
	}
	// If we return early because of an error, undo everything.
	// After a successful Commit, this Rollback does nothing.
	defer tx.Rollback(ctx)

	// Step 1: mark the shipment delivered and record when.
	if _, err := tx.Exec(ctx, `
		update marketplace.shipments
		set status = 'delivered', delivered_at = $2, updated_at = now()
		where id = $1`, shipmentID, deliveredAt); err != nil {
		return err
	}

	// Step 2: mark the order item delivered,
	// unless it was cancelled in the meantime (never revive a cancelled item).
	if _, err := tx.Exec(ctx, `
		update marketplace.order_items
		set fulfilment_status = 'delivered', updated_at = now()
		where id = $1 and fulfilment_status <> 'cancelled'`, orderItemID); err != nil {
		return err
	}

	// Step 3: mark the whole order delivered, but only when every item is finished.
	if _, err := tx.Exec(ctx, `
		update marketplace.orders o
		set status = 'delivered', updated_at = now()
		where o.id = $1
		  and o.status <> 'delivered'              -- skip if it's already delivered (no pointless write)
		  and not exists (                          -- no item is still open...
		        select 1 from marketplace.order_items oi
		        where oi.order_id = o.id
		          and oi.fulfilment_status not in ('delivered', 'cancelled')
		      )
		  and exists (                              -- ...and at least one item was really delivered
		        select 1 from marketplace.order_items oi   --   (so an all-cancelled order isn't marked "delivered")
		        where oi.order_id = o.id
		          and oi.fulfilment_status = 'delivered'
		      )`, orderID); err != nil {
		return err
	}

	return tx.Commit(ctx) // save all three changes together
}