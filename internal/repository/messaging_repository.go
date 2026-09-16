package repository

import (
	"context" // request-scoped cancellation / deadlines from the HTTP layer
	"errors"  // sentinel errors returned to the service layer
	"time"    // pagination cursor (*time.Time "before") for ListMessages

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"        // pgx.ErrNoRows for "not found" mapping
	"github.com/jackc/pgx/v5/pgxpool" // connection pool shared across the API

	"myapp/internal/models"
)

// Sentinel errors the service maps to ErrConversationNotFound / ErrNotParticipant.
var (
	ErrConversationNotFound = errors.New("conversation not found")
	ErrNotParticipant       = errors.New("not a conversation participant")
)

// MessagingRepository is the only layer that talks to:
//   - messaging.conversations
//   - messaging.conversation_participants
//   - messaging.messages
//   - support.cases
// It has no business rules — just SQL + row scanning.
type MessagingRepository struct {
	db *pgxpool.Pool
}

// NewMessagingRepository wraps the shared Postgres pool.
func NewMessagingRepository(db *pgxpool.Pool) *MessagingRepository {
	return &MessagingRepository{db: db}
}

// conversationSelectCols is the shared column list for scanning a Conversation.
// Always alias the conversations table as `c` when embedding this fragment.
const conversationSelectCols = `
	c.id, c.type, c.status, c.product_id, c.shop_id, c.order_id, c.order_item_id,
	c.created_by_user_id, c.last_message_at, c.created_at, c.updated_at`

