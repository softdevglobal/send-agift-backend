package services

import (
	"bytes"
	_ "embed"
	"fmt"
	"html/template"
	"net/url"
	"strings"
	"time"

	"myapp/internal/repository"
)

// EmailContent is one rendered email: its subject, HTML and plain text.
type EmailContent struct {
	Subject string
	HTML    string
	Text    string
}

// The logo travels inside each email as an inline image.
//
//go:embed emailassets/logo.png
var emailLogoPNG []byte

const emailLogoCID = "sendagift-logo"

// Platform colours, shared with the web and mobile apps.
const (
	colorNavy     = "#0F1B45" // headings, footer
	colorViolet   = "#6D28D9" // labels, buttons
	colorTeal     = "#14B8B8"
	colorText     = "#12172E"
	colorMuted    = "#676E86"
	colorBorder   = "#E3E5F1"
	colorLavender = "#F1EDFB" // callouts
	colorPage     = "#F7F7FC"
)

// emailView is what the shared layout draws: the logo header, the email's
// own sections and the footer.
type emailView struct {
	Subject   string
	Preheader string
	WebURL    string
	LogoSrc   template.URL
	Year      int
	Data      any
}

type emailButton struct {
	Label string
	URL   string
}

// emailStep is one numbered column: 01 / 02 / 03.
type emailStep struct {
	Title, Body string
	Done        bool
}

var emailFuncs = template.FuncMap{
	"isURL": func(s *string) bool {
		return s != nil && (strings.HasPrefix(*s, "https://") || strings.HasPrefix(*s, "http://"))
	},
	"deref": func(s *string) string { return derefOr(s, "") },
	"money": formatMoney,
	"lines": func(s string) []string { return strings.Split(strings.TrimSpace(s), "\n") },
	"num":   func(i int) string { return fmt.Sprintf("%02d", i+1) },
	"last":  func(i, n int) bool { return i == n-1 },
	"c": func(name string) template.CSS {
		return template.CSS(map[string]string{
			"navy": colorNavy, "violet": colorViolet, "teal": colorTeal, "text": colorText,
			"muted": colorMuted, "border": colorBorder, "lavender": colorLavender, "page": colorPage,
		}[name])
	},
}

// The layout follows a quiet editorial style: logo centred on white, one
// strong heading, numbered steps, a lavender callout, a navy footer.
const emailLayout = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<meta http-equiv="X-UA-Compatible" content="IE=edge">
<meta name="color-scheme" content="light">
<meta name="supported-color-schemes" content="light">
<title>{{.Subject}}</title>
<style>
  @media (max-width:640px){
    .container{width:100%!important;}
    .px{padding-left:24px!important;padding-right:24px!important;}
    .h1{font-size:32px!important;line-height:40px!important;letter-spacing:-1px!important;}
    .col{display:block!important;width:100%!important;padding:0 0 22px!important;border:0!important;}
    .code{width:40px!important;height:52px!important;font-size:26px!important;line-height:52px!important;}
  }
</style>
</head>
<body style="margin:0;padding:0;background-color:{{c "page"}};font-family:{{template "sans"}};color:{{c "text"}};">
<div style="display:none;max-height:0;overflow:hidden;opacity:0;mso-hide:all;">{{.Preheader}}&#8203;&#8203;&#8203;&#8203;&#8203;&#8203;&#8203;&#8203;</div>
<table width="100%" cellpadding="0" cellspacing="0" border="0" style="width:100%;background-color:{{c "page"}};margin:0;padding:0;">
<tr><td align="center" style="padding:36px 16px 50px;">
<table class="container" width="620" cellpadding="0" cellspacing="0" border="0" style="width:620px;max-width:620px;background-color:#ffffff;border:1px solid {{c "border"}};border-radius:8px;overflow:hidden;">

  <tr><td align="center" style="padding:30px 40px 26px;border-bottom:1px solid {{c "border"}};">
    <a href="{{.WebURL}}" style="text-decoration:none;">
      <img src="{{.LogoSrc}}" alt="SendAGift" width="120" style="display:block;width:120px;max-width:120px;height:auto;border:0;margin:0 auto;">
    </a>
  </td></tr>

  {{template "content" .Data}}

  <tr><td align="center" style="padding:28px 40px;background-color:{{c "navy"}};">
    <div style="font-size:12px;line-height:18px;color:#C9CDE0;font-weight:700;letter-spacing:0.4px;">SendAGift</div>
    <div style="margin-top:6px;font-size:11px;line-height:17px;color:#8E94AE;">Gifts that arrive with intention.</div>
    <div style="margin-top:6px;font-size:11px;line-height:17px;color:#8E94AE;">&copy; {{.Year}} SendAGift. All rights reserved.</div>
  </td></tr>

