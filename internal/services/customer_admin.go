package services

import (
	"context"

	"myapp/internal/models"
	"myapp/internal/repository"
)

// UseAdminReads wires order headers into the customer record an admin opens.
func (s *CustomerService) UseAdminReads(orders *repository.OrderRepository) {
	s.orders = orders
}

// AdminGetCustomer returns the full customer record: profile, recipients,
// saved gifts, and orders.
func (s *CustomerService) AdminGetCustomer(ctx context.Context, id string) (*models.AdminCustomerRecord, error) {
	details, err := s.GetDetails(ctx, id)
	if err != nil {
		return nil, err
	}
	if details.Addresses == nil {
		details.Addresses = []models.CustomerAddress{}
	}
	country, err := s.countries.GetByID(ctx, details.CountryID.String())
	if err != nil {
		return nil, err
	}
	recipients, err := s.customers.ListRecipients(ctx, id)
	if err != nil {
		return nil, err
	}
	recipientDetails := make([]models.RecipientDetails, 0, len(recipients))
	for _, recipient := range recipients {
		addresses, err := s.customers.ListRecipientAddresses(ctx, recipient.ID.String())
		if err != nil {
			return nil, err
		}
		if addresses == nil {
			addresses = []models.RecipientAddress{}
		}
		recipientDetails = append(recipientDetails, models.RecipientDetails{
			Recipient: recipient,
			Addresses: addresses,
		})
	}
	saved, err := s.customers.ListSavedGifts(ctx, id, true)
	if err != nil {
		return nil, err
	}
	if saved == nil {
		saved = []models.SavedGiftDetails{}
	}
	orders := []models.Order{}
	if s.orders != nil {
		orders, err = s.orders.ListByCustomer(ctx, id)
		if err != nil {
			return nil, err
		}
	}
	return &models.AdminCustomerRecord{
		CustomerDetails: *details,
		CountryName:     country.Name,
		Recipients:      recipientDetails,
		SavedGifts:      saved,
		Orders:          orders,
	}, nil
}
