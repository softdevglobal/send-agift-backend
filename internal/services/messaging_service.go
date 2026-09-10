package services

import (
	"context" // request-scoped context passed through to repository calls (cancellation, deadlines)
	"encoding/json"
	"errors"  // sentinel error values + errors.Is for wrapped-error matching
	"strings" // trimming/lowercasing user input (type, roles, IDs, subject, priority)
	"time"    // *time.Time used for the "before" pagination cursor on ListMessages

	"github.com/google/uuid" // parsing/validating string IDs into uuid.UUID

	"myapp/internal/models"     // domain structs: Conversation, Message, SupportCase, etc.
	"myapp/internal/repository" // data-access layer this service delegates persistence to
)

// Sentinel errors this service can return — the handler layer maps each of these
// to a specific HTTP status code (see writeError in the previous file)
var (
	ErrConversationNotFound  = errors.New("conversation not found")
	ErrInvalidConversation   = errors.New("invalid conversation")
	ErrNotParticipant        = errors.New("not a conversation participant")
	ErrForbiddenConversation = errors.New("forbidden conversation action")
	ErrSupportUserNotFound   = errors.New("support counterpart not found")
	ErrInvalidChatAttachment = errors.New("invalid chat attachment")
	// NOTE: ErrProductNotFound and ErrOrderItemNotFound are referenced later in this file
	// (and in the handler's writeError switch) but are NOT declared in this var block —
	// they must be defined elsewhere in the package (another file), otherwise this won't compile.
)

// MessagingService owns product-inquiry, order, and admin support chat (text + attachments).
// It orchestrates business rules on top of several repositories — it doesn't talk to the DB directly.
type MessagingService struct {
	msg       *repository.MessagingRepository // conversations/messages/participants persistence
	orders    *repository.OrderRepository     // looks up products/order items for inquiry & order chats
	customers *repository.CustomerRepository  // validates customer existence for support chats
	sellers   *repository.SellerRepository    // validates seller existence for support chats
	admins    *repository.AdminRepository     // finds an active admin to route new support tickets to
	s3        *S3Service                      // public URLs for uploaded chat files
	bucket    string                          // S3 bucket name stamped onto media_assets
}

// NewMessagingService wires all required repositories into a new service instance (standard DI constructor)
func NewMessagingService(
	msg *repository.MessagingRepository,
	orders *repository.OrderRepository,
	customers *repository.CustomerRepository,
	sellers *repository.SellerRepository,
	admins *repository.AdminRepository,
	s3 *S3Service,
	bucket string,
) *MessagingService {
	return &MessagingService{
		msg:       msg,
		orders:    orders,
		customers: customers,
		sellers:   sellers,
		admins:    admins,
		s3:        s3,
		bucket:    bucket,
	}
}

// maxChatAttachments caps files per message (damage photo sets, invoices, etc.).
const maxChatAttachments = 5

// StartConversationInput opens (or reuses) a product, order, or support thread.
// It's a single struct covering all three conversation types — fields relevant to
// one type are simply left nil/omitted for the others.
type StartConversationInput struct {
	Type              string             `json:"type"`                // product_inquiry | order | support — determines which fields below are required
	ProductID         *string            `json:"product_id"`          // required for product_inquiry
	OrderItemID       *string            `json:"order_item_id"`       // required for order
	CounterpartRole   *string            `json:"counterpart_role"`    // customer | seller — required when an admin opens a support case
	CounterpartUserID *string            `json:"counterpart_user_id"` // target customer/seller id — required when an admin opens a support case
	Subject           *string            `json:"subject"`             // optional free-text subject, support only
	Priority          *string            `json:"priority"`            // low|normal|high|urgent, support only (admin-opened)
	Body              *string            `json:"body"`                // optional first message text
	Attachments       []ChatAttachmentInput `json:"attachments"`      // optional first-message files (already uploaded via presign)
}