</table>
</td></tr>
</table>
</body>
</html>

{{define "sans"}}Arial,Helvetica,sans-serif{{end}}

{{define "eyebrow"}}<tr><td class="px" style="padding:52px 60px 0;">
  <div style="font-size:11px;line-height:16px;letter-spacing:1.8px;text-transform:uppercase;font-weight:700;color:{{c "violet"}};">{{.}}</div>
</td></tr>{{end}}

{{define "heading"}}<tr><td class="px" style="padding:14px 60px 0;">
  <div class="h1" style="font-size:40px;line-height:48px;letter-spacing:-1.6px;font-weight:700;color:{{c "navy"}};">{{range $i, $l := lines .}}{{if $i}}<br>{{end}}{{$l}}{{end}}</div>
</td></tr>{{end}}

{{define "intro"}}<tr><td class="px" style="padding:24px 60px 0;">
  <div style="font-size:16px;line-height:27px;color:{{c "muted"}};max-width:480px;">{{range $i, $l := lines .}}{{if $i}}<br><br>{{end}}{{$l}}{{end}}</div>
</td></tr>{{end}}

{{define "button"}}<tr><td class="px" style="padding:32px 60px 50px;">
  <table cellpadding="0" cellspacing="0" border="0"><tr>
    <td style="background-color:{{c "violet"}};border-radius:6px;">
      <a href="{{.URL}}" style="display:inline-block;padding:15px 24px;font-size:14px;line-height:20px;font-weight:700;color:#ffffff;text-decoration:none;">{{.Label}}&nbsp;&nbsp;&rarr;</a>
    </td>
  </tr></table>
</td></tr>{{end}}

{{define "spacer"}}<tr><td style="padding:0 0 46px;font-size:0;line-height:0;">&nbsp;</td></tr>{{end}}

{{define "divider"}}<tr><td class="px" style="padding:0 60px;">
  <div style="height:1px;background-color:{{c "border"}};font-size:0;line-height:0;">&nbsp;</div>
</td></tr>{{end}}

{{define "steps"}}<tr><td class="px" style="padding:36px 60px 40px;">
  <table width="100%" cellpadding="0" cellspacing="0" border="0"><tr>
  {{$n := len .}}{{range $i, $s := .}}
    <td class="col" width="33.33%" valign="top" style="width:33.33%;{{if eq $i 0}}padding-right:18px;{{else if last $i $n}}padding-left:18px;{{else}}padding:0 18px;border-left:1px solid {{c "border"}};border-right:1px solid {{c "border"}};{{end}}">
      <div style="font-size:11px;line-height:16px;font-weight:700;color:{{if $s.Done}}{{c "teal"}}{{else}}{{c "violet"}}{{end}};margin-bottom:13px;">{{if $s.Done}}&#10003; DONE{{else}}{{num $i}}{{end}}</div>
      <div style="font-size:15px;line-height:21px;font-weight:700;color:{{c "navy"}};margin-bottom:7px;">{{$s.Title}}</div>
      <div style="font-size:12px;line-height:19px;color:{{c "muted"}};">{{$s.Body}}</div>
    </td>
  {{end}}
  </tr></table>
