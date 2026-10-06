package handlers

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"myapp/internal/middleware"
	"myapp/internal/services"
	"myapp/internal/utils"
)

type SellerHandler struct {
	sellers *services.SellerService
}

func NewSellerHandler(sellers *services.SellerService) *SellerHandler {
	return &SellerHandler{sellers: sellers}
}

type sellerRegisterRequest struct {
	CountryID   string                        `json:"country_id"`
	SellerType  string                        `json:"seller_type"`
	LegalName   string                        `json:"legal_name"`
	TradingName *string                       `json:"trading_name"`
	Email       string                        `json:"email"`
	Password    string                        `json:"password"`
	Phone       *string                       `json:"phone"`
	ImageURL    *string                       `json:"image_url"`
	Addresses   []services.SellerAddressInput `json:"addresses"`
	Shop        *services.ShopInput           `json:"shop"`

	LocalName          *string                               `json:"local_name"`
	RegistrationStatus string                                `json:"registration_status"`
	RegistrationNote   *string                               `json:"registration_note"`
	Identifiers        []services.SellerIdentifierInput      `json:"identifiers"`
	TaxStatus          string                                `json:"tax_status"`
	TaxRegistrations   []services.SellerTaxRegistrationInput `json:"tax_registrations"`
	ContactName        *string                               `json:"contact_name"`
	ContactRole        string                                `json:"contact_role"`
	ContactJobTitle    *string                               `json:"contact_job_title"`
	AuthorityConfirmed bool                                  `json:"authority_confirmed"`
	TermsAccepted      bool                                  `json:"terms_accepted"`
	MarketingOptIn     bool                                  `json:"marketing_opt_in"`
}

type sellerUpdateRequest struct {
	CountryID   string  `json:"country_id"`
	SellerType  string  `json:"seller_type"`
	LegalName   string  `json:"legal_name"`
	TradingName *string `json:"trading_name"`
	Phone       *string `json:"phone"`
	ImageURL    *string `json:"image_url"`
}

func (h *SellerHandler) Register(w http.ResponseWriter, r *http.Request) {
	var req sellerRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	details, err := h.sellers.Register(r.Context(), services.SellerRegisterInput{
		CountryID:   req.CountryID,
		SellerType:  req.SellerType,
		LegalName:   req.LegalName,
		TradingName: req.TradingName,
		Email:       req.Email,
		Password:    req.Password,
		Phone:       req.Phone,
		ImageURL:    req.ImageURL,
		Addresses:   req.Addresses,
		Shop:        req.Shop,

		LocalName:          req.LocalName,
		RegistrationStatus: req.RegistrationStatus,
		RegistrationNote:   req.RegistrationNote,
		Identifiers:        req.Identifiers,
		TaxStatus:          req.TaxStatus,
		TaxRegistrations:   req.TaxRegistrations,
		ContactName:        req.ContactName,
		ContactRole:        req.ContactRole,
		ContactJobTitle:    req.ContactJobTitle,
		AuthorityConfirmed: req.AuthorityConfirmed,
		TermsAccepted:      req.TermsAccepted,
		MarketingOptIn:     req.MarketingOptIn,
	})
	if err != nil {
		h.writeError(w, err, "could not register seller")
		return
	}
	utils.JSON(w, http.StatusCreated, details)
}

func (h *SellerHandler) VerifyEmail(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
		Code  string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	result, err := h.sellers.VerifyEmail(r.Context(), req.Email, req.Code)
	if err != nil {
		h.writeError(w, err, "could not verify email")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]string{"token": result.Token, "role": "seller"})
}

func (h *SellerHandler) ResendEmailCode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.sellers.ResendEmailCode(r.Context(), req.Email); err != nil {
		h.writeError(w, err, "could not send a new code")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]string{"message": "If this email is waiting for a code, we sent a new one."})
}

// ShopSlugAvailable handles GET /sellers/shops/slug-available?slug=... so the
// sign-up form can flag a taken shop URL before the final submit.
func (h *SellerHandler) ShopSlugAvailable(w http.ResponseWriter, r *http.Request) {
	slug, available, err := h.sellers.ShopSlugAvailable(r.Context(), r.URL.Query().Get("slug"))
	if err != nil {
		if errors.Is(err, services.ErrInvalidShop) {
			utils.Error(w, http.StatusBadRequest, "slug must contain letters or numbers")
			return
		}
		h.writeError(w, err, "could not check shop slug")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]any{"slug": slug, "available": available})
}

func (h *SellerHandler) Me(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	details, err := h.sellers.GetDetails(r.Context(), sellerID)
	if err != nil {
		h.writeError(w, err, "could not load seller")
		return
	}
	utils.JSON(w, http.StatusOK, details)
}