// CreateConversation inserts a conversation and its participants in one transaction.
// Used for product_inquiry and order threads (support uses CreateSupportConversation).
// On success, conv.ID / CreatedAt / UpdatedAt and each participant's ID / JoinedAt are filled in.
func (r *MessagingRepository) CreateConversation(
	ctx context.Context,
	conv *models.Conversation,
	participants []models.ConversationParticipant,
) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) // no-op after successful Commit

	// 1) Insert the room row
	err = tx.QueryRow(ctx, `
		insert into messaging.conversations (
			type, status, product_id, shop_id, order_id, order_item_id, created_by_user_id
		) values ($1,$2,$3,$4,$5,$6,$7)
		returning id, created_at, updated_at`,
		conv.Type, conv.Status, conv.ProductID, conv.ShopID, conv.OrderID, conv.OrderItemID, conv.CreatedByUserID,
	).Scan(&conv.ID, &conv.CreatedAt, &conv.UpdatedAt)
	if err != nil {
		return err
	}

	// 2) Insert every participant (e.g. customer + seller) against the new conversation id
	for i := range participants {
		participants[i].ConversationID = conv.ID
		err = tx.QueryRow(ctx, `
			insert into messaging.conversation_participants (
				conversation_id, user_id, role
			) values ($1,$2,$3)
			returning id, joined_at`,
			participants[i].ConversationID, participants[i].UserID, participants[i].Role,
		).Scan(&participants[i].ID, &participants[i].JoinedAt)
		if err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

// FindOpenProductInquiry returns an existing open product inquiry for this customer+product.
// Matches the partial unique index idx_conversations_product_inquiry_open.
func (r *MessagingRepository) FindOpenProductInquiry(ctx context.Context, productID, customerUserID string) (*models.Conversation, error) {
	c := &models.Conversation{}
	err := r.db.QueryRow(ctx, `
		select `+conversationSelectCols+`
		from messaging.conversations c
		where c.type = 'product_inquiry'
		  and c.status = 'open'
		  and c.product_id = $1
		  and c.created_by_user_id = $2`, productID, customerUserID,
	).Scan(
		&c.ID, &c.Type, &c.Status, &c.ProductID, &c.ShopID, &c.OrderID, &c.OrderItemID,
		&c.CreatedByUserID, &c.LastMessageAt, &c.CreatedAt, &c.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrConversationNotFound
	}
	return c, err
}

// FindByOrderItem returns the order conversation for a line item, if any.
// At most one exists (idx_conversations_order_item).
func (r *MessagingRepository) FindByOrderItem(ctx context.Context, orderItemID string) (*models.Conversation, error) {
	c := &models.Conversation{}
	err := r.db.QueryRow(ctx, `
		select `+conversationSelectCols+`
		from messaging.conversations c
		where c.type = 'order' and c.order_item_id = $1`, orderItemID,
	).Scan(
		&c.ID, &c.Type, &c.Status, &c.ProductID, &c.ShopID, &c.OrderID, &c.OrderItemID,
		&c.CreatedByUserID, &c.LastMessageAt, &c.CreatedAt, &c.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrConversationNotFound
	}
	return c, err
}

// GetForParticipant returns a conversation only if userID is a participant with the given role.
// role is normalized (superadmin → admin). This blocks cross-role / other-seller access.
func (r *MessagingRepository) GetForParticipant(ctx context.Context, conversationID, userID, role string) (*models.Conversation, error) {
	c := &models.Conversation{}
	err := r.db.QueryRow(ctx, `
		select `+conversationSelectCols+`
		from messaging.conversations c
		inner join messaging.conversation_participants p
			on p.conversation_id = c.id and p.user_id = $2 and p.role = $3
		where c.id = $1`, conversationID, userID, role,
	).Scan(
		&c.ID, &c.Type, &c.Status, &c.ProductID, &c.ShopID, &c.OrderID, &c.OrderItemID,
		&c.CreatedByUserID, &c.LastMessageAt, &c.CreatedAt, &c.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		// Either the conversation doesn't exist OR the user isn't in it — both look like "not found"
		return nil, ErrConversationNotFound
	}
	return c, err
}

// ListForUser returns inbox rows for a participant, newest activity first.
// userID is the JWT subject; role must match conversation_participants.role so a
// seller only sees threads where they sit as role=seller (not another account's chats).
// Unread = messages from someone else after this user's last_read_at.
func (r *MessagingRepository) ListForUser(ctx context.Context, userID, role string) ([]models.ConversationSummary, error) {
	rows, err := r.db.Query(ctx, `
		select `+conversationSelectCols+`,
			(
				select count(*)::int
				from messaging.messages m
				where m.conversation_id = c.id
				  and m.deleted_at is null
				  and m.sender_user_id <> $1
				  and (p.last_read_at is null or m.created_at > p.last_read_at)
			) as unread_count
		from messaging.conversations c
		inner join messaging.conversation_participants p
			on p.conversation_id = c.id and p.user_id = $1 and p.role = $2
		order by coalesce(c.last_message_at, c.created_at) desc`, userID, role)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []models.ConversationSummary{}
	ids := []uuid.UUID{}
	for rows.Next() {
		var s models.ConversationSummary
		if err := rows.Scan(
			&s.ID, &s.Type, &s.Status, &s.ProductID, &s.ShopID, &s.OrderID, &s.OrderItemID,
			&s.CreatedByUserID, &s.LastMessageAt, &s.CreatedAt, &s.UpdatedAt,
			&s.UnreadCount,
		); err != nil {
			return nil, err
		}
		out = append(out, s)
		ids = append(ids, s.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Batch-load participants and support cases so we don't N+1 query per inbox row
	byConv, err := r.participantsByConversation(ctx, ids)
	if err != nil {
		return nil, err
	}
	byCase, err := r.supportCasesByConversation(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Participants = byConv[out[i].ID]
		if out[i].Participants == nil {
			out[i].Participants = []models.ConversationParticipant{}
		}
		if sc, ok := byCase[out[i].ID]; ok {
			out[i].SupportCase = &sc
		}
	}
	return out, nil
}

// ListParticipants returns everyone in a conversation (customer, seller, and/or admin).
func (r *MessagingRepository) ListParticipants(ctx context.Context, conversationID string) ([]models.ConversationParticipant, error) {
	id, err := uuid.Parse(conversationID)
	if err != nil {
		return nil, ErrConversationNotFound
	}
	byConv, err := r.participantsByConversation(ctx, []uuid.UUID{id})
	if err != nil {
		return nil, err
	}
	items := byConv[id]
	if items == nil {
		items = []models.ConversationParticipant{}
	}
	return items, nil
}

// participantsByConversation batch-loads participant rows keyed by conversation id.
func (r *MessagingRepository) participantsByConversation(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID][]models.ConversationParticipant, error) {
	out := make(map[uuid.UUID][]models.ConversationParticipant, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	// user_id is polymorphic, so the name/photo come from whichever table the
	// participant's role points at.
	rows, err := r.db.Query(ctx, `
		select p.id, p.conversation_id, p.user_id, p.role, p.last_read_at, p.joined_at,
		       case p.role
		           when 'customer' then nullif(btrim(cu.display_name), '')
		           when 'seller'   then coalesce(nullif(btrim(se.trading_name), ''), se.legal_name)
		           when 'admin'    then nullif(btrim(ad.display_name), '')
		       end as display_name,
		       case p.role
		           when 'customer' then cu.image_url
		           when 'seller'   then se.image_url
		           when 'admin'    then ad.image_url
		       end as image_url
		from messaging.conversation_participants p
		left join customer.customers cu on p.role = 'customer' and cu.id = p.user_id
		left join seller.sellers se     on p.role = 'seller'   and se.id = p.user_id
		left join admin.admin_users ad  on p.role = 'admin'    and ad.id = p.user_id
		where p.conversation_id = any($1)
		order by p.joined_at asc`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var p models.ConversationParticipant
		if err := rows.Scan(
			&p.ID, &p.ConversationID, &p.UserID, &p.Role, &p.LastReadAt, &p.JoinedAt,
			&p.DisplayName, &p.ImageURL,
		); err != nil {
			return nil, err
		}
		out[p.ConversationID] = append(out[p.ConversationID], p)
	}
	return out, rows.Err()
}

// CreateMessage stores a message and, in the same transaction:
//  1. inserts optional media.media_assets + message_attachments (damage photos, PDFs, …)
//  2. bumps conversations.last_message_at (for inbox sorting)
//  3. sets the sender's last_read_at to the new message time (they've "seen" their own send)
func (r *MessagingRepository) CreateMessage(ctx context.Context, msg *models.Message, assets []models.MediaAsset) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	err = tx.QueryRow(ctx, `
		insert into messaging.messages (conversation_id, sender_user_id, body, type)
		values ($1,$2,$3,$4)
		returning id, created_at`,
		msg.ConversationID, msg.SenderUserID, msg.Body, msg.Type,
	).Scan(&msg.ID, &msg.CreatedAt)
	if err != nil {
		return err
	}

	attachments := make([]models.MessageAttachment, 0, len(assets))
	for i := range assets {
		if err := insertMediaAssetTx(ctx, tx, &assets[i]); err != nil {
			return err
		}
		var att models.MessageAttachment
		err = tx.QueryRow(ctx, `
			insert into messaging.message_attachments (message_id, media_id)
			values ($1,$2)
			returning id, message_id, media_id, created_at`,
			msg.ID, assets[i].ID,
		).Scan(&att.ID, &att.MessageID, &att.MediaID, &att.CreatedAt)
		if err != nil {
			return err
		}
		att.AssetType = assets[i].AssetType
		att.ObjectPath = assets[i].ObjectPath
		att.CDNURL = assets[i].CDNURL
		att.MimeType = assets[i].MimeType
		att.SizeBytes = assets[i].SizeBytes
		attachments = append(attachments, att)
	}
	msg.Attachments = attachments

	_, err = tx.Exec(ctx, `
		update messaging.conversations
		set last_message_at = $2, updated_at = now()
		where id = $1`, msg.ConversationID, msg.CreatedAt)
	if err != nil {
		return err
	}

	// Sender has read up through their own message.
	_, err = tx.Exec(ctx, `
		update messaging.conversation_participants
		set last_read_at = $3
		where conversation_id = $1 and user_id = $2`,
		msg.ConversationID, msg.SenderUserID, msg.CreatedAt)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// ListMessages returns messages in chronological order (oldest → newest) for the UI.
// Internally fetches newest-first (efficient with LIMIT + before cursor), then reverses.
// Soft-deleted rows (deleted_at IS NOT NULL) are excluded.
func (r *MessagingRepository) ListMessages(ctx context.Context, conversationID string, limit int, before *time.Time) ([]models.Message, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}

	var rows pgx.Rows
	var err error
	if before != nil {
		// Cursor page: messages strictly older than `before`
		rows, err = r.db.Query(ctx, `
			select id, conversation_id, sender_user_id, body, type, created_at, deleted_at
			from messaging.messages
			where conversation_id = $1 and deleted_at is null and created_at < $2
			order by created_at desc
			limit $3`, conversationID, *before, limit)
	} else {
		// First page: most recent `limit` messages
		rows, err = r.db.Query(ctx, `
			select id, conversation_id, sender_user_id, body, type, created_at, deleted_at
			from messaging.messages
			where conversation_id = $1 and deleted_at is null
			order by created_at desc
			limit $2`, conversationID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Fetch newest-first then reverse for chronological UI order.
	tmp := []models.Message{}
	for rows.Next() {
		var m models.Message
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.SenderUserID, &m.Body, &m.Type, &m.CreatedAt, &m.DeletedAt); err != nil {
			return nil, err
		}
		tmp = append(tmp, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]models.Message, len(tmp))
	for i := range tmp {
		out[len(tmp)-1-i] = tmp[i]
	}

	if err := r.attachMessageAttachments(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

// attachMessageAttachments fills Message.Attachments for a page of messages.
func (r *MessagingRepository) attachMessageAttachments(ctx context.Context, msgs []models.Message) error {
	if len(msgs) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(msgs))
	index := make(map[uuid.UUID]int, len(msgs))
	for i := range msgs {
		ids[i] = msgs[i].ID
		index[msgs[i].ID] = i
		msgs[i].Attachments = []models.MessageAttachment{}
	}

	rows, err := r.db.Query(ctx, `
		select ma.id, ma.message_id, ma.media_id, ma.created_at,
		       a.asset_type, a.object_path, a.cdn_url, a.mime_type, a.size_bytes
		from messaging.message_attachments ma
		inner join media.media_assets a on a.id = ma.media_id
		where ma.message_id = any($1)
		order by ma.created_at asc`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var att models.MessageAttachment
		if err := rows.Scan(
			&att.ID, &att.MessageID, &att.MediaID, &att.CreatedAt,
			&att.AssetType, &att.ObjectPath, &att.CDNURL, &att.MimeType, &att.SizeBytes,
		); err != nil {
			return err
		}
		if i, ok := index[att.MessageID]; ok {
			msgs[i].Attachments = append(msgs[i].Attachments, att)
		}
	}
	return rows.Err()
}

// MarkRead sets the participant's last_read_at to now.
// Returns ErrNotParticipant if no matching participant row was updated.
func (r *MessagingRepository) MarkRead(ctx context.Context, conversationID, userID string) error {
	tag, err := r.db.Exec(ctx, `
		update messaging.conversation_participants
		set last_read_at = now()
		where conversation_id = $1 and user_id = $2`, conversationID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotParticipant
	}
	return nil
}

// CloseConversation sets conversations.status = closed.
// For support threads it also sets support.cases.status = closed (when still open/in_progress).
func (r *MessagingRepository) CloseConversation(ctx context.Context, conversationID string) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		update messaging.conversations
		set status = 'closed', updated_at = now()
		where id = $1 and status = 'open'`, conversationID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		// Either missing or already closed — caller should have verified participancy first.
		var exists bool
		_ = tx.QueryRow(ctx, `select exists(select 1 from messaging.conversations where id = $1)`, conversationID).Scan(&exists)
		if !exists {
			return ErrConversationNotFound
		}
		// Already closed: treat as success (idempotent).
	}

	_, err = tx.Exec(ctx, `
		update support.cases
		set status = 'closed', updated_at = now()
		where conversation_id = $1 and status in ('open', 'in_progress')`, conversationID)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// ReopenConversation sets conversations.status = open.
// For support threads it sets support.cases.status back to open when currently closed.
func (r *MessagingRepository) ReopenConversation(ctx context.Context, conversationID string) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		update messaging.conversations
		set status = 'open', updated_at = now()
		where id = $1 and status = 'closed'`, conversationID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		_ = tx.QueryRow(ctx, `select exists(select 1 from messaging.conversations where id = $1)`, conversationID).Scan(&exists)
		if !exists {
			return ErrConversationNotFound
		}
		// Already open: idempotent success.
	}

	_, err = tx.Exec(ctx, `
		update support.cases
		set status = 'open', updated_at = now()
		where conversation_id = $1 and status = 'closed'`, conversationID)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// CreateSupportConversation creates, in one transaction:
//  1. messaging.conversations (type=support)
//  2. messaging.conversation_participants (admin + customer/seller)
//  3. support.cases (subject/status/priority + counterpart)
func (r *MessagingRepository) CreateSupportConversation(
	ctx context.Context,
	conv *models.Conversation,
	participants []models.ConversationParticipant,
	sc *models.SupportCase,
) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	err = tx.QueryRow(ctx, `
		insert into messaging.conversations (
			type, status, product_id, shop_id, order_id, order_item_id, created_by_user_id
		) values ($1,$2,$3,$4,$5,$6,$7)
		returning id, created_at, updated_at`,
		conv.Type, conv.Status, conv.ProductID, conv.ShopID, conv.OrderID, conv.OrderItemID, conv.CreatedByUserID,
	).Scan(&conv.ID, &conv.CreatedAt, &conv.UpdatedAt)
	if err != nil {
		return err
	}

	for i := range participants {
		participants[i].ConversationID = conv.ID
		err = tx.QueryRow(ctx, `
			insert into messaging.conversation_participants (
				conversation_id, user_id, role
			) values ($1,$2,$3)
			returning id, joined_at`,
			participants[i].ConversationID, participants[i].UserID, participants[i].Role,
		).Scan(&participants[i].ID, &participants[i].JoinedAt)
		if err != nil {
			return err
		}
	}

	sc.ConversationID = conv.ID
	if sc.Status == "" {
		sc.Status = "open"
	}
	if sc.Priority == "" {
		sc.Priority = "normal"
	}
	err = tx.QueryRow(ctx, `
		insert into support.cases (
			conversation_id, opened_by_user_id, opened_by_role,
			counterpart_user_id, counterpart_role, subject, status, priority
		) values ($1,$2,$3,$4,$5,$6,$7,$8)
		returning id, created_at, updated_at`,
		sc.ConversationID, sc.OpenedByUserID, sc.OpenedByRole,
		sc.CounterpartUserID, sc.CounterpartRole, sc.Subject, sc.Status, sc.Priority,
	).Scan(&sc.ID, &sc.CreatedAt, &sc.UpdatedAt)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// FindOpenSupportForCounterpart returns the active support conversation for a customer/seller.
// "Active" = support.cases.status in (open, in_progress) — matches idx_support_cases_counterpart_open.
func (r *MessagingRepository) FindOpenSupportForCounterpart(ctx context.Context, counterpartRole, counterpartUserID string) (*models.Conversation, *models.SupportCase, error) {
	c := &models.Conversation{}
	sc := &models.SupportCase{}
	err := r.db.QueryRow(ctx, `
		select `+conversationSelectCols+`,
		       sc.id, sc.conversation_id, sc.opened_by_user_id, sc.opened_by_role,
		       sc.counterpart_user_id, sc.counterpart_role, sc.subject, sc.status, sc.priority,
		       sc.created_at, sc.updated_at
		from support.cases sc
		inner join messaging.conversations c on c.id = sc.conversation_id
		where sc.counterpart_role = $1
		  and sc.counterpart_user_id = $2
		  and sc.status in ('open', 'in_progress')`, counterpartRole, counterpartUserID,
	).Scan(
		&c.ID, &c.Type, &c.Status, &c.ProductID, &c.ShopID, &c.OrderID, &c.OrderItemID,
		&c.CreatedByUserID, &c.LastMessageAt, &c.CreatedAt, &c.UpdatedAt,
		&sc.ID, &sc.ConversationID, &sc.OpenedByUserID, &sc.OpenedByRole,
		&sc.CounterpartUserID, &sc.CounterpartRole, &sc.Subject, &sc.Status, &sc.Priority,
		&sc.CreatedAt, &sc.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ErrConversationNotFound
	}
	return c, sc, err
}

// GetSupportCaseByConversation returns the case for a support conversation, if any.
func (r *MessagingRepository) GetSupportCaseByConversation(ctx context.Context, conversationID string) (*models.SupportCase, error) {
	sc := &models.SupportCase{}
	err := r.db.QueryRow(ctx, `
		select id, conversation_id, opened_by_user_id, opened_by_role,
		       counterpart_user_id, counterpart_role, subject, status, priority,
		       created_at, updated_at
		from support.cases
		where conversation_id = $1`, conversationID,
	).Scan(
		&sc.ID, &sc.ConversationID, &sc.OpenedByUserID, &sc.OpenedByRole,
		&sc.CounterpartUserID, &sc.CounterpartRole, &sc.Subject, &sc.Status, &sc.Priority,
		&sc.CreatedAt, &sc.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrConversationNotFound
	}
	return sc, err
}

// AddParticipant adds a user to a conversation.
// ON CONFLICT upserts role if they were already a participant (idempotent join).
// Used when a second admin joins an existing support case.
func (r *MessagingRepository) AddParticipant(ctx context.Context, conversationID, userID, role string) (*models.ConversationParticipant, error) {
	p := &models.ConversationParticipant{}
	err := r.db.QueryRow(ctx, `
		insert into messaging.conversation_participants (conversation_id, user_id, role)
		values ($1,$2,$3)
		on conflict (conversation_id, user_id) do update
			set role = excluded.role
		returning id, conversation_id, user_id, role, last_read_at, joined_at`,
		conversationID, userID, role,
	).Scan(&p.ID, &p.ConversationID, &p.UserID, &p.Role, &p.LastReadAt, &p.JoinedAt)
	return p, err
}

// supportCasesByConversation batch-loads support.cases keyed by conversation id
// (only conversations that actually have a case will appear in the map).
func (r *MessagingRepository) supportCasesByConversation(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]models.SupportCase, error) {
	out := make(map[uuid.UUID]models.SupportCase, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.db.Query(ctx, `
		select id, conversation_id, opened_by_user_id, opened_by_role,
		       counterpart_user_id, counterpart_role, subject, status, priority,
		       created_at, updated_at
		from support.cases
		where conversation_id = any($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var sc models.SupportCase
		if err := rows.Scan(
			&sc.ID, &sc.ConversationID, &sc.OpenedByUserID, &sc.OpenedByRole,
			&sc.CounterpartUserID, &sc.CounterpartRole, &sc.Subject, &sc.Status, &sc.Priority,
			&sc.CreatedAt, &sc.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out[sc.ConversationID] = sc
	}
	return out, rows.Err()
}