</td></tr>{{end}}

{{define "callout"}}<tr><td class="px" style="padding:0 60px 44px;">
  <table width="100%" cellpadding="0" cellspacing="0" border="0" style="background-color:{{c "lavender"}};border-left:3px solid {{c "violet"}};">
    <tr><td style="padding:22px 24px;">
      <div style="font-size:12px;line-height:18px;font-weight:700;color:{{c "violet"}};margin-bottom:7px;text-transform:uppercase;letter-spacing:1px;">{{.Label}}</div>
      <div style="font-size:14px;line-height:22px;color:{{c "text"}};">{{range $i, $l := lines .Body}}{{if $i}}<br>{{end}}{{$l}}{{end}}</div>
    </td></tr>
  </table>
</td></tr>{{end}}

{{define "otp"}}<tr><td class="px" align="center" style="padding:32px 60px 8px;">
  <table cellpadding="0" cellspacing="0" border="0"><tr>
  {{range .}}<td width="8"></td>
    <td class="code" width="48" height="60" align="center" valign="middle" style="width:48px;height:60px;line-height:60px;font-size:28px;font-weight:700;color:{{c "navy"}};background-color:{{c "lavender"}};border-radius:8px;">{{.}}</td>
  {{end}}<td width="8"></td>
  </tr></table>
</td></tr>{{end}}

{{define "closing"}}<tr><td class="px" align="center" style="padding:0 60px 56px;">
  <div style="font-size:22px;line-height:30px;font-weight:700;letter-spacing:-0.4px;color:{{c "navy"}};">{{.Title}}</div>
  <div style="margin-top:10px;font-size:13px;line-height:21px;color:{{c "muted"}};">{{.Body}}</div>
</td></tr>{{end}}

{{define "items"}}<tr><td class="px" style="padding:36px 60px 8px;">
  <div style="font-size:11px;line-height:16px;letter-spacing:1.8px;text-transform:uppercase;font-weight:700;color:{{c "violet"}};margin-bottom:6px;">{{if .ShowPrices}}Your order{{else}}What arrived{{end}}</div>
  <table width="100%" cellpadding="0" cellspacing="0" border="0">
  {{range .Items}}<tr>
    <td width="68" valign="middle" style="padding:12px 0;border-bottom:1px solid {{c "border"}};">
      {{if isURL .ImageURL}}<img src="{{deref .ImageURL}}" width="56" height="56" alt="" style="display:block;width:56px;height:56px;border-radius:6px;object-fit:cover;background-color:{{c "lavender"}};">
      {{else}}<div style="width:56px;height:56px;line-height:56px;border-radius:6px;background-color:{{c "lavender"}};text-align:center;font-size:24px;">&#127873;</div>{{end}}
    </td>
    <td valign="middle" style="padding:12px 10px;border-bottom:1px solid {{c "border"}};">
      <div style="font-size:15px;line-height:21px;font-weight:700;color:{{c "navy"}};">{{.ProductName}}</div>
      <div style="font-size:12px;line-height:19px;color:{{c "muted"}};">from {{.ShopName}} &middot; Qty {{.Quantity}}</div>
    </td>
    {{if $.ShowPrices}}<td align="right" valign="middle" style="padding:12px 0;border-bottom:1px solid {{c "border"}};font-size:14px;font-weight:700;color:{{c "navy"}};white-space:nowrap;">{{money .TotalAmount $.Currency}}</td>{{end}}
  </tr>{{end}}
  </table>