func (h *SellerHandler) UpdateMe(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	var req sellerUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	seller, err := h.sellers.Update(r.Context(), sellerID, services.SellerUpdateInput{
		CountryID:   req.CountryID,
		SellerType:  req.SellerType,
		LegalName:   req.LegalName,
		TradingName: req.TradingName,
		Phone:       req.Phone,
		ImageURL:    req.ImageURL,
	})
	if err != nil {
		h.writeError(w, err, "could not update seller")
		return
	}
	utils.JSON(w, http.StatusOK, seller)
}

func (h *SellerHandler) DeleteMe(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	if err := h.sellers.Delete(r.Context(), sellerID); err != nil {
		h.writeError(w, err, "could not delete seller")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]string{"message": "seller deleted"})
}

func (h *SellerHandler) AddAddress(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	var req services.SellerAddressInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	addr, err := h.sellers.AddAddress(r.Context(), sellerID, req)
	if err != nil {
		h.writeError(w, err, "could not add address")
		return
	}
	utils.JSON(w, http.StatusCreated, addr)
}

func (h *SellerHandler) UpdateAddress(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	addressID := chi.URLParam(r, "id")
	var req services.SellerAddressInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	addr, err := h.sellers.UpdateAddress(r.Context(), sellerID, addressID, req)
	if err != nil {
		h.writeError(w, err, "could not update address")
		return
	}
	utils.JSON(w, http.StatusOK, addr)
}

func (h *SellerHandler) DeleteAddress(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	addressID := chi.URLParam(r, "id")
	if err := h.sellers.DeleteAddress(r.Context(), sellerID, addressID); err != nil {
		h.writeError(w, err, "could not delete address")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]string{"message": "address deleted"})
}

// ListShops handles GET /sellers/me/shops — the seller's own shops, any status.
func (h *SellerHandler) ListShops(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	shops, err := h.sellers.ListShops(r.Context(), sellerID)
	if err != nil {
		h.writeError(w, err, "could not list shops")
		return
	}
	utils.JSON(w, http.StatusOK, shops)
}

// createshop handles the Http request for creating a new shop
func (h *SellerHandler) CreateShop(
	w http.ResponseWriter, // used to send the response back to the frontend/postman
	r *http.Request) { // contains the incoming HTTP request.

	// Get the authenticated seller/user ID from the request context
	// The seller ID normally comes from the JWT authentication middleware
	// extract the sellerID from the context of the request

	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)

	// create a variable to store the incoming request body
	var req services.ShopInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil { // If the JSON is invalid, Decode() returns an error.
		// send HTTP 400 bad request error to frontend/postman
		// because the request body is invalid
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// call the create shop service to create a new shop
	// the service will return the created shop or an error
	// if the shop is created successfully, send the shop details back to the frontend/postman
	shop, err := h.sellers.CreateShop(r.Context(), sellerID, req)
	if err != nil {
		// if there is an error, send the error back to the frontend/postman
		// and return the error message
		h.writeError(w, err, "could not create shop")
		return
	}
	// send the created shop details back to the frontend/postman
	// with HTTP 201 created status code
	utils.JSON(w, http.StatusCreated, shop)
}

func (h *SellerHandler) UpdateShop(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	shopID := chi.URLParam(r, "id")
	var req services.ShopInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	shop, err := h.sellers.UpdateShop(r.Context(), sellerID, shopID, req)
	if err != nil {
		h.writeError(w, err, "could not update shop")
		return
	}
	utils.JSON(w, http.StatusOK, shop)
}

func (h *SellerHandler) DeleteShop(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	shopID := chi.URLParam(r, "id")
	if err := h.sellers.DeleteShop(r.Context(), sellerID, shopID); err != nil {
		h.writeError(w, err, "could not delete shop")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]string{"message": "shop deleted"})
}

func (h *SellerHandler) ListDeliveryZones(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	shopID := chi.URLParam(r, "shopID")
	zones, err := h.sellers.ListDeliveryZones(r.Context(), sellerID, shopID)
	if err != nil {
		h.writeError(w, err, "could not list delivery zones")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]any{"zones": zones})
}

func (h *SellerHandler) ReplaceDeliveryZones(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	shopID := chi.URLParam(r, "shopID")
	var req services.DeliveryZonesReplaceInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	zones, err := h.sellers.ReplaceDeliveryZones(r.Context(), sellerID, shopID, req)
	if err != nil {
		h.writeError(w, err, "could not save delivery zones")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]any{"zones": zones})
}

// RequireActive refuses seller routes when an admin has suspended the account.
// Other roles pass through, so shared routes can use the same middleware.
func (h *SellerHandler) RequireActive(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role, _ := r.Context().Value(middleware.RoleContextKey).(string)
		if strings.ToLower(role) != "seller" {
			next.ServeHTTP(w, r)
			return
		}
		sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
		status, err := h.sellers.AccountStatus(r.Context(), sellerID)
		if err != nil || status != "active" {
			utils.Error(w, http.StatusForbidden, services.ErrSellerSuspended.Error())
			return
		}
		next.ServeHTTP(w, r)
	})
}