// ChatAttachmentInput is one already-uploaded file (via /media/presign-upload folder chat-image|chat-document).
type ChatAttachmentInput struct {
	ObjectPath string          `json:"object_path"` // S3 key returned by presign-upload
	MimeType   string          `json:"mime_type"`   // image/jpeg, application/pdf, ...
	SizeBytes  int64           `json:"size_bytes"`
	Metadata   json.RawMessage `json:"metadata"` // optional width/height/etc.
}

// SendMessageInput is the body of a new chat bubble (text and/or files).
type SendMessageInput struct {
	Body        string                `json:"body"`
	Attachments []ChatAttachmentInput `json:"attachments"`
}

// normalizeMessagingRole lowercases the role and treats "superadmin" as "admin"
// for the purposes of messaging permissions (matches the route doc comment:
// "superadmin counts as admin")
func normalizeMessagingRole(role string) string {
	role = strings.ToLower(role)
	if role == "superadmin" {
		return "admin"
	}
	return role
}

// Start opens a conversation for the authenticated customer, seller, or admin.
// It dispatches to one of three type-specific flows based on the requested type.
func (s *MessagingService) Start(ctx context.Context, userID, role string, in StartConversationInput) (*models.ConversationDetails, error) {
	role = normalizeMessagingRole(role)
	switch strings.ToLower(strings.TrimSpace(in.Type)) {
	case "product_inquiry":
		return s.startProductInquiry(ctx, userID, role, in)
	case "order":
		return s.startOrderChat(ctx, userID, role, in)
	case "support":
		return s.startSupportChat(ctx, userID, role, in)
	default:
		// Unknown/missing type string → reject up front before touching any repository
		return nil, ErrInvalidConversation
	}
}

// startProductInquiry creates (or reuses) a customer's pre-purchase question thread about a product.
func (s *MessagingService) startProductInquiry(ctx context.Context, userID, role string, in StartConversationInput) (*models.ConversationDetails, error) {
	// Only customers may initiate a product inquiry — sellers/admins can't start one on someone's behalf
	if role != "customer" {
		return nil, ErrForbiddenConversation
	}
	// product_id is mandatory and must be non-blank
	if in.ProductID == nil || strings.TrimSpace(*in.ProductID) == "" {
		return nil, ErrInvalidConversation
	}
	productID := strings.TrimSpace(*in.ProductID)

	// Reuse pattern: check if this customer already has an OPEN inquiry for this exact product
	// (mirrors the DB's partial unique index idx_conversations_product_inquiry_open)
	existing, err := s.msg.FindOpenProductInquiry(ctx, productID, userID)
	if err == nil {
		// Found one — just return its details rather than creating a duplicate
		return s.details(ctx, existing)
	}
	if !errors.Is(err, repository.ErrConversationNotFound) {
		// Some other (unexpected) repository error — propagate it rather than treating as "not found"
		return nil, err
	}

	// No existing inquiry: look up the product to get its shop/seller and validate it's purchasable/visible
	p, err := s.orders.GetCheckoutProduct(ctx, productID)
	if err != nil {
		if errors.Is(err, repository.ErrOrderProductNotFound) {
			return nil, ErrProductNotFound
		}
		return nil, err
	}
	// Guard against starting inquiries on unpublished products or inactive shops
	if p.Status != "published" || p.ShopStatus != "active" {
		return nil, ErrProductNotFound
	}

	// Parse all the string IDs we got back into uuid.UUID — fails closed (returns raw err) if malformed,
	// since these come from trusted internal data rather than client input
	shopID, err := uuid.Parse(p.ShopID)
	if err != nil {
		return nil, err
	}
	prodUUID, err := uuid.Parse(p.ID)
	if err != nil {
		return nil, err
	}
	sellerUUID, err := uuid.Parse(p.SellerID)
	if err != nil {
		return nil, err
	}
	// This one IS client-influenced (userID from the JWT) so a parse failure maps to
	// ErrInvalidConversation rather than a raw internal error
	customerUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, ErrInvalidConversation
	}

	// Build the new conversation row matching the product_inquiry shape required by the DB CHECK constraint
	conv := &models.Conversation{
		Type:            "product_inquiry",
		Status:          "open",
		ProductID:       &prodUUID,
		ShopID:          &shopID,
		CreatedByUserID: customerUUID,
	}
	// Both sides of the chat are added as participants up front
	participants := []models.ConversationParticipant{
		{UserID: customerUUID, Role: "customer"},
		{UserID: sellerUUID, Role: "seller"},
	}
	if err := s.msg.CreateConversation(ctx, conv, participants); err != nil {
		// Race-condition handling: if creation failed (e.g. the unique index was violated because
		// another concurrent request created the same inquiry first), re-check for an existing one
		// and return that instead of surfacing a hard error to the client
		if existing, findErr := s.msg.FindOpenProductInquiry(ctx, productID, userID); findErr == nil {
			return s.details(ctx, existing)
		}
		return nil, err
	}

	// If the caller included an initial message body and/or attachments, send as the first bubble
	if err := s.maybeFirstMessage(ctx, conv.ID, customerUUID, role, in.Body, in.Attachments); err != nil {
		return nil, err
	}
	// Re-fetch through Get() rather than returning `conv` directly, so the response includes
	// participants and is built the same way a subsequent GET /conversations/{id} would return it
	return s.Get(ctx, userID, conv.ID.String())
}