</td></tr>{{end}}
`

// ── Customer welcome ────────────────────────────────────────────────────

const customerWelcomeContent = `{{define "content"}}
{{template "eyebrow" "Welcome to SendAGift"}}
{{template "heading" "A little something\ngoes a long way."}}
{{template "intro" .Intro}}
{{template "button" .CTA}}
{{template "divider"}}
{{template "steps" .Steps}}
{{template "callout" .Callout}}
{{template "closing" .Closing}}
{{end}}`

type emailCallout struct{ Label, Body string }
type emailClosing struct{ Title, Body string }

const sellerEmailCodeContent = `{{define "content"}}
{{template "eyebrow" "Confirm your email"}}
{{template "heading" "Your seller code"}}
{{template "intro" .Intro}}
{{template "otp" .Digits}}
{{template "callout" .Callout}}
{{template "spacer"}}
{{end}}`

func renderSellerEmailCode(name, code string, validFor time.Duration) (*EmailContent, error) {
	minutes := int(validFor.Round(time.Minute).Minutes())
	if minutes < 1 {
		minutes = 1
	}
	digits := make([]string, 0, len(code))
	for _, r := range code {
		digits = append(digits, string(r))
	}
	data := map[string]any{
		"Intro":  fmt.Sprintf("Hi %s,\nEnter this code to confirm your seller email and open your shop. It expires in %d minutes.", name, minutes),
		"Digits": digits,
		"Callout": emailCallout{
			Label: "Keep this code private",
			Body:  "We will never ask you to read it out or forward this email.",
		},
	}
	text := fmt.Sprintf(`Hi %s,

Your SendAGift seller code is %s.

Enter it to confirm your email. It expires in %d minutes.

The SendAGift team`, name, code, minutes)
	return renderEmail("", sellerEmailCodeContent, "Your SendAGift seller code",
		"Enter this code to confirm your seller email.", data, text)
}

const passwordResetCodeContent = `{{define "content"}}
{{template "eyebrow" .Eyebrow}}
{{template "heading" .Heading}}
{{template "intro" .Intro}}
{{template "otp" .Digits}}
{{template "callout" .Callout}}
{{template "spacer"}}
{{end}}`

func renderPasswordResetCode(name, code, purpose string, validFor time.Duration) (*EmailContent, error) {
	minutes := int(validFor.Round(time.Minute).Minutes())
	if minutes < 1 {
		minutes = 1
	}
	digits := make([]string, 0, len(code))
	for _, r := range code {
		digits = append(digits, string(r))
	}
	eyebrow := "Password reset"
	heading := "Reset your password"
	action := "reset your password"
	if purpose == "profile" {
		eyebrow = "Password change"
		heading = "Confirm your new password"
		action = "confirm the password change you started"
	}
	data := map[string]any{
		"Eyebrow": eyebrow,
		"Heading": heading,
		"Intro":   fmt.Sprintf("Hi %s,\nEnter this code to %s. It expires in %d minutes. If you didn't ask for this, you can ignore the email.", name, action, minutes),
		"Digits":  digits,
		"Callout": emailCallout{Label: "Keep this code private", Body: "We will never ask you to read it out or forward this email."},
	}
	text := fmt.Sprintf("Hi %s,\n\nYour SendAGift code is %s.\n\nUse it to %s. It expires in %d minutes.\n\nThe SendAGift team", name, code, action, minutes)
	return renderEmail("", passwordResetCodeContent, "Your SendAGift password code",
		"Enter this code to continue.", data, text)
}

func renderCustomerWelcome(webURL, name string) (*EmailContent, error) {
	data := map[string]any{
		"Intro": fmt.Sprintf("Hi %s,\nYour account is ready. Send thoughtful gifts to the people who matter, right from your phone or desktop. We'll help take care of the rest.", name),
		"CTA":   emailButton{Label: "Start sending gifts", URL: webURL + "/products"},
		"Steps": []emailStep{
			{Title: "Find a gift", Body: "Discover gifts from shops near them."},
			{Title: "Make it personal", Body: "Add a message or video to your gift."},
			{Title: "Earn points", Body: "Get points whenever you send a gift."},
		},
		"Callout": emailCallout{Label: "One more thing", Body: "Every gift you send earns points. Use them to play our skill games and see what you can win."},
		"Closing": emailClosing{Title: "Ready to make someone's day?", Body: "Your next thoughtful gift is only a few clicks away."},
	}
	text := fmt.Sprintf(`Hi %s,

