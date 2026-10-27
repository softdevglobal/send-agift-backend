package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Config holds process settings loaded from .env / environment.
type Config struct {
	AppPort         string
	DBHost          string
	DBPort          string
	DBUser          string
	DBPassword      string
	DBName          string
	DBSSLMode       string
	JWTSecret       string
	JWTExpiry       time.Duration
	BootstrapSecret string
	AWSRegion       string
	// Optional static keys for local development. Leave both empty on AWS:
	// the task role supplies credentials there.
	AWSAccessKeyID string
	AWSSecretKey   string
	S3Bucket       string
	GoogleMapsKey  string

	// Seller points purchases. PointsCentsPerPoint is what one point costs in
	// minor units of PointsCurrency (10 = $0.10 a point). The provider is
	// "instant" (the default until Stripe is connected: points are credited
	// the moment they are bought), "manual" (an admin confirms each payment)
	// or "test" (sellers approve their own purchase. Development only). The webhook secret
	// signs provider confirmations; without it the webhook is refused.
	PointsCentsPerPoint   int
	PointsCurrency        string
	PointsPaymentProvider string
	PointsWebhookSecret   string

	// Stripe. Customers pay the platform account; delivered order lines are
	// then transferred to each seller's Connect account. The two webhook
	// secrets belong to two endpoints (events from your account, and events
	// from connected accounts). Each endpoint has its own signing secret.
	// StripeFeeBPS is the platform's cut of each line in basis points
	// (100 = 1%). Without a secret key Stripe is off and orders are unchanged.
	StripeSecretKey string
	// StripePublishableKey is handed to the mobile app for Stripe's in-app
	// payment sheet. It is public by design.
	StripePublishableKey       string
	StripeWebhookSecret        string
	StripeConnectWebhookSecret string
	StripeFeeBPS               int
	// StripeConnectCountry overrides the country a seller's Stripe account is
	// created in. Stripe only supports some countries (not Sri Lanka, for
	// one), so for testing a supported one such as AU can be used instead of
	// the seller's own. Leave empty to use the seller's country.
	StripeConnectCountry string
	// PointsRewardTiming is when a product's reward points reach the buyer:
	// "order" (as soon as the order is placed. The default while there is
	// no payment step) or "delivery".
	PointsRewardTiming string

	// Firebase service-account credentials for push notifications: a path
	// to the JSON key file, or the JSON itself (for secret managers). With
	// neither set, notifications queue up and are sent once one is added.
	FirebaseCredentialsFile string
	FirebaseCredentialsJSON string

	// Transactional email through ZeptoMail. The token is the "Send Mail"
	// token from the ZeptoMail agent, with or without its "Zoho-enczapikey"
	// prefix. Without a token, emails queue up and are sent once one is set.
	ZeptoMailToken       string
	ZeptoMailFromAddress string
	ZeptoMailFromName    string
	// ZeptoMailAPIURL is the send endpoint for the account's data centre
	// (api.zeptomail.com, .eu, .in, ...).
	ZeptoMailAPIURL string
	// AppWebURL is the customer website, for links and images in emails.
	AppWebURL string

	// Customer sign-in with Google and Facebook. Without an ID the provider's
	// button is refused with 503. GoogleClientIDs lists every OAuth client
	// whose tokens are accepted: the web client first, then any Android or
	// iOS clients (GOOGLE_EXTRA_CLIENT_IDS, comma separated).
	GoogleClientIDs []string
	// AppleClientIDs are the identifiers Apple issues Sign in with Apple
	// tokens for: the iPhone app's bundle id (APPLE_CLIENT_IDS, comma
	// separated). Empty turns Apple sign-in off.
	AppleClientIDs     []string
	GoogleClientSecret string
	FacebookAppID      string
	FacebookAppSecret  string

	// SMS through a textbee.dev Android gateway. Without a key, messages
	// queue up and are sent once one is set.
	TextBeeAPIKey          string
	TextBeeDeviceID        string
	TextBeeAPIBase         string
	TextBeeSIMSubscription string
	// DefaultPhoneCountryCode reads a local number (0771234567) as E.164.
	DefaultPhoneCountryCode string

	// First free gift program. Off by default: every free gift route answers
	// FREE_GIFT_DISABLED until FREE_GIFT_ENABLED=true. The identity secret
	// keys the HMAC that the one-time limits are stored under, so it must
	// never change once gifts have been sent. The terms version is stamped on
	// a seller's program when they accept.
	FreeGiftEnabled        bool
	FreeGiftIdentitySecret string
	FreeGiftTermsVersion   string

	// Competition points (handover v4). Nothing is offered without an
	// approval record, so there is no on/off flag here: these only tune it.
	// CPQuoteSecret signs entry quotes (falls back to a key derived from
	// JWT_SECRET). CPEntryHoldMinutes is how long a reserved entry waits to
	// start. CPRestoreWindowHours keeps points released from a lot that
	// expired while held usable for that long; it is a release decision,
	// with a sandbox default of 24. MembershipTestPayments lets a sandbox
	// without Stripe confirm membership payments by hand.
	CPQuoteSecret          string
	CPEntryHoldMinutes     int
	CPRestoreWindowHours   int
	MembershipTestPayments bool
}