// startOrderChat creates (or reuses) a thread tied to a specific order line item, for either
// the customer who bought it or the seller who's fulfilling it.
func (s *MessagingService) startOrderChat(ctx context.Context, userID, role string, in StartConversationInput) (*models.ConversationDetails, error) {
	// Only customer or seller roles may start order chats (not admin)
	if role != "customer" && role != "seller" {
		return nil, ErrForbiddenConversation
	}
	if in.OrderItemID == nil || strings.TrimSpace(*in.OrderItemID) == "" {
		return nil, ErrInvalidConversation
	}
	itemID := strings.TrimSpace(*in.OrderItemID)

	// Reuse pattern: an order-item thread already exists (DB enforces uniqueness per order_item_id).
	// AliExpress-like: keep the same thread; if it was optionally closed, reopen so chat can continue.
	existing, err := s.msg.FindByOrderItem(ctx, itemID)
	if err == nil {
		if _, perr := s.msg.GetForParticipant(ctx, existing.ID.String(), userID); perr != nil {
			return nil, ErrConversationNotFound
		}
		senderID, perr := uuid.Parse(userID)
		if perr != nil {
			return nil, ErrInvalidConversation
		}
		if existing.Status != "open" {
			if err := s.msg.ReopenConversation(ctx, existing.ID.String()); err != nil {
				return nil, err
			}
		}
		if err := s.maybeFirstMessage(ctx, existing.ID, senderID, role, in.Body, in.Attachments); err != nil {
			return nil, err
		}
		return s.Get(ctx, userID, existing.ID.String())
	}
	if !errors.Is(err, repository.ErrConversationNotFound) {
		return nil, err
	}

	// No existing thread yet: look up the order item, scoped differently depending on caller's role,
	// so a customer can only reference their own items and a seller only their own fulfillments
	var item *models.OrderItem
	var customerID uuid.UUID
	switch role {
	case "customer":
		item, err = s.orders.GetItemForCustomer(ctx, userID, itemID)
		if err != nil {
			if errors.Is(err, repository.ErrOrderItemNotFound) {
				return nil, ErrOrderItemNotFound
			}
			return nil, err
		}
		customerID, err = uuid.Parse(userID)
		if err != nil {
			return nil, ErrInvalidConversation
		}
	case "seller":
		details, err := s.orders.GetItemBySeller(ctx, userID, itemID)
		if err != nil {
			if errors.Is(err, repository.ErrOrderItemNotFound) {
				return nil, ErrOrderItemNotFound
			}
			return nil, err
		}
		item = &details.OrderItem
		// For the seller path, the customer's ID comes from the order itself (not the caller)
		customerID = details.Order.CustomerID
	}

	creator, err := uuid.Parse(userID)
	if err != nil {
		return nil, ErrInvalidConversation
	}
	// Pull the remaining context fields required by the order conversation shape from the item
	prodID := item.ProductID
	shopID := item.ShopID
	orderID := item.OrderID
	orderItemID := item.ID

	conv := &models.Conversation{
		Type:            "order",
		Status:          "open",
		ProductID:       &prodID,
		ShopID:          &shopID,
		OrderID:         &orderID,
		OrderItemID:     &orderItemID,
		CreatedByUserID: creator,
	}
	participants := []models.ConversationParticipant{
		{UserID: customerID, Role: "customer"},
		{UserID: item.SellerID, Role: "seller"},
	}
	if err := s.msg.CreateConversation(ctx, conv, participants); err != nil {
		// Same race-condition fallback as product inquiry: someone else may have created the
		// same order-item thread concurrently — re-check, re-verify participancy, and return it
		if existing, findErr := s.msg.FindByOrderItem(ctx, itemID); findErr == nil {
			if _, perr := s.msg.GetForParticipant(ctx, existing.ID.String(), userID); perr != nil {
				return nil, ErrConversationNotFound
			}
			return s.details(ctx, existing)
		}
		return nil, err
	}

	if err := s.maybeFirstMessage(ctx, conv.ID, creator, role, in.Body, in.Attachments); err != nil {
		return nil, err
	}
	return s.Get(ctx, userID, conv.ID.String())
}