Welcome to SendAGift. Your account is ready.

Send thoughtful gifts to the people who matter, right from your phone or desktop.

  01 Find a gift. Discover gifts from shops near them.
  02 Make it personal. Add a message or video to your gift.
  03 Earn points. Get points whenever you send a gift.

Start sending gifts: %s/products

The SendAGift team`, name, webURL)
	return renderEmail(webURL, customerWelcomeContent, "Welcome to SendAGift, "+name,
		"Your account is ready. A little something goes a long way.", data, text)
}

// ── Orders ──────────────────────────────────────────────────────────────

// orderEmailData is what the order emails draw from.
type orderEmailData struct {
	Name          string
	SenderName    string
	OrderNumber   string
	RecipientName string
	RecipientCity string
	DeliveryDate  string
	GiftMessage   string
	Items         []repository.OrderEmailItem
	ShowPrices    bool
	Currency      string
	Subtotal      int
	Delivery      int
	Total         int
	GiftPoints    int64
	// Account details for a recipient whose account was made for them.
	LoginEmail   string
	TempPassword string

	Eyebrow, Heading, Intro string
	CTA                     emailButton
	Callout                 emailCallout
	Closing                 emailClosing
}

const orderPlacedContent = `{{define "content"}}
{{template "eyebrow" .Eyebrow}}
{{template "heading" .Heading}}
{{template "intro" .Intro}}
<tr><td class="px" style="padding:32px 60px 0;">
  <table width="100%" cellpadding="0" cellspacing="0" border="0" style="border:1px solid {{c "border"}};border-radius:6px;"><tr>
    <td class="col" width="50%" valign="top" style="width:50%;padding:20px 22px;">
      <div style="font-size:11px;line-height:16px;letter-spacing:1.4px;text-transform:uppercase;font-weight:700;color:{{c "violet"}};">Going to</div>
      <div style="margin-top:6px;font-size:17px;line-height:24px;font-weight:700;color:{{c "navy"}};">{{if .RecipientName}}{{.RecipientName}}{{else}}Your recipient{{end}}</div>
      {{if .RecipientCity}}<div style="font-size:13px;line-height:20px;color:{{c "muted"}};">{{.RecipientCity}}</div>{{end}}
    </td>
    <td class="col" width="50%" valign="top" style="width:50%;padding:20px 22px;border-left:1px solid {{c "border"}};">
      <div style="font-size:11px;line-height:16px;letter-spacing:1.4px;text-transform:uppercase;font-weight:700;color:{{c "violet"}};">Arriving</div>
      <div style="margin-top:6px;font-size:17px;line-height:24px;font-weight:700;color:{{c "navy"}};">{{.DeliveryDate}}</div>
      <div style="font-size:13px;line-height:20px;color:{{c "muted"}};">Order {{.OrderNumber}}</div>
    </td>
  </tr></table>
</td></tr>
{{template "items" .}}
<tr><td class="px" style="padding:8px 60px 0;">
  <table width="100%" cellpadding="0" cellspacing="0" border="0" style="font-size:14px;line-height:20px;color:{{c "muted"}};">
    <tr><td style="padding:5px 0;">Subtotal</td><td align="right" style="padding:5px 0;">{{money .Subtotal .Currency}}</td></tr>
    <tr><td style="padding:5px 0;">Delivery</td><td align="right" style="padding:5px 0;">{{if eq .Delivery 0}}<span style="color:{{c "teal"}};font-weight:700;">Free</span>{{else}}{{money .Delivery .Currency}}{{end}}</td></tr>
    {{if gt .GiftPoints 0}}<tr><td style="padding:5px 0;">Gift points attached</td><td align="right" style="padding:5px 0;color:{{c "violet"}};font-weight:700;">{{.GiftPoints}} pts</td></tr>{{end}}
    <tr><td style="padding:12px 0 0;border-top:1px solid {{c "navy"}};font-size:16px;font-weight:700;color:{{c "navy"}};">Total</td><td align="right" style="padding:12px 0 0;border-top:1px solid {{c "navy"}};font-size:16px;font-weight:700;color:{{c "navy"}};">{{money .Total .Currency}}</td></tr>
  </table>
