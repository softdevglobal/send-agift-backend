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

// ProductReviewHandler serves AliExpress-style product reviews.
type ProductReviewHandler struct {
	reviews   *services.ProductReviewService
	jwtSecret string
}

func NewProductReviewHandler(reviews *services.ProductReviewService, jwtSecret string) *ProductReviewHandler {
	return &ProductReviewHandler{reviews: reviews, jwtSecret: jwtSecret}
}

// Create handles POST /customers/me/order-items/{orderItemId}/reviews.
func (h *ProductReviewHandler) Create(w http.ResponseWriter, r *http.Request) {
	customerID, _ := r.Context().Value(middleware.UserIDContextKey).(string) // customerID is the user ID of the customer who is creating the review
	orderItemID := chi.URLParam(r, "orderItemId") // orderItemID is the ID of the order item that the customer is reviewing
	var req services.CreateReviewInput // req is the request body for creating a review
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil { // decode the request body into the req variable
		utils.Error(w, http.StatusBadRequest, "invalid request body") // return a bad request error if the request body is invalid
		return
	}
	details, err := h.reviews.Create(r.Context(), customerID, orderItemID, req)
	if err != nil {
		h.writeError(w, err, "could not create review")
		return
	}
	utils.JSON(w, http.StatusCreated, details)
}

// Update handles PUT /customers/me/reviews/{id}.
func (h *ProductReviewHandler) Update(w http.ResponseWriter, r *http.Request) { // update is the handler for updating a review
	customerID, _ := r.Context().Value(middleware.UserIDContextKey).(string) // customerID is the user ID of the customer who is updating the review
	reviewID := chi.URLParam(r, "id") // reviewID is the ID of the review that the customer is updating
	var req services.UpdateReviewInput // req is the request body for updating a review
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil { // decode the request body into the req variable
		utils.Error(w, http.StatusBadRequest, "invalid request body") // return a bad request error if the request body is invalid
		return
	}
	details, err := h.reviews.Update(r.Context(), customerID, reviewID, req) // update the review in the database
	if err != nil {
		h.writeError(w, err, "could not update review") // return an error if the review could not be updated		
		return
	}
	utils.JSON(w, http.StatusOK, details)
}

// Delete handles DELETE /customers/me/reviews/{id}.
func (h *ProductReviewHandler) Delete(w http.ResponseWriter, r *http.Request) { // delete is the handler for deleting a review
	customerID, _ := r.Context().Value(middleware.UserIDContextKey).(string) // customerID is the user ID of the customer who is deleting the review
	reviewID := chi.URLParam(r, "id") // reviewID is the ID of the review that the customer is deleting
	if err := h.reviews.Delete(r.Context(), customerID, reviewID); err != nil {
		h.writeError(w, err, "could not delete review") // return an error if the review could not be deleted
		return
	}
	utils.JSON(w, http.StatusOK, map[string]string{"message": "review deleted"})
}

// GetMine handles GET /customers/me/reviews/{id}.
func (h *ProductReviewHandler) GetMine(w http.ResponseWriter, r *http.Request) { // getMine is the handler for getting a review
	customerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	reviewID := chi.URLParam(r, "id") // reviewID is the ID of the review that the customer is getting
	details, err := h.reviews.GetMine(r.Context(), customerID, reviewID) // get the review from the database
	if err != nil {
		h.writeError(w, err, "could not get review") // return an error if the review could not be gotten
		return
	}
	utils.JSON(w, http.StatusOK, details) // return the review in the response body
}

// ListMine handles GET /customers/me/reviews.
func (h *ProductReviewHandler) ListMine(w http.ResponseWriter, r *http.Request) {
	customerID, _ := r.Context().Value(middleware.UserIDContextKey).(string) // customerID is the user ID of the customer who is listing the reviews
	list, err := h.reviews.ListMine(r.Context(), customerID, h.listFilter(r)) // list the reviews from the database
	if err != nil {
		h.writeError(w, err, "could not list reviews") // return an error if the reviews could not be listed
		return
	}
	utils.JSON(w, http.StatusOK, list) // return the reviews in the response body
}