// startSupportChat:
//   - admin → customer/seller (counterpart_role + counterpart_user_id required)
//   - customer/seller → admin help (counterpart fields omitted; opens a ticket)
//
// Dispatches based on the caller's role since the two directions have very different semantics
// (an admin proactively reaching out vs. a user opening a help ticket).
func (s *MessagingService) startSupportChat(ctx context.Context, userID, role string, in StartConversationInput) (*models.ConversationDetails, error) {
	switch role {
	case "admin":
		return s.startAdminSupport(ctx, userID, in)
	case "customer", "seller":
		return s.startUserSupport(ctx, userID, role, in)
	default:
		// Shouldn't normally happen given RequireRole middleware, but defends against
		// any other role value slipping through
		return nil, ErrForbiddenConversation
	}
}

// startAdminSupport handles an admin proactively opening (or joining) a support thread with a
// specific customer or seller.
func (s *MessagingService) startAdminSupport(ctx context.Context, adminID string, in StartConversationInput) (*models.ConversationDetails, error) {
	// Both counterpart fields are mandatory for the admin-initiated flow
	if in.CounterpartRole == nil || in.CounterpartUserID == nil {
		return nil, ErrInvalidConversation
	}
	counterpartRole := strings.ToLower(strings.TrimSpace(*in.CounterpartRole))
	counterpartUserID := strings.TrimSpace(*in.CounterpartUserID)
	if counterpartRole != "customer" && counterpartRole != "seller" {
		return nil, ErrInvalidConversation
	}
	if counterpartUserID == "" {
		return nil, ErrInvalidConversation
	}

	// Verify the target customer/seller actually exists (and isn't deleted, for sellers)
	// before creating anything
	if err := s.ensureCounterpartExists(ctx, counterpartRole, counterpartUserID); err != nil {
		return nil, err
	}

	// Reuse pattern: if this counterpart already has an active support case, join it as a
	// participant rather than opening a second one (matches the "one active case per
	// counterpart" partial unique index on support.cases)
	existing, _, err := s.msg.FindOpenSupportForCounterpart(ctx, counterpartRole, counterpartUserID)
	if err == nil {
		// Add this admin to the existing thread (e.g. a second admin picking up a case,
		// or the same admin re-opening it) — AddParticipant presumably no-ops or upserts
		// if already a participant
		if _, addErr := s.msg.AddParticipant(ctx, existing.ID.String(), adminID, "admin"); addErr != nil {
			return nil, addErr
		}
		adminUUID, _ := uuid.Parse(adminID)
		// NOTE: error from uuid.Parse here is silently discarded — adminID is assumed always
		// valid since it comes from the authenticated JWT, not client-supplied JSON
		if err := s.maybeFirstMessage(ctx, existing.ID, adminUUID, "admin", in.Body, in.Attachments); err != nil {
			return nil, err
		}
		return s.Get(ctx, adminID, existing.ID.String())
	}
	if !errors.Is(err, repository.ErrConversationNotFound) {
		return nil, err
	}

	// No existing case: parse both participant IDs (admin from context, counterpart from validated input)
	adminUUID, err := uuid.Parse(adminID)
	if err != nil {
		return nil, ErrInvalidConversation
	}
	counterpartUUID, err := uuid.Parse(counterpartUserID)
	if err != nil {
		return nil, ErrInvalidConversation
	}

	// Priority defaults to "normal" if not supplied, otherwise must be one of the allowed values
	priority := "normal"
	if in.Priority != nil && strings.TrimSpace(*in.Priority) != "" {
		priority = strings.ToLower(strings.TrimSpace(*in.Priority))
	}
	switch priority {
	case "low", "normal", "high", "urgent":
		// valid — fall through
	default:
		return nil, ErrInvalidConversation
	}

	// Subject is optional free text; only kept if non-blank after trimming
	var subject *string
	if in.Subject != nil {
		trimmed := strings.TrimSpace(*in.Subject)
		if trimmed != "" {
			subject = &trimmed
		}
	}

	// Build the conversation row (no product/order anchors, per the support CHECK constraint)
	conv := &models.Conversation{
		Type:            "support",
		Status:          "open",
		CreatedByUserID: adminUUID,
	}
	participants := []models.ConversationParticipant{
		{UserID: adminUUID, Role: "admin"},
		{UserID: counterpartUUID, Role: counterpartRole},
	}
	// Plus the support.cases row with workflow metadata (status/priority/subject/counterpart)
	sc := &models.SupportCase{
		OpenedByUserID:    adminUUID,
		OpenedByRole:      "admin",
		CounterpartUserID: counterpartUUID,
		CounterpartRole:   counterpartRole,
		Subject:           subject,
		Status:            "open",
		Priority:          priority,
	}
	// CreateSupportConversation presumably creates both the conversation AND the support case
	// together (likely in a single DB transaction, given they're 1:1 and both need to succeed)
	if err := s.msg.CreateSupportConversation(ctx, conv, participants, sc); err != nil {
		// Race-condition fallback again: someone else opened a case for this counterpart first
		if existing, _, findErr := s.msg.FindOpenSupportForCounterpart(ctx, counterpartRole, counterpartUserID); findErr == nil {
			// Best-effort join; errors from AddParticipant are deliberately ignored here (`_, _ =`)
			// since the primary goal — returning *a* valid conversation — still succeeds either way
			_, _ = s.msg.AddParticipant(ctx, existing.ID.String(), adminID, "admin")
			return s.Get(ctx, adminID, existing.ID.String())
		}
		return nil, err
	}

	if err := s.maybeFirstMessage(ctx, conv.ID, adminUUID, "admin", in.Body, in.Attachments); err != nil {
		return nil, err
	}
	return s.Get(ctx, adminID, conv.ID.String())
}