</td></tr>
{{template "button" .CTA}}
{{if .GiftMessage}}{{template "callout" .Callout}}{{end}}
{{template "closing" .Closing}}
{{end}}`

func renderOrderPlaced(webURL string, o *repository.OrderEmailSummary) (*EmailContent, error) {
	data := orderData(o)
	data.Name = firstName(derefOr(o.CustomerName, ""), o.CustomerEmail)
	data.ShowPrices = true
	recipient := data.RecipientName
	if recipient == "" {
		recipient = "your recipient"
	}
	data.Eyebrow = "Order confirmed · " + o.OrderNumber
	data.Heading = "Your gift for " + recipient + "\nis being prepared."
	data.Intro = fmt.Sprintf("Hi %s,\nThank you for your order. The shop is getting everything ready. And we won't tell %s a thing until the gift is in their hands.", data.Name, recipient)
	data.CTA = emailButton{Label: "Track your order", URL: webURL + "/orders/" + o.OrderID.String()}
	data.Callout = emailCallout{Label: "Your message", Body: "“" + data.GiftMessage + "”"}
	data.Closing = emailClosing{Title: "Surprise intact.", Body: "We'll keep you posted as your gift makes its way."}

	var b strings.Builder
	fmt.Fprintf(&b, "Hi %s,\n\nThank you for your order! Here's your gift at a glance.\n\n", data.Name)
	fmt.Fprintf(&b, "Order: %s\nGoing to: %s\nArriving: %s\n", o.OrderNumber, recipient, data.DeliveryDate)
	if data.GiftMessage != "" {
		fmt.Fprintf(&b, "Your message: \"%s\"\n", data.GiftMessage)
	}
	b.WriteString("\n")
	for _, it := range data.Items {
		fmt.Fprintf(&b, "  • %s ×%d (%s), %s\n", it.ProductName, it.Quantity, it.ShopName, formatMoney(it.TotalAmount, data.Currency))
	}
	fmt.Fprintf(&b, "\nSubtotal: %s\nDelivery: %s\nTotal: %s\n", formatMoney(data.Subtotal, data.Currency), formatMoney(data.Delivery, data.Currency), formatMoney(data.Total, data.Currency))
	fmt.Fprintf(&b, "\nWe won't tell %s a thing until the gift is in their hands.\n\nTrack your order: %s/orders/%s\n\nThe SendAGift team", recipient, webURL, o.OrderID)
	return renderEmail(webURL, orderPlacedContent, "Order confirmed: your gift for "+recipient,
		"Order "+o.OrderNumber+" · arriving "+data.DeliveryDate+". We'll keep it a surprise.", data, b.String())
}

const giftDeliveredContent = `{{define "content"}}
{{template "eyebrow" .Eyebrow}}
{{template "heading" .Heading}}
{{template "intro" .Intro}}
{{if .GiftMessage}}<tr><td class="px" style="padding:32px 60px 0;">
  <table width="100%" cellpadding="0" cellspacing="0" border="0" style="background-color:{{c "lavender"}};border-left:3px solid {{c "violet"}};">
    <tr><td style="padding:24px 26px;">
      <div style="font-size:12px;line-height:18px;font-weight:700;color:{{c "violet"}};margin-bottom:9px;text-transform:uppercase;letter-spacing:1px;">A message from {{.SenderName}}</div>
      <div style="font-family:Georgia,'Times New Roman',serif;font-size:19px;line-height:29px;font-style:italic;color:{{c "navy"}};">&ldquo;{{range $i, $l := lines .GiftMessage}}{{if $i}}<br>{{end}}{{$l}}{{end}}&rdquo;</div>
    </td></tr>
  </table>
