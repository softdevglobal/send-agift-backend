package services

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestSellerSeesOnlyTheirOwnCustomers(t *testing.T) {
	f := newShopFixture(t)
	seller := f.seller()
	product := f.product(seller)
	other := f.seller()
	otherProduct := f.product(other)

	regular := f.customer("")
	first := f.mustOrder(regular, product)
	f.pay(first.ID)
	second := f.mustOrder(regular, product)
	f.pay(second.ID)
	// The same customer's order with another seller stays out of view.
	f.pay(f.mustOrder(regular, otherProduct).ID)
	// A cancelled line counts as an order but not as spend.
	cancelled := f.mustOrder(regular, product)
	f.exec(`update marketplace.order_items set fulfilment_status = 'cancelled' where order_id = $1`, cancelled.ID)

	stranger := f.customer("")
	f.pay(f.mustOrder(stranger, otherProduct).ID)

	list, err := f.orders.ListCustomersForSeller(f.ctx, seller.String())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].CustomerID != regular {
		t.Fatalf("seller's customers = %+v, want only the regular", list)
	}
	c := list[0]
	if c.OrdersCount != 3 || c.ItemsCount != 2 {
		t.Fatalf("orders %d items %d, want 3 and 2", c.OrdersCount, c.ItemsCount)
	}
	var lineTotal int
	f.scan(&lineTotal, `select total_amount from marketplace.order_items where order_id = $1`, first.ID)
	if len(c.Spent) != 1 || c.Spent[0].Amount != 2*lineTotal {
		t.Fatalf("spent = %+v, want %d", c.Spent, 2*lineTotal)
	}

	d, err := f.orders.GetCustomerForSeller(f.ctx, seller.String(), regular.String())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Orders) != 3 {
		t.Fatalf("orders = %d, want 3", len(d.Orders))
	}
	for _, o := range d.Orders {
		for _, l := range o.Items {
			if l.ProductName == "" || l.ShopName == "" {
				t.Fatalf("line missing its product or shop: %+v", l)
			}
		}
		if o.OrderID == cancelled.ID && o.Amount != 0 {
			t.Fatalf("cancelled order amount = %d", o.Amount)
		}
	}

	// Someone who never ordered from this seller cannot be looked up.
	for _, id := range []string{stranger.String(), uuid.NewString(), "not-a-uuid"} {
		if _, err := f.orders.GetCustomerForSeller(f.ctx, seller.String(), id); !errors.Is(err, ErrSellerCustomerNotFound) {
			t.Fatalf("customer %s: err = %v, want not found", id, err)
		}
	}
}
