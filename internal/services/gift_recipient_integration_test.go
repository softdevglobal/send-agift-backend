package services

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"myapp/internal/repository"
	"myapp/internal/utils"
)

// Integration test for what happens around a gift: the buyer's confirmation,
// the recipient's account made silently at order time, and the recipient's
// email once — and only once — the gift is delivered.
//
//	TEST_DATABASE_URL=postgres://user:pass@localhost:5432/scratch_db?sslmode=disable \
//	    go test ./internal/services -run GiftRecipient -v

type capturedSender struct {
	mu   sync.Mutex
	sent []repository.OutboxEmail
}

func (c *capturedSender) Send(_ context.Context, e repository.OutboxEmail) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, e)
	return nil
}

func TestGiftRecipientAccountAndEmails(t *testing.T) {
	f := newMultiShopFixture(t)

	// A recipient email unique to this run, so no earlier account is reused.
	recipientEmail := "gift-" + uuid.NewString()[:8] + "@example.test"
	f.exec(`update customer.recipients set email = $2 where id = $1`, f.recipient, recipientEmail)

	emailRepo := repository.NewEmailRepository(f.pool)
	sender := &capturedSender{}
	email := NewEmailService(emailRepo, sender, "https://sendagift.test")
	orderRepo := repository.NewOrderRepository(f.pool)
	customerRepo := repository.NewCustomerRepository(f.pool)
	gifts := NewGiftRecipientService(orderRepo, customerRepo, email)
	f.orders.NotifyWith(gifts)

	t.Cleanup(func() {
		c := context.Background()
		_, _ = f.pool.Exec(c, `delete from core.email_outbox where to_email = $1 or dedupe_key like '%' || $2 || '%'`, recipientEmail, f.customer.String())
		_, _ = f.pool.Exec(c, `delete from customer.customers where email = $1`, recipientEmail)
	})

	s := f.shop(f.seller(), 2500)
	order, err := f.placeOrder([]OrderShippingQuoteInput{f.zoneQuote(s)}, nil, s)
	if err != nil {
		t.Fatalf("place order: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `delete from core.email_outbox where dedupe_key like '%' || $1`, order.id.String())
	})

	// The recipient has an account on the default password, linked to the order.
	recipient, err := customerRepo.GetByEmail(f.ctx, recipientEmail)
	if err != nil {
		t.Fatalf("recipient account not created: %v", err)
	}
	if !recipient.PasswordChangeRequired || !utils.CheckPassword(GiftRecipientDefaultPassword, recipient.PasswordHash) {
		t.Fatal("recipient account should start on the default password and be asked to change it")
	}
	var linked *uuid.UUID
	f.scan(&linked, `select recipient_customer_id from marketplace.orders where id = $1`, order.id)
	if linked == nil || *linked != recipient.ID {
		t.Fatalf("order not linked to the recipient account: %v", linked)
	}

	// The buyer is emailed; the recipient is not, yet.
	kinds := func(to string) []string {
		rows, err := f.pool.Query(f.ctx, `select kind from core.email_outbox where to_email = $1 order by created_at`, to)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var k string
			_ = rows.Scan(&k)
			out = append(out, k)
		}
		return out
	}
	var buyerEmail string
	f.scan(&buyerEmail, `select email from customer.customers where id = $1`, f.customer)
	if got := kinds(buyerEmail); len(got) != 1 || got[0] != "order_placed" {
		t.Fatalf("buyer emails = %v, want [order_placed]", got)
	}
	if got := kinds(recipientEmail); len(got) != 0 {
		t.Fatalf("recipient emailed before delivery: %v", got)
	}
	if n, _ := gifts.NotifyDelivered(f.ctx, 500); n != 0 {
		var due int
		f.scan(&due, `select count(*) from marketplace.orders where id = $1 and status = 'delivered'`, order.id)
		if due != 0 {
			t.Fatal("undelivered order treated as delivered")
		}
	}

	// The recipient can review the line only once it is theirs; the buyer's
	// own lookups stay the buyer's.
	itemID := order.itemsByShop[s.shop][0]
	if _, err := orderRepo.GetItemForReviewer(f.ctx, recipient.ID.String(), itemID.String()); err != nil {
		t.Fatalf("recipient cannot reach their gift line for review: %v", err)
	}
	if _, err := orderRepo.GetItemForCustomer(f.ctx, recipient.ID.String(), itemID.String()); err == nil {
		t.Fatal("recipient must not see the order as its buyer")
	}

	// Deliver it.
	f.exec(`update marketplace.order_items set fulfilment_status = 'delivered' where order_id = $1`, order.id)
	f.exec(`update marketplace.orders set status = 'delivered' where id = $1`, order.id)

	if _, err := gifts.NotifyDelivered(f.ctx, 500); err != nil {
		t.Fatal(err)
	}
	if _, err := gifts.NotifyDelivered(f.ctx, 500); err != nil {
		t.Fatal(err)
	}
	if got := kinds(recipientEmail); len(got) != 1 || got[0] != "gift_delivered" {
		t.Fatalf("recipient emails = %v, want exactly one gift_delivered", got)
	}
	var html string
	f.scan(&html, `select html_body from core.email_outbox where to_email = $1`, recipientEmail)
	if !strings.Contains(html, GiftRecipientDefaultPassword) || strings.Contains(html, "USD") {
		t.Fatal("recipient email should carry the temporary password and no prices")
	}

	gifts2, err := gifts.ReceivedGifts(f.ctx, recipient.ID.String())
	if err != nil || len(gifts2) != 1 || len(gifts2[0].Items) != 1 || gifts2[0].SenderName != "Multi Shop Buyer" {
		t.Fatalf("received gifts = %+v, %v", gifts2, err)
	}

	// The outbox hands both emails to the provider.
	if _, err := email.DeliverDue(f.ctx, 500); err != nil {
		t.Fatal(err)
	}
	var pending int
	f.scan(&pending, `select count(*) from core.email_outbox where status <> 'sent' and (to_email = $1 or to_email = $2)`, recipientEmail, buyerEmail)
	if pending != 0 || len(sender.sent) < 2 {
		t.Fatalf("emails not delivered: %d pending, %d sent", pending, len(sender.sent))
	}

	// Signing up with the recipient's email claims the unclaimed account.
	countries := repository.NewCountryRepository(f.pool)
	customers := NewCustomerService(customerRepo, countries, nil, nil, "secret", 0)
	claimed := recipient
	claimed.PasswordHash, _ = utils.HashPassword("a-real-password")
	if ok, err := customerRepo.ClaimGiftAccount(f.ctx, claimed); err != nil || !ok {
		t.Fatalf("claim gift account: %v %v", ok, err)
	}
	if err := customers.ChangePassword(f.ctx, recipient.ID.String(), ChangePasswordInput{CurrentPassword: "a-real-password", NewPassword: GiftRecipientDefaultPassword}); err == nil {
		t.Fatal("the default password must not be chosen as a new password")
	}
	after, _ := customerRepo.GetByEmail(f.ctx, recipientEmail)
	if after.PasswordChangeRequired {
		t.Fatal("claimed account should no longer ask for a password change")
	}
}
