package services

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"myapp/internal/models"
	"myapp/internal/repository"
)

// ErrDeliveryDate is a requested delivery date the shop cannot meet: in the
// past, sooner than the zone or preparation time allows, a day the product is
// marked unavailable, or more units than are in stock.
var ErrDeliveryDate = errors.New("this delivery date cannot be met")

// dateLine is one cart line to be delivered on the requested day.
type dateLine struct {
	ProductID string
	ShopID    string
	Quantity  int
}

// checkDeliveryDate applies the same rules as the gift search, so a date the
// product filter hid can no longer be quoted or ordered by calling the API
// directly. date is yyyy-mm-dd. It returns every reason the date fails, one
// per shop or product, and nil when the date works.
//
// options are the shops' delivery options built with the shop's local clock;
// rules are keyed by product id.
func checkDeliveryDate(
	date string,
	lines []dateLine,
	options map[string]*SellerDeliveryOption,
	rules map[string]repository.ProductDateRules,
) []string {
	// A product can sit on more than one line; stock is for the total.
	type key struct{ shop, product string }
	wanted := map[key]int{}
	for _, line := range lines {
		wanted[key{line.ShopID, line.ProductID}] += line.Quantity
	}
	keys := make([]key, 0, len(wanted))
	for k := range wanted {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].shop != keys[j].shop {
			return keys[i].shop < keys[j].shop
		}
		return keys[i].product < keys[j].product
	})

	var problems []string
	shopBlocked := map[string]bool{}
	for _, k := range keys {
		opt := options[k.shop]
		if opt == nil || !opt.Available {
			continue // pricing already reports a shop that cannot deliver
		}
		shop := opt.shopName
		if shop == "" {
			shop = "this shop"
		}
		if !shopBlocked[k.shop] {
			if !opt.localNow.IsZero() && date < opt.localNow.Format("2006-01-02") {
				problems = append(problems, fmt.Sprintf("%s: choose today or a later date", shop))
				shopBlocked[k.shop] = true
			} else if opt.EstimatedDeliveryDate > date {
				problems = append(problems, fmt.Sprintf(
					"%s cannot deliver by %s. The earliest it can arrive is %s",
					shop, friendlyDay(date), friendlyDay(opt.EstimatedDeliveryDate)))
				shopBlocked[k.shop] = true
			}
		}
		if shopBlocked[k.shop] {
			continue
		}

		rule, ok := rules[k.product]
		if !ok {
			continue
		}
		name := rule.Name
		if name == "" {
			name = "a gift in your cart"
		}
		if arrival := arrivalWithPrep(opt.localNow, opt, rule.PrepMinutes); arrival != "" && arrival > date {
			problems = append(problems, fmt.Sprintf(
				"%s needs more preparation time. The earliest it can arrive is %s", name, friendlyDay(arrival)))
			continue
		}
		if rule.IsUnavailableOn(date) {
			problems = append(problems, fmt.Sprintf("%s cannot be delivered on %s", name, friendlyDay(date)))
			continue
		}
		if rule.HasInventory {
			left := max(rule.AvailableQty-rule.ReservedQty, 0)
			switch {
			case left == 0:
				problems = append(problems, fmt.Sprintf("%s is out of stock", name))
			case left < wanted[k]:
				problems = append(problems, fmt.Sprintf("Only %d of %s left", left, name))
			}
		}
	}
	return problems
}

// deliveryDateError wraps the reasons in ErrDeliveryDate.
func deliveryDateError(problems []string) error {
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrDeliveryDate, problems[0])
}

// checkOrderDate loads the products' preparation time, stock and blocked days
// and applies checkDeliveryDate to an order's lines.
func (s *OrderService) checkOrderDate(
	ctx context.Context,
	date string,
	items []models.OrderItem,
	options map[string]*SellerDeliveryOption,
) error {
	ids := make([]string, 0, len(items))
	lines := make([]dateLine, 0, len(items))
	seen := map[string]bool{}
	for _, it := range items {
		pid := it.ProductID.String()
		if !seen[pid] {
			seen[pid] = true
			ids = append(ids, pid)
		}
		lines = append(lines, dateLine{ProductID: pid, ShopID: it.ShopID.String(), Quantity: it.Quantity})
	}
	rules, err := s.orders.ProductDateRules(ctx, ids)
	if err != nil {
		return err
	}
	return deliveryDateError(checkDeliveryDate(date, lines, options, rules))
}

// friendlyDay writes a yyyy-mm-dd day the way a shopper reads it,
// "Thu 15 Oct". Anything else is returned unchanged.
func friendlyDay(day string) string {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return day
	}
	return t.Format("Mon 2 Jan")
}