// ListByProduct handles GET /products/{productId}/reviews.
func (h *ProductReviewHandler) ListByProduct(w http.ResponseWriter, r *http.Request) {
	f := h.listFilter(r) // f is the filter for listing the reviews
	f.ProductID = chi.URLParam(r, "productId") // productID is the ID of the product that the reviews are for
	list, err := h.reviews.ListPublic(r.Context(), f, h.optionalCustomerID(r)) // list the reviews from the database
	if err != nil {
		h.writeError(w, err, "could not list reviews") // return an error if the reviews could not be listed
		return
	}
	utils.JSON(w, http.StatusOK, list) // return the reviews in the response body
}

// SummaryByProduct handles GET /products/{productId}/reviews/summary.
func (h *ProductReviewHandler) SummaryByProduct(w http.ResponseWriter, r *http.Request) {
	productID := chi.URLParam(r, "productId") // productID is the ID of the product that the Reviews are for
	summary, err := h.reviews.Summary(r.Context(), productID) // get the summary of the reviews from the database
	if err != nil {
		h.writeError(w, err, "could not get review summary") // return an error if the review summary could not be gotten
		return
	}
	utils.JSON(w, http.StatusOK, summary)
}

// ListByShop handles GET /shops/{shopId}/reviews.
func (h *ProductReviewHandler) ListByShop(w http.ResponseWriter, r *http.Request) {
	f := h.listFilter(r) // f is the filter for listing the reviews
	f.ShopID = chi.URLParam(r, "shopId") // shopID is the ID of the shop that the reviews are for
	list, err := h.reviews.ListPublic(r.Context(), f, h.optionalCustomerID(r)) // list the reviews from the database
	if err != nil {
		h.writeError(w, err, "could not list reviews") // return an error if the reviews could not be listed
		return
	}
	utils.JSON(w, http.StatusOK, list) // return the reviews in the response body
}

// GetPublic handles GET /reviews/{id}.
func (h *ProductReviewHandler) GetPublic(w http.ResponseWriter, r *http.Request) {
	reviewID := chi.URLParam(r, "id") // reviewID is the ID of the review that the customer is getting
	details, err := h.reviews.GetPublic(r.Context(), reviewID, h.optionalCustomerID(r)) // get the review from the database
	if err != nil {
		h.writeError(w, err, "could not get review") // return an error if the review could not be gotten
		return
	}
	utils.JSON(w, http.StatusOK, details) // return the review in the response body
}

// Vote handles PUT /reviews/{id}/vote.
func (h *ProductReviewHandler) Vote(w http.ResponseWriter, r *http.Request) {
	customerID, _ := r.Context().Value(middleware.UserIDContextKey).(string)
	reviewID := chi.URLParam(r, "id") // reviewID is the ID of the review that the customer is voting on
	var req services.VoteInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil { // decode the request body into the req variable
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	res, err := h.reviews.Vote(r.Context(), customerID, reviewID, req)
	if err != nil {
		h.writeError(w, err, "could not vote on review") // return an error if the review could not be voted on
		return
	}
	utils.JSON(w, http.StatusOK, res) // return the result of the vote in the response body
}	

// ClearVote handles DELETE /reviews/{id}/vote.
func (h *ProductReviewHandler) ClearVote(w http.ResponseWriter, r *http.Request) {
	customerID, _ := r.Context().Value(middleware.UserIDContextKey).(string) // customerID is the user ID of the customer who is clearing the vote
	reviewID := chi.URLParam(r, "id") // reviewID is the ID of the review that the customer is clearing the vote on
	res, err := h.reviews.ClearVote(r.Context(), customerID, reviewID) // clear the vote from the database
	if err != nil {
		h.writeError(w, err, "could not clear vote") // return an error if the vote could not be cleared
		return
	}
	utils.JSON(w, http.StatusOK, res) // return the result of the vote clearing in the response body
}

// ListForSeller handles GET /sellers/me/reviews.
func (h *ProductReviewHandler) ListForSeller(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string) // sellerID is the user ID of the seller who is listing the reviews
	list, err := h.reviews.ListForSeller(r.Context(), sellerID, h.listFilter(r)) // list the reviews from the database
	if err != nil {
		h.writeError(w, err, "could not list reviews") // return an error if the reviews could not be listed
		return
	}
	utils.JSON(w, http.StatusOK, list) // return the reviews in the response body
}