// Load reads .env (if present) and required environment variables.
func Load() (*Config, error) {
	_ = godotenv.Load()

	cfg := &Config{
		AppPort:         envOr("APP_PORT", "8080"),
		DBHost:          envOr("DB_HOST", "localhost"),
		DBPort:          envOr("DB_PORT", "5432"),
		DBUser:          os.Getenv("DB_USER"),
		DBPassword:      os.Getenv("DB_PASSWORD"),
		DBName:          os.Getenv("DB_NAME"),
		DBSSLMode:       envOr("DB_SSLMODE", "disable"),
		JWTSecret:       os.Getenv("JWT_SECRET"),
		JWTExpiry:       minutesOr("JWT_EXPIRY_MINUTES", 24*60),
		BootstrapSecret: os.Getenv("BOOTSTRAP_SECRET"),
		AWSRegion:       envOr("AWS_REGION", "ap-southeast-2"),
		AWSAccessKeyID:  os.Getenv("AWS_ACCESS_KEY_ID"),
		AWSSecretKey:    os.Getenv("AWS_SECRET_ACCESS_KEY"),
		S3Bucket:        os.Getenv("S3_BUCKET"),
		GoogleMapsKey:   os.Getenv("GOOGLE_MAPS_API_KEY"),

		PointsCentsPerPoint:   intOr("POINTS_CENTS_PER_POINT", 10),
		PointsCurrency:        envOr("POINTS_CURRENCY", "USD"),
		PointsPaymentProvider: envOr("POINTS_PAYMENT_PROVIDER", "instant"),
		PointsWebhookSecret:   os.Getenv("POINTS_WEBHOOK_SECRET"),
		PointsRewardTiming:    envOr("POINTS_REWARD_TIMING", "order"),

		StripeSecretKey:            os.Getenv("STRIPE_SECRET_KEY"),
		StripePublishableKey:       strings.TrimSpace(os.Getenv("STRIPE_PUBLISHABLE_KEY")),
		StripeWebhookSecret:        os.Getenv("STRIPE_WEBHOOK_SECRET"),
		StripeConnectWebhookSecret: os.Getenv("STRIPE_CONNECT_WEBHOOK_SECRET"),
		StripeFeeBPS:               intOr("STRIPE_PLATFORM_FEE_BPS", 0),
		StripeConnectCountry:       strings.ToUpper(strings.TrimSpace(os.Getenv("STRIPE_CONNECT_COUNTRY"))),

		FirebaseCredentialsFile: os.Getenv("FIREBASE_CREDENTIALS_FILE"),
		FirebaseCredentialsJSON: os.Getenv("FIREBASE_CREDENTIALS_JSON"),

		ZeptoMailToken:       os.Getenv("ZEPTOMAIL_SYSTEM_TOKEN"),
		ZeptoMailFromAddress: os.Getenv("ZEPTOMAIL_SYSTEM_FROM_ADDRESS"),
		ZeptoMailFromName:    envOr("ZEPTOMAIL_SYSTEM_FROM_NAME", "SendAGift"),
		ZeptoMailAPIURL:      envOr("ZEPTOMAIL_API_URL", "https://api.zeptomail.com/v1.1/email"),
		AppWebURL:            strings.TrimRight(envOr("APP_WEB_URL", "http://localhost:5173"), "/"),

		GoogleClientIDs:    listEnv("GOOGLE_CLIENT_ID", "GOOGLE_EXTRA_CLIENT_IDS"),
		AppleClientIDs:     listEnv("APPLE_CLIENT_IDS"),
		GoogleClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
		FacebookAppID:      os.Getenv("FACEBOOK_APP_ID"),
		FacebookAppSecret:  os.Getenv("FACEBOOK_APP_SECRET"),

		TextBeeAPIKey:           os.Getenv("TEXTBEE_API_KEY"),
		TextBeeDeviceID:         os.Getenv("TEXTBEE_DEVICE_ID"),
		TextBeeAPIBase:          strings.TrimRight(envOr("TEXTBEE_API_BASE", "https://api.textbee.dev/api/v1"), "/"),
		TextBeeSIMSubscription:  os.Getenv("TEXTBEE_SIM_SUBSCRIPTION_ID"),
		DefaultPhoneCountryCode: envOr("TEXTBEE_DEFAULT_COUNTRY_CODE", "+61"),

		FreeGiftEnabled:        strings.EqualFold(strings.TrimSpace(os.Getenv("FREE_GIFT_ENABLED")), "true"),
		FreeGiftIdentitySecret: os.Getenv("FREE_GIFT_IDENTITY_SECRET"),
		FreeGiftTermsVersion:   envOr("FREE_GIFT_TERMS_VERSION", "v1"),

		CPQuoteSecret:          os.Getenv("CP_QUOTE_SECRET"),
		CPEntryHoldMinutes:     intOr("CP_ENTRY_HOLD_MINUTES", 10),
		CPRestoreWindowHours:   wholeOr("CP_RESTORE_WINDOW_HOURS", 24),
		MembershipTestPayments: strings.EqualFold(strings.TrimSpace(os.Getenv("MEMBERSHIP_TEST_PAYMENTS")), "true"),
	}

	// if the JWT secret is not set, return an error
	if cfg.JWTSecret == "" {
		return nil, fmt.Errorf("JWT_SECRET is required")
	}
	// if the database user or name is not set, return an error
	if cfg.DBUser == "" || cfg.DBName == "" {
		return nil, fmt.Errorf("DB_USER and DB_NAME are required")
	}
	if cfg.S3Bucket == "" {
		return nil, fmt.Errorf("S3_BUCKET is required")
	}
	// Keys come as a pair or not at all; one alone is a typo, not a choice.
	if (cfg.AWSAccessKeyID == "") != (cfg.AWSSecretKey == "") {
		return nil, fmt.Errorf("set both AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY, or neither to use the AWS default credentials (the task role on ECS)")
	}
	if cfg.ZeptoMailToken != "" && cfg.ZeptoMailFromAddress == "" {
		return nil, fmt.Errorf("ZEPTOMAIL_SYSTEM_FROM_ADDRESS is required when ZEPTOMAIL_SYSTEM_TOKEN is set")
	}
	if (cfg.FacebookAppID == "") != (cfg.FacebookAppSecret == "") {
		return nil, fmt.Errorf("set both FACEBOOK_APP_ID and FACEBOOK_APP_SECRET, or neither")
	}
	if (cfg.TextBeeAPIKey == "") != (cfg.TextBeeDeviceID == "") {
		return nil, fmt.Errorf("set both TEXTBEE_API_KEY and TEXTBEE_DEVICE_ID, or neither")
	}
	if cfg.FreeGiftEnabled && len(cfg.FreeGiftIdentitySecret) < 32 {
		return nil, fmt.Errorf("FREE_GIFT_IDENTITY_SECRET (32 characters or more) is required when FREE_GIFT_ENABLED=true")
	}
	if cfg.CPEntryHoldMinutes < 1 || cfg.CPEntryHoldMinutes > 120 {
		return nil, fmt.Errorf("CP_ENTRY_HOLD_MINUTES must be 1 to 120")
	}
	if cfg.CPRestoreWindowHours < 0 || cfg.CPRestoreWindowHours > 24*30 {
		return nil, fmt.Errorf("CP_RESTORE_WINDOW_HOURS must be 0 to 720")
	}
	if cfg.GoogleMapsKey == "" {
		return nil, fmt.Errorf("GOOGLE_MAPS_API_KEY is required")
	}

	return cfg, nil // return the configuration
}

// DSN builds a Postgres connection string.
// This string is used to connect to the database
func (c *Config) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		c.DBHost, c.DBPort, c.DBUser, c.DBPassword, c.DBName, c.DBSSLMode,
	)
}

// This function is used to get the value of an environment variable or return a fallback value
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// intOr reads a positive whole number from the environment, or the fallback.
func intOr(key string, fallback int) int {
	n, err := strconv.Atoi(os.Getenv(key))
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

// This function is used to get the value of an environment variable or return a fallback value
func minutesOr(key string, fallback int) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return time.Duration(fallback) * time.Minute
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return time.Duration(fallback) * time.Minute
	}
	return time.Duration(n) * time.Minute
}

// listEnv reads comma-separated values from the given variables, in order,
// skipping blanks.
func listEnv(keys ...string) []string {
	var out []string
	for _, key := range keys {
		for _, v := range strings.Split(os.Getenv(key), ",") {
			if v = strings.TrimSpace(v); v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}

// wholeOr reads a whole number, zero included, or the fallback when unset
// or unreadable.
func wholeOr(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}