// AdminList handles GET /admin/sellers?status=&q=&limit=&offset=.
func (h *SellerHandler) AdminList(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	limit, _ := strconv.Atoi(query.Get("limit"))
	offset, _ := strconv.Atoi(query.Get("offset"))
	items, total, err := h.sellers.AdminListSellers(r.Context(), query.Get("status"), query.Get("q"), limit, offset)
	if err != nil {
		h.writeError(w, err, "could not list sellers")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

// AdminGet handles GET /admin/sellers/{id}.
func (h *SellerHandler) AdminGet(w http.ResponseWriter, r *http.Request) {
	record, err := h.sellers.AdminGetSeller(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		h.writeError(w, err, "could not load seller")
		return
	}
	utils.JSON(w, http.StatusOK, record)
}

// AdminSetStatus handles PATCH /admin/sellers/{id}/status.
func (h *SellerHandler) AdminSetStatus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	item, err := h.sellers.AdminSetSellerStatus(r.Context(), chi.URLParam(r, "id"), req.Status)
	if err != nil {
		h.writeError(w, err, "could not update seller")
		return
	}
	utils.JSON(w, http.StatusOK, item)
}

// AdminSetVerification handles PATCH /admin/sellers/{id}/verification.
func (h *SellerHandler) AdminSetVerification(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	item, err := h.sellers.AdminSetSellerVerification(r.Context(), chi.URLParam(r, "id"), req.Status)
	if err != nil {
		h.writeError(w, err, "could not update seller verification")
		return
	}
	utils.JSON(w, http.StatusOK, item)
}

func (h *SellerHandler) writeError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, services.ErrInvalidInput):
		utils.Error(w, http.StatusBadRequest, "legal_name, email required; password must be at least 8 characters")
	case errors.Is(err, services.ErrInvalidCountry):
		utils.Error(w, http.StatusBadRequest, "invalid country_id")
	case errors.Is(err, services.ErrSellerRegistrationDisabled):
		utils.Error(w, http.StatusForbidden, "seller registration is disabled for this country")
	case errors.Is(err, services.ErrInvalidSellerSignup):
		utils.Error(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, services.ErrInvalidAddress):
		utils.Error(w, http.StatusBadRequest, "address requires country_id, line1, city; address_type must be pickup|return|both|registered")
	case errors.Is(err, services.ErrInvalidShop):
		utils.Error(w, http.StatusBadRequest, "shop name is required; at least one delivery zone is required; latitude and longitude must be sent together (-90..90, -180..180); timezone must be a valid IANA name; delivery zones need max_km > 0, price_amount >= 0, estimated_days >= 0, unique max_km, and a known currency (0 price = free, 0 days = same day and needs cutoff_time as HH:MM)")
	case errors.Is(err, services.ErrInvalidCurrency):
		utils.Error(w, http.StatusBadRequest, "currency must be the shop's country currency")
	case errors.Is(err, services.ErrSellerEmailUnconfirmed):
		utils.Error(w, http.StatusForbidden, services.ErrSellerEmailUnconfirmed.Error())
	case errors.Is(err, services.ErrSellerSuspended):
		utils.Error(w, http.StatusForbidden, services.ErrSellerSuspended.Error())
	case errors.Is(err, services.ErrSellerAccountStatus):
		utils.Error(w, http.StatusBadRequest, services.ErrSellerAccountStatus.Error())
	case errors.Is(err, services.ErrSellerVerificationStatus):
		utils.Error(w, http.StatusBadRequest, services.ErrSellerVerificationStatus.Error())
	case errors.Is(err, services.ErrSellerClosed):
		utils.Error(w, http.StatusBadRequest, services.ErrSellerClosed.Error())
	case errors.Is(err, services.ErrSellerEmailCodeWrong),
		errors.Is(err, services.ErrSellerEmailCodeExpired),
		errors.Is(err, services.ErrSellerEmailCodeLocked),
		errors.Is(err, services.ErrSellerEmailCodeWait):
		utils.Error(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, services.ErrSellerEmailAlreadyVerified):
		utils.Error(w, http.StatusConflict, services.ErrSellerEmailAlreadyVerified.Error())
	case errors.Is(err, services.ErrSellerConflict):
		utils.Error(w, http.StatusConflict, "email already registered")
	case errors.Is(err, services.ErrShopConflict):
		utils.Error(w, http.StatusConflict, "shop slug already exists")
	case errors.Is(err, services.ErrSellerNotFound):
		utils.Error(w, http.StatusNotFound, "seller not found")
	case errors.Is(err, services.ErrShopNotFound):
		utils.Error(w, http.StatusNotFound, "shop not found")
	case errors.Is(err, services.ErrSellerAddrNotFound):
		utils.Error(w, http.StatusNotFound, "address not found")
	default:
		log.Printf("seller handler error: %v", err)
		utils.Error(w, http.StatusInternalServerError, fallback)
	}
}
