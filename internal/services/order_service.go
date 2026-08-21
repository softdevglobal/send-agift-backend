package services

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"myapp/internal/models"
	"myapp/internal/repository"
)

var (
	ErrOrderNotFound       = errors.New("order not found")
	ErrInvalidOrder        = errors.New("invalid order")
	ErrOrderProduct        = errors.New("product not available")
	ErrOrderCurrencyMix    = errors.New("items must share the same currency")
	ErrOrderProductVisibility = errors.New("product not visible for this customer type")
	ErrOrderNotCancellable    = errors.New("order cannot be cancelled")
)

type OrderService struct {
	orders    *repository.OrderRepository
	customers *repository.CustomerRepository
	countries *repository.CountryRepository
}

func NewOrderService(
	orders *repository.OrderRepository,
	customers *repository.CustomerRepository,
	countries *repository.CountryRepository,
) *OrderService {
	return &OrderService{orders: orders, customers: customers, countries: countries}
}

type OrderItemInput struct {
	ProductID string `json:"product_id"`
	Quantity  int    `json:"quantity"`
}

type OrderCreateInput struct {
	RecipientID     *string         `json:"recipient_id"`
	CountryID       string          `json:"country_id"`
	CustomerType    string          `json:"customer_type"`
	DeliveryDate    string          `json:"delivery_date"`
	GiftMessage     *string         `json:"gift_message"`
	MediaGreetingID *string         `json:"media_greeting_id"`
	DeliveryAmount  *int            `json:"delivery_amount"`
	Items           []OrderItemInput `json:"items"`
}

