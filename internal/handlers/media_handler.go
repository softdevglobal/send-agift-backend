package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"myapp/internal/services"
	"myapp/internal/utils"
)

type MediaHandler struct {
	s3 *services.S3Service
}

func NewMediaHandler(s3 *services.S3Service) *MediaHandler {
	return &MediaHandler{s3: s3}
}

type presignUploadRequest struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Folder      string `json:"folder"`
}

type presignUploadResponse struct {
	UploadURL string `json:"upload_url"`
	Key       string `json:"key"`
	PublicURL string `json:"public_url,omitempty"`
}

// allowedFolders maps a client-supplied folder name to its storage prefix.
// Folders under "public/" are readable by anyone via PublicURL; anything
// else stays private and must be read back through GetURL.
var allowedFolders = map[string]string{
	"seller-profile": "public/sellers",
	"shop-image":     "public/shops",
	"product-image":  "public/products",
}

// PresignUpload issues a short-lived URL the client can PUT a file to directly.
func (h *MediaHandler) PresignUpload(w http.ResponseWriter, r *http.Request) {
	var req presignUploadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		utils.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Filename == "" || req.ContentType == "" {
		utils.Error(w, http.StatusBadRequest, "filename and content_type are required")
		return
	}
	prefix, ok := allowedFolders[req.Folder]
	if !ok {
		utils.Error(w, http.StatusBadRequest, "unsupported folder")
		return
	}

	key := prefix + "/" + uuid.NewString() + "-" + req.Filename
	url, err := h.s3.PresignPutURL(r.Context(), key, req.ContentType, 10*time.Minute)
	if err != nil {
		utils.Error(w, http.StatusInternalServerError, "could not create upload url")
		return
	}

	resp := presignUploadResponse{UploadURL: url, Key: key}
	if strings.HasPrefix(prefix, "public/") {
		resp.PublicURL = h.s3.PublicURL(key)
	}
	utils.JSON(w, http.StatusOK, resp)
}

// GetURL returns a temporary signed URL to read an object by key.
func (h *MediaHandler) GetURL(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key == "" {
		utils.Error(w, http.StatusBadRequest, "key is required")
		return
	}
	url, err := h.s3.PresignGetURL(r.Context(), key, 15*time.Minute)
	if err != nil {
		utils.Error(w, http.StatusInternalServerError, "could not create download url")
		return
	}
	utils.JSON(w, http.StatusOK, map[string]string{"url": url})
}