</td></tr>{{end}}
{{template "items" .}}
{{if gt .GiftPoints 0}}<tr><td class="px" style="padding:20px 60px 0;">
  <div style="font-size:14px;line-height:22px;color:{{c "text"}};"><strong style="color:{{c "violet"}};">+{{.GiftPoints}} points</strong> from {{.SenderName}} are waiting in your SendAGift wallet.</div>
</td></tr>{{end}}
{{if .TempPassword}}<tr><td class="px" style="padding:32px 60px 0;">
  <table width="100%" cellpadding="0" cellspacing="0" border="0" style="border:1px solid {{c "border"}};border-radius:6px;">
    <tr><td style="padding:22px 24px;">
      <div style="font-size:11px;line-height:16px;letter-spacing:1.4px;text-transform:uppercase;font-weight:700;color:{{c "violet"}};">Your SendAGift account</div>
      <div style="margin-top:8px;font-size:14px;line-height:22px;color:{{c "muted"}};">We've set up an account for you so you can review your gift and send one back. Sign in with:</div>
      <table cellpadding="0" cellspacing="0" border="0" style="margin-top:14px;">
        <tr><td style="padding:4px 18px 4px 0;font-size:13px;color:{{c "muted"}};">Email</td><td style="padding:4px 0;font-size:15px;font-weight:700;color:{{c "navy"}};">{{.LoginEmail}}</td></tr>
        <tr><td style="padding:4px 18px 4px 0;font-size:13px;color:{{c "muted"}};">Password</td><td style="padding:4px 0;"><span style="display:inline-block;padding:4px 10px;border-radius:4px;background-color:{{c "lavender"}};font-family:'Courier New',Courier,monospace;font-size:16px;font-weight:700;letter-spacing:2px;color:{{c "navy"}};">{{.TempPassword}}</span></td></tr>
      </table>
      <div style="margin-top:12px;font-size:12px;line-height:18px;color:{{c "muted"}};">This is a temporary password. Please change it as soon as you sign in.</div>
    </td></tr>
  </table>