// startUserSupport handles a customer or seller opening a help ticket with support staff
// (they don't choose which admin — the system auto-assigns one).
func (s *MessagingService) startUserSupport(ctx context.Context, userID, role string, in StartConversationInput) (*models.ConversationDetails, error) {
	// Reuse pattern: if this user already has an active support case, just return it
	// rather than opening a duplicate ticket
	existing, _, err := s.msg.FindOpenSupportForCounterpart(ctx, role, userID)
	if err == nil {
		return s.Get(ctx, userID, existing.ID.String())
	}
	if !errors.Is(err, repository.ErrConversationNotFound) {
		return nil, err
	}

	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, ErrInvalidConversation
	}

	var subject *string
	if in.Subject != nil {
		trimmed := strings.TrimSpace(*in.Subject)
		if trimmed != "" {
			subject = &trimmed
		}
	}

	// Prefer an active admin so the ticket appears in an admin inbox immediately.
	// This picks *some* available admin automatically — the user doesn't specify one
	// (unlike startAdminSupport, where the admin explicitly targets a user)
	admin, err := s.admins.GetFirstActive(ctx)
	if err != nil {
		if errors.Is(err, repository.ErrAdminNotFound) {
			// No active admins available at all → surface as "support counterpart not found"
			return nil, ErrSupportUserNotFound
		}
		return nil, err
	}

	conv := &models.Conversation{
		Type:            "support",
		Status:          "open",
		CreatedByUserID: userUUID,
	}
	participants := []models.ConversationParticipant{
		{UserID: userUUID, Role: role},
		{UserID: admin.ID, Role: "admin"},
	}
	// Here, "counterpart" for a self-opened ticket is the user themself — since counterpart_role
	// is what identifies which customer/seller the case is about, and here that IS the caller
	sc := &models.SupportCase{
		OpenedByUserID:    userUUID,
		OpenedByRole:      role,
		CounterpartUserID: userUUID,
		CounterpartRole:   role,
		Subject:           subject,
		Status:            "open",
		Priority:          "normal", // user-opened tickets always start at normal priority (only admins can set priority explicitly)
	}
	if err := s.msg.CreateSupportConversation(ctx, conv, participants, sc); err != nil {
		if existing, _, findErr := s.msg.FindOpenSupportForCounterpart(ctx, role, userID); findErr == nil {
			return s.Get(ctx, userID, existing.ID.String())
		}
		return nil, err
	}

	if err := s.maybeFirstMessage(ctx, conv.ID, userUUID, role, in.Body, in.Attachments); err != nil {
		return nil, err
	}
	return s.Get(ctx, userID, conv.ID.String())
}