func (s *OrderService) Create(ctx context.Context, customerID string, in OrderCreateInput) (*models.OrderDetails, error) {
	if _, err := s.customers.GetByID(ctx, customerID); err != nil {
		if errors.Is(err, repository.ErrCustomerNotFound) {
			return nil, ErrCustomerNotFound
		}
		return nil, err
	}

	cid, err := uuid.Parse(customerID)
	if err != nil {
		return nil, ErrCustomerNotFound
	}

	countryID, err := uuid.Parse(strings.TrimSpace(in.CountryID))
	if err != nil {
		return nil, ErrInvalidCountry
	}
	if _, err := s.countries.GetByID(ctx, countryID.String()); err != nil {
		if errors.Is(err, repository.ErrCountryNotFound) {
			return nil, ErrInvalidCountry
		}
		return nil, err
	}

	customerType := strings.TrimSpace(in.CustomerType)
	if customerType == "" {
		customerType = "personal"
	}
	if customerType != "personal" && customerType != "corporate" {
		return nil, ErrInvalidOrder
	}

	deliveryDate, err := repository.ParseDate(in.DeliveryDate)
	if err != nil || deliveryDate == nil {
		return nil, ErrInvalidOrder
	}

	var recipientID *uuid.UUID
	if in.RecipientID != nil && strings.TrimSpace(*in.RecipientID) != "" {
		rec, err := s.customers.GetRecipientByID(ctx, customerID, strings.TrimSpace(*in.RecipientID))
		if err != nil {
			if errors.Is(err, repository.ErrRecipientNotFound) {
				return nil, ErrRecipientNotFound
			}
			return nil, err
		}
		recipientID = &rec.ID
	}

	var mediaGreetingID *uuid.UUID
	if in.MediaGreetingID != nil && strings.TrimSpace(*in.MediaGreetingID) != "" {
		mid, err := uuid.Parse(strings.TrimSpace(*in.MediaGreetingID))
		if err != nil {
			return nil, ErrInvalidOrder
		}
		mediaGreetingID = &mid
	}

	if len(in.Items) == 0 {
		return nil, ErrInvalidOrder
	}

	deliveryAmount := 0
	if in.DeliveryAmount != nil {
		if *in.DeliveryAmount < 0 {
			return nil, ErrInvalidOrder
		}
		deliveryAmount = *in.DeliveryAmount
	}

	items := make([]models.OrderItem, 0, len(in.Items))
	subtotal := 0
	currency := ""

	for _, line := range in.Items {
		if line.Quantity < 1 {
			return nil, ErrInvalidOrder
		}
		pid, err := uuid.Parse(strings.TrimSpace(line.ProductID))
		if err != nil {
			return nil, ErrOrderProduct
		}
		snap, err := s.orders.GetCheckoutProduct(ctx, pid.String())
		if err != nil {
			if errors.Is(err, repository.ErrOrderProductNotFound) {
				return nil, ErrOrderProduct
			}
			return nil, err
		}
		if snap.Status != "published" || snap.ShopStatus != "active" {
			return nil, ErrOrderProduct
		}
		if snap.CustomerTypeVisibility != "both" && snap.CustomerTypeVisibility != customerType {
			return nil, ErrOrderProductVisibility
		}
		if currency == "" {
			currency = snap.Currency
		} else if snap.Currency != currency {
			return nil, ErrOrderCurrencyMix
		}

		sellerID, _ := uuid.Parse(snap.SellerID)
		shopID, _ := uuid.Parse(snap.ShopID)
		lineTotal := snap.PriceAmount * line.Quantity
		subtotal += lineTotal
		items = append(items, models.OrderItem{
			SellerID:         sellerID,
			ShopID:           shopID,
			ProductID:        pid,
			Quantity:         line.Quantity,
			UnitAmount:       snap.PriceAmount,
			TotalAmount:      lineTotal,
			FulfilmentStatus: "pending",
		})
	}

	order := &models.Order{
		OrderNumber:     newOrderNumber(),
		CustomerID:      cid,
		RecipientID:     recipientID,
		CountryID:       countryID,
		CustomerType:    customerType,
		DeliveryDate:    *deliveryDate,
		Status:          "pending_payment",
		SubtotalAmount:  subtotal,
		DeliveryAmount:  deliveryAmount,
		TotalAmount:     subtotal + deliveryAmount,
		Currency:        currency,
		GiftMessage:     in.GiftMessage,
		MediaGreetingID: mediaGreetingID,
	}

	if err := s.orders.Create(ctx, order, items); err != nil {
		if errors.Is(err, repository.ErrOrderDuplicate) {
			order.OrderNumber = newOrderNumber()
			if err := s.orders.Create(ctx, order, items); err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
	}

	return &models.OrderDetails{Order: *order, Items: items}, nil
}

func (s *OrderService) List(ctx context.Context, customerID string) ([]models.Order, error) {
	if _, err := s.customers.GetByID(ctx, customerID); err != nil {
		if errors.Is(err, repository.ErrCustomerNotFound) {
			return nil, ErrCustomerNotFound
		}
		return nil, err
	}
	return s.orders.ListByCustomer(ctx, customerID)
}

func (s *OrderService) Get(ctx context.Context, customerID, orderID string) (*models.OrderDetails, error) {
	order, err := s.orders.GetByIDForCustomer(ctx, customerID, orderID)
	if err != nil {
		if errors.Is(err, repository.ErrOrderNotFound) {
			return nil, ErrOrderNotFound
		}
		return nil, err
	}
	items, err := s.orders.ListItems(ctx, order.ID.String())
	if err != nil {
		return nil, err
	}
	return &models.OrderDetails{Order: *order, Items: items}, nil
}

func (s *OrderService) Cancel(ctx context.Context, customerID, orderID string) (*models.OrderDetails, error) {
	err := s.orders.CancelForCustomer(ctx, customerID, orderID)
	if err != nil {
		if errors.Is(err, repository.ErrOrderNotFound) {
			return nil, ErrOrderNotFound
		}
		if errors.Is(err, repository.ErrOrderNotCancellable) {
			return nil, ErrOrderNotCancellable
		}
		return nil, err
	}
	return s.Get(ctx, customerID, orderID)
}

func newOrderNumber() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("SAG-%s-%s", time.Now().UTC().Format("20060102"), strings.ToUpper(hex.EncodeToString(b)))
}
