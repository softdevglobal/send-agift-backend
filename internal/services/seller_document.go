package services

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	"myapp/internal/repository"
)

// SellerDocumentPrefix is where business documents are uploaded. It is not
// public: documents are read through a short-lived signed link.
const SellerDocumentPrefix = "private/sellers/documents/"

var (
	// ErrInvalidSellerDocument is a document that is not an uploaded PDF, JPG or PNG.
	ErrInvalidSellerDocument = errors.New("upload a PDF, JPG or PNG business document")
	// ErrNoSellerApplication is a seller with no application on file.
	ErrNoSellerApplication = errors.New("seller has no application")
	// ErrNoSellerDocument is an application with no document uploaded.
	ErrNoSellerDocument = errors.New("no business document uploaded")
	// ErrDocumentsUnavailable is document storage that is not set up.
	ErrDocumentsUnavailable = errors.New("document storage is not configured")
)

var sellerDocumentTypes = map[string]bool{"application/pdf": true, "image/jpeg": true, "image/png": true}

// SellerDocumentInput is a business document the seller has uploaded.
type SellerDocumentInput struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
}

// AttachDocument records the business registration evidence on a seller's
// application, replacing any earlier one.
func (s *SellerVerificationService) AttachDocument(ctx context.Context, sellerID string, in SellerDocumentInput) error {
	id, err := uuid.Parse(sellerID)
	if err != nil {
		return ErrSellerNotFound
	}
	in.Key = strings.TrimSpace(in.Key)
	in.Name = cut(in.Name, 200)
	in.ContentType = strings.ToLower(strings.TrimSpace(in.ContentType))
	if !strings.HasPrefix(in.Key, SellerDocumentPrefix) || strings.Contains(in.Key, "..") ||
		in.Name == "" || !sellerDocumentTypes[in.ContentType] {
		return ErrInvalidSellerDocument
	}
	previous, err := s.sellers.SetApplicationDocument(ctx, id, in.Key, in.Name, in.ContentType)
	if errors.Is(err, repository.ErrSellerApplicationNotFound) {
		return ErrNoSellerApplication
	}
	if err != nil {
		return err
	}
	if previous != nil && *previous != in.Key && s.documents != nil {
		if err := s.documents.Delete(ctx, *previous); err != nil {
			log.Printf("seller %s old document: %v", sellerID, err)
		}
	}
	return nil
}

// AdminDocumentURL is a short-lived link to a seller's business document.
func (s *SellerVerificationService) AdminDocumentURL(ctx context.Context, sellerID string) (string, error) {
	id, err := uuid.Parse(sellerID)
	if err != nil {
		return "", ErrSellerNotFound
	}
	if s.documents == nil {
		return "", ErrDocumentsUnavailable
	}
	key, err := s.sellers.ApplicationDocumentKey(ctx, id)
	if errors.Is(err, repository.ErrSellerApplicationNotFound) {
		return "", ErrNoSellerApplication
	}
	if err != nil {
		return "", err
	}
	if key == nil {
		return "", ErrNoSellerDocument
	}
	return s.documents.PresignGetURL(ctx, *key, 10*time.Minute)
}