// ensureCounterpartExists validates that the target customer/seller for an admin-initiated
// support case actually exists (and, for sellers, isn't soft-deleted).
func (s *MessagingService) ensureCounterpartExists(ctx context.Context, role, id string) error {
	switch role {
	case "customer":
		_, err := s.customers.GetByID(ctx, id)
		if errors.Is(err, repository.ErrCustomerNotFound) {
			return ErrSupportUserNotFound
		}
		// NOTE: any other error (nil included) is returned as-is here — if GetByID succeeds,
		// err is nil and this correctly returns nil (no error)
		return err
	case "seller":
		seller, err := s.sellers.GetByID(ctx, id)
		if errors.Is(err, repository.ErrSellerNotFound) {
			return ErrSupportUserNotFound
		}
		if err != nil {
			return err
		}
		// Sellers get an extra check customers don't: reject soft-deleted seller accounts
		if seller.Status == "deleted" {
			return ErrSupportUserNotFound
		}
		return nil
	default:
		// Shouldn't be reachable given earlier validation, but defends against misuse of this helper
		return ErrInvalidConversation
	}
}

// maybeFirstMessage sends an optional initial message (text and/or files) right after a
// conversation is created. Nil/blank body with no attachments = no first message (valid).
func (s *MessagingService) maybeFirstMessage(
	ctx context.Context,
	conversationID, senderID uuid.UUID,
	senderRole string,
	body *string,
	attachments []ChatAttachmentInput,
) error {
	text := ""
	if body != nil {
		text = strings.TrimSpace(*body)
	}
	if text == "" && len(attachments) == 0 {
		return nil
	}
	assets, err := s.buildChatAssets(senderID, senderRole, attachments)
	if err != nil {
		return err
	}
	msg := &models.Message{
		ConversationID: conversationID,
		SenderUserID:   senderID,
		Body:           text,
		Type:           "text",
	}
	return s.msg.CreateMessage(ctx, msg, assets)
}