</td></tr>{{end}}
{{template "button" .CTA}}
{{template "closing" .Closing}}
{{end}}`

func renderGiftDelivered(webURL string, o *repository.OrderEmailSummary, tempPassword string) (*EmailContent, error) {
	data := orderData(o)
	data.Name = firstName(data.RecipientName, derefOr(o.RecipientEmail, ""))
	data.SenderName = firstName(derefOr(o.CustomerName, ""), o.CustomerEmail)
	data.LoginEmail = derefOr(o.RecipientEmail, "")
	data.TempPassword = tempPassword
	signIn := webURL + "/login?" + url.Values{"email": {data.LoginEmail}, "next": {"/account/gifts"}}.Encode()
	data.Eyebrow = "Special delivery"
	data.Heading = data.SenderName + " sent you\nsomething special."
	data.Intro = fmt.Sprintf("Hi %s,\n%s was thinking of you, and your gift has just been delivered.", data.Name, data.SenderName)
	data.CTA = emailButton{Label: "See your gift & leave a review", URL: signIn}
	data.Closing = emailClosing{Title: "Loving it?", Body: "Tell " + data.SenderName + " and everyone else with a quick review. It only takes a minute."}

	var b strings.Builder
	fmt.Fprintf(&b, "Hi %s,\n\n%s was thinking of you. Your gift has just been delivered!\n\n", data.Name, data.SenderName)
	if data.GiftMessage != "" {
		fmt.Fprintf(&b, "\"%s\"\nFrom %s\n\n", data.GiftMessage, data.SenderName)
	}
	b.WriteString("What arrived:\n")
	for _, it := range data.Items {
		fmt.Fprintf(&b, "  • %s ×%d (from %s)\n", it.ProductName, it.Quantity, it.ShopName)
	}
	if data.GiftPoints > 0 {
		fmt.Fprintf(&b, "\n%s also sent you %d points. They're in your SendAGift wallet.\n", data.SenderName, data.GiftPoints)
	}
	if tempPassword != "" {
		fmt.Fprintf(&b, "\nWe've set up a SendAGift account for you:\n  Email: %s\n  Password: %s\nThis is a temporary password. Please change it as soon as you sign in.\n", data.LoginEmail, tempPassword)
	}
	fmt.Fprintf(&b, "\nSee your gift and leave a review: %s\n\nThe SendAGift team", signIn)
	return renderEmail(webURL, giftDeliveredContent, data.SenderName+" sent you a gift",
		"Your gift from "+data.SenderName+" has been delivered. Open to see what's inside.", data, b.String())
}

func orderData(o *repository.OrderEmailSummary) orderEmailData {
	return orderEmailData{
		OrderNumber:   o.OrderNumber,
		RecipientName: derefOr(o.RecipientName, ""),
		RecipientCity: derefOr(o.RecipientCity, ""),
		DeliveryDate:  o.DeliveryDate.Format("Mon, 2 Jan 2006"),
		GiftMessage:   derefOr(o.GiftMessage, ""),
		Items:         o.Items,
		Currency:      o.Currency,
		Subtotal:      o.SubtotalAmount,
		Delivery:      o.DeliveryAmount,
		Total:         o.TotalAmount,
		GiftPoints:    o.GiftPoints,
	}
}

// ── Rendering ───────────────────────────────────────────────────────────

var emailBase = template.Must(template.New("layout").Funcs(emailFuncs).Parse(emailLayout))

// emailLogoSrc is where the layout's logo comes from: the inline image
// attached when the email is sent. Previews swap in a data: URI.
var emailLogoSrc = template.URL("cid:" + emailLogoCID)

// renderEmail draws an email's sections inside the shared layout.
func renderEmail(webURL, content, subject, preheader string, data any, text string) (*EmailContent, error) {
	t, err := emailBase.Clone()
	if err != nil {
		return nil, err
	}
	if _, err := t.Parse(content); err != nil {
		return nil, fmt.Errorf("parse email template: %w", err)
	}
	view := emailView{
		Subject: subject, Preheader: preheader, WebURL: webURL,
		LogoSrc: emailLogoSrc, Year: time.Now().Year(), Data: data,
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", view); err != nil {
		return nil, fmt.Errorf("render email: %w", err)
	}
	return &EmailContent{Subject: subject, HTML: buf.String(), Text: text}, nil
}

func textNote(note string) string {
	if strings.TrimSpace(note) == "" {
		return ""
	}
	return "\nA note from our team: " + strings.TrimSpace(note) + "\n"
}

// zeroDecimalCurrencies have no minor unit: an amount is whole units.
var zeroDecimalCurrencies = map[string]bool{
	"JPY": true, "KRW": true, "VND": true, "CLP": true, "ISK": true, "UGX": true, "XAF": true, "XOF": true,
}

// formatMoney shows a minor-unit amount, e.g. 12345 USD as "USD 123.45".
func formatMoney(minor int, currency string) string {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if zeroDecimalCurrencies[currency] {
		return fmt.Sprintf("%s %s", currency, groupThousands(minor))
	}
	sign := ""
	if minor < 0 {
		sign, minor = "-", -minor
	}
	return fmt.Sprintf("%s%s %s.%02d", sign, currency, groupThousands(minor/100), minor%100)
}

func groupThousands(n int) string {
	s := fmt.Sprint(n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}