// GetForSeller handles GET /sellers/me/reviews/{id}.
func (h *ProductReviewHandler) GetForSeller(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string) // sellerID is the user ID of the seller who is getting the review
	reviewID := chi.URLParam(r, "id") // reviewID is the ID of the review that the seller is getting
	details, err := h.reviews.GetForSeller(r.Context(), sellerID, reviewID) // get the review from the database
	if err != nil {
		h.writeError(w, err, "could not get review") // return an error if the review could not be gotten
		return
	}
	utils.JSON(w, http.StatusOK, details)
}

// Reply handles PUT /sellers/me/reviews/{id}/reply.
func (h *ProductReviewHandler) Reply(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string) // sellerID is the user ID of the seller who is replying to the review
	reviewID := chi.URLParam(r, "id") // reviewID is the ID of the review that the seller is replying to
	var req services.SellerReplyInput // req is the request body for replying to the review
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body") // return a bad request error if the request body is invalid
		return
	}
	details, err := h.reviews.ReplyAsSeller(r.Context(), sellerID, reviewID, req)
	if err != nil {
		h.writeError(w, err, "could not reply to review") // return an error if the review could not be replied to
		return
	}
	utils.JSON(w, http.StatusOK, details)
}

// ClearReply handles DELETE /sellers/me/reviews/{id}/reply.
func (h *ProductReviewHandler) ClearReply(w http.ResponseWriter, r *http.Request) {
	sellerID, _ := r.Context().Value(middleware.UserIDContextKey).(string) // sellerID is the user ID of the seller who is clearing the reply
	reviewID := chi.URLParam(r, "id")
	details, err := h.reviews.ClearSellerReply(r.Context(), sellerID, reviewID)
	if err != nil {
		h.writeError(w, err, "could not clear reply")
		return
	}
	utils.JSON(w, http.StatusOK, details)
}

func (h *ProductReviewHandler) listFilter(r *http.Request) services.ReviewListFilter { // listFilter is the filter for listing the reviews
	q := r.URL.Query() // q is the query parameters for listing the reviews
	limit := 0 // limit is the limit for listing the reviews
	if raw := q.Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			limit = n // limit is the limit for listing the reviews
		}
	}
	return services.ReviewListFilter{ // return the filter for listing the reviews
		Cursor: q.Get("cursor"), // cursor is the cursor for listing the reviews
		Limit:  limit, // limit is the limit for listing the reviews
	}
}

func (h *ProductReviewHandler) optionalCustomerID(r *http.Request) string { // optionalCustomerID is the customer ID for listing the reviews
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") { // return an empty string if the header is not a bearer token
		return ""
	}
	tokenStr := strings.TrimPrefix(header, "Bearer ")
	claims, err := utils.ParseJWT(tokenStr, h.jwtSecret) // parse the JWT token into the claims variable
	if err != nil || claims.Role != "customer" {
		return "" // return an empty string if the claims are not a customer
	}
	return claims.Subject
}

func (h *ProductReviewHandler) writeError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, services.ErrInvalidProductReview):
		utils.Error(w, http.StatusBadRequest, "ratings must be 1–5; media[] requires object_path under public/reviews/ and image/* or video/* mime_type")
	case errors.Is(err, services.ErrInvalidCursor):
		utils.Error(w, http.StatusBadRequest, "invalid cursor")
	case errors.Is(err, services.ErrProductReviewNotEligible):
		utils.Error(w, http.StatusBadRequest, "order item must be delivered and belong to you")
	case errors.Is(err, services.ErrProductReviewDuplicate):
		utils.Error(w, http.StatusConflict, "a review already exists for this order item")
	case errors.Is(err, services.ErrProductReviewNotFound):
		utils.Error(w, http.StatusNotFound, "review not found")
	case errors.Is(err, services.ErrProductReviewVoteNotFound):
		utils.Error(w, http.StatusNotFound, "vote not found")
	case errors.Is(err, services.ErrProductReviewForbidden):
		utils.Error(w, http.StatusForbidden, "forbidden")
	default:
		log.Printf("product review handler error: %v", err)
		utils.Error(w, http.StatusInternalServerError, fallback)
	}
}