// buildChatAssets turns already-uploaded S3 object refs into media.media_assets rows
// (inserted later inside CreateMessage's transaction).
func (s *MessagingService) buildChatAssets(ownerID uuid.UUID, ownerRole string, in []ChatAttachmentInput) ([]models.MediaAsset, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > maxChatAttachments {
		return nil, ErrInvalidChatAttachment
	}
	ownerType := normalizeMessagingRole(ownerRole)
	if ownerType != "customer" && ownerType != "seller" && ownerType != "admin" {
		return nil, ErrInvalidChatAttachment
	}

	assets := make([]models.MediaAsset, 0, len(in))
	for _, item := range in {
		objectPath := strings.TrimSpace(item.ObjectPath)
		mimeType := strings.ToLower(strings.TrimSpace(item.MimeType))
		if objectPath == "" || mimeType == "" || item.SizeBytes < 0 {
			return nil, ErrInvalidChatAttachment
		}
		// Chat uploads must land under the chat/ prefixes from media presign folders.
		if !strings.HasPrefix(objectPath, "public/chat/") {
			return nil, ErrInvalidChatAttachment
		}
		assetType, ok := chatAssetTypeFromMime(mimeType)
		if !ok {
			return nil, ErrInvalidChatAttachment
		}
		metadata := item.Metadata
		if len(metadata) == 0 {
			metadata = json.RawMessage(`{}`)
		}
		owner := ownerID
		asset := models.MediaAsset{
			OwnerType:        ownerType,
			OwnerID:          &owner,
			AssetType:        assetType,
			Bucket:           s.bucket,
			ObjectPath:       objectPath,
			MimeType:         mimeType,
			SizeBytes:        item.SizeBytes,
			ProcessingStatus: "ready",
			ModerationStatus: "approved",
			Metadata:         metadata,
		}
		if strings.HasPrefix(objectPath, "public/") && s.s3 != nil {
			url := s.s3.PublicURL(objectPath)
			asset.CDNURL = &url
		}
		assets = append(assets, asset)
	}
	return assets, nil
}

func chatAssetTypeFromMime(mimeType string) (string, bool) {
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		return "image", true
	case mimeType == "application/pdf":
		return "document", true
	case strings.HasPrefix(mimeType, "application/"):
		return "document", true
	default:
		return "", false
	}
}

// List returns the caller's inbox.
func (s *MessagingService) List(ctx context.Context, userID string) ([]models.ConversationSummary, error) {
	// Straight pass-through to the repository — no extra business logic needed here
	return s.msg.ListForUser(ctx, userID)
}

// Get returns one conversation the user belongs to.
func (s *MessagingService) Get(ctx context.Context, userID, conversationID string) (*models.ConversationDetails, error) {
	// GetForParticipant does double duty: fetches the conversation AND verifies the
	// caller is actually a participant, in one repository call
	conv, err := s.msg.GetForParticipant(ctx, conversationID, userID)
	if err != nil {
		if errors.Is(err, repository.ErrConversationNotFound) {
			return nil, ErrConversationNotFound
		}
		return nil, err
	}
	return s.details(ctx, conv)
}

// details assembles the full response shape for a conversation: the conversation itself,
// its participants, and — if it's a support thread — the linked support case.
func (s *MessagingService) details(ctx context.Context, conv *models.Conversation) (*models.ConversationDetails, error) {
	participants, err := s.msg.ListParticipants(ctx, conv.ID.String())
	if err != nil {
		return nil, err
	}
	d := &models.ConversationDetails{
		Conversation: *conv,
		Participants: participants,
	}
	if conv.Type == "support" {
		sc, err := s.msg.GetSupportCaseByConversation(ctx, conv.ID.String())
		if err == nil {
			d.SupportCase = sc
		} else if !errors.Is(err, repository.ErrConversationNotFound) {
			// If the support case genuinely doesn't exist, that's silently tolerated (SupportCase
			// stays nil) — but any OTHER error while fetching it is treated as a real failure
			return nil, err
		}
	}
	return d, nil
}

// ListMessages returns messages and marks the conversation read for the viewer.
func (s *MessagingService) ListMessages(ctx context.Context, userID, conversationID string, limit int, before *time.Time) ([]models.Message, error) {
	// Authorization check first: confirm the caller is a participant before returning any messages
	if _, err := s.msg.GetForParticipant(ctx, conversationID, userID); err != nil {
		if errors.Is(err, repository.ErrConversationNotFound) {
			return nil, ErrConversationNotFound
		}
		return nil, err
	}
	msgs, err := s.msg.ListMessages(ctx, conversationID, limit, before)
	if err != nil {
		return nil, err
	}
	// Mark read as a side effect of viewing messages. Error is deliberately swallowed (`_ =`) —
	// a failure to update the read marker shouldn't cause the whole request (which already has
	// valid messages to return) to fail
	_ = s.msg.MarkRead(ctx, conversationID, userID)
	return msgs, nil
}

// SendMessage posts a text and/or file message into a conversation the user belongs to.
func (s *MessagingService) SendMessage(ctx context.Context, userID, role, conversationID string, in SendMessageInput) (*models.Message, error) {
	body := strings.TrimSpace(in.Body)
	if body == "" && len(in.Attachments) == 0 {
		// Need either text or at least one file (e.g. damage photo with caption optional)
		return nil, ErrInvalidConversation
	}
	// Confirm participancy before allowing a send
	conv, err := s.msg.GetForParticipant(ctx, conversationID, userID)
	if err != nil {
		if errors.Is(err, repository.ErrConversationNotFound) {
			return nil, ErrConversationNotFound
		}
		return nil, err
	}
	// Closed conversations reject new messages — you can't post into an archived thread
	if conv.Status != "open" {
		return nil, ErrInvalidConversation
	}
	senderID, err := uuid.Parse(userID)
	if err != nil {
		return nil, ErrInvalidConversation
	}
	assets, err := s.buildChatAssets(senderID, role, in.Attachments)
	if err != nil {
		return nil, err
	}
	msg := &models.Message{
		ConversationID: conv.ID,
		SenderUserID:   senderID,
		Body:           body,
		Type:           "text",
	}
	if err := s.msg.CreateMessage(ctx, msg, assets); err != nil {
		return nil, err
	}
	return msg, nil
}

// MarkRead updates the caller's last_read_at.
func (s *MessagingService) MarkRead(ctx context.Context, userID, conversationID string) error {
	// Same participancy check pattern as the other methods
	if _, err := s.msg.GetForParticipant(ctx, conversationID, userID); err != nil {
		if errors.Is(err, repository.ErrConversationNotFound) {
			return ErrConversationNotFound
		}
		return err
	}
	if err := s.msg.MarkRead(ctx, conversationID, userID); err != nil {
		// Unlike ListMessages (which swallows this error), here it's surfaced — MarkRead is
		// the entire point of this endpoint, so a failure here should be visible to the caller
		if errors.Is(err, repository.ErrNotParticipant) {
			return ErrNotParticipant
		}
		return err
	}
	return nil
}

// Close marks a conversation closed for a participant (optional freeze — not auto).
// Recommended use:
//   - product_inquiry / order: leave open by default; close only if a party chooses
//   - support: close when the ticket is resolved
// After close, SendMessage rejects new posts until Reopen (or order Start reopens automatically).
// Closing again is a no-op success. Support cases linked to the thread are closed as well.
func (s *MessagingService) Close(ctx context.Context, userID, conversationID string) (*models.ConversationDetails, error) {
	if _, err := s.msg.GetForParticipant(ctx, conversationID, userID); err != nil {
		if errors.Is(err, repository.ErrConversationNotFound) {
			return nil, ErrConversationNotFound
		}
		return nil, err
	}
	if err := s.msg.CloseConversation(ctx, conversationID); err != nil {
		if errors.Is(err, repository.ErrConversationNotFound) {
			return nil, ErrConversationNotFound
		}
		return nil, err
	}
	return s.Get(ctx, userID, conversationID)
}

// Reopen opens a previously closed conversation (optional; order Start also auto-reopens).
// Support cases that were closed are set back to open.
func (s *MessagingService) Reopen(ctx context.Context, userID, conversationID string) (*models.ConversationDetails, error) {
	if _, err := s.msg.GetForParticipant(ctx, conversationID, userID); err != nil {
		if errors.Is(err, repository.ErrConversationNotFound) {
			return nil, ErrConversationNotFound
		}
		return nil, err
	}
	if err := s.msg.ReopenConversation(ctx, conversationID); err != nil {
		if errors.Is(err, repository.ErrConversationNotFound) {
			return nil, ErrConversationNotFound
		}
		return nil, err
	}
	return s.Get(ctx, userID, conversationID)
}