package main

import (
	"context"       // context is used to manage the lifecycle of a request
	"crypto/sha256" // sha256 derives the entry-quote signing key when none is set
	"encoding/hex"  // hex encodes that derived key
	"fmt"           // fmt is used to format and print text
	"log"           // log is used to log messages to the console
	"net/http"      // net/http is used to create and manage HTTP servers and clients
	"strings"       // strings is used to read the points payment provider name
	"time"          // time is used to measure and manage time-related operations

	"github.com/google/uuid"

	"myapp/internal/config"     // config is used to load and manage the configuration of the application
	"myapp/internal/database"   // database is used to connect and manage the database
	"myapp/internal/handlers"   // handlers is used to handle the HTTP requests and responses
	"myapp/internal/middleware" // middleware guards routes, including the customer session cut-off
	"myapp/internal/repository" // repository is used to manage the data access layer of the application
	"myapp/internal/routes"     // routes is used to manage the routing of the application
	"myapp/internal/services"   // services is used to manage the business logic of the project
)

// func = is the function main function
func main() {
	cfg, err := config.Load() // load application configuration from the environment variables
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	// pool is the database connection pool
	pool, err := database.Connect(cfg)
	if err != nil {
		log.Fatalf("database error: %v", err)
	}
	defer pool.Close()

	// migCtx is the context for the migration operation
	migCtx, migCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer migCancel()
	if err := database.MigrateUp(migCtx, pool); err != nil {
		log.Fatalf("migration error: %v", err)
	}

	// admins, countries, customers, sellers are the repositories for the admin, country, customer, and seller entities
	admins := repository.NewAdminRepository(pool)      // create a new admin repository
	countries := repository.NewCountryRepository(pool) // create a new country repository
	countryCapabilities := repository.NewCountryCapabilityRepository(pool)
	customers := repository.NewCustomerRepository(pool) // create a new customer repository
	sellers := repository.NewSellerRepository(pool)     // create a new seller repository
	products := repository.NewProductRepository(pool)
	orders := repository.NewOrderRepository(pool)
	shipments := repository.NewShipmentRepository(pool)
	reels := repository.NewReelRepository(pool)
	reelSocial := repository.NewReelSocialRepository(pool)
	productReviews := repository.NewProductReviewRepository(pool)
	messaging := repository.NewMessagingRepository(pool)
	gameRepo := repository.NewGameRepository(pool)
	competitionRepo := repository.NewCompetitionRepository(pool)
	pointsRepo := repository.NewPointsRepository(pool)

	// s3Service issues presigned URLs so clients upload straight to the bucket
	s3Service, err := services.NewS3Service(cfg)
	if err != nil {
		log.Fatalf("s3 error: %v", err)
	}

	authService := services.NewAuthService(admins, customers, sellers, cfg.JWTSecret, cfg.BootstrapSecret, cfg.JWTExpiry)
	authService.UsePhoneCountry(cfg.DefaultPhoneCountryCode)
	adminService := services.NewAdminService(admins) // create a new admin service
	countryService := services.NewCountryService(countries)
	countryCapabilityService := services.NewCountryCapabilityService(countryCapabilities, countries)
	customerService := services.NewCustomerService(customers, countries, countryCapabilityService, products, cfg.JWTSecret, cfg.JWTExpiry) // create a new customer service
	customerService.UseAdminReads(orders)
	// Product reward points reach the buyer when the order is placed, or on
	// delivery (POINTS_REWARD_TIMING=delivery).
	orders.PayRewardsAtOrder(cfg.PointsRewardTiming != "delivery")
	orderService := services.NewOrderService(orders, customers, countries, shipments)
	sellerService := services.NewSellerService(sellers, countries, countryCapabilityService, cfg.JWTSecret, cfg.JWTExpiry)
	sellerService.UseAdminReads(products, orders)
	productService := services.NewProductService(products, sellers, countries, s3Service, cfg.S3Bucket)
	reelService := services.NewReelService(reels, reelSocial, sellers, s3Service, cfg.S3Bucket)
	reelSocialService := services.NewReelSocialService(reelSocial)
	productReviewService := services.NewProductReviewService(productReviews, orders, s3Service, cfg.S3Bucket)
	messagingService := services.NewMessagingService(messaging, orders, customers, sellers, admins, s3Service, cfg.S3Bucket)
	gameService := services.NewGameService(gameRepo)
	competitionService := services.NewCompetitionService(competitionRepo, gameRepo, customers, countryCapabilities, countries, pointsRepo)
	gameService.UseCompetitions(competitionService)
	// Every practice play costs the points a Super Admin set for that game
	// (competition.games.play_cost_points), taken on the server when it starts.
	gameService.ChargeForPlays(pointsRepo)
	pointsService := services.NewPointsService(pointsRepo)
	// Prize reconciliation (Progressive Prize spec §9): every round whose
	// money can still move is re-derived from its ledger on a schedule.
	// With several API servers running, an advisory lock gives each round
	// to only one of them.
	go competitionService.RunReconciliation(context.Background(), 15*time.Minute,
		database.Exclusive(pool, database.LockPrizeReconciliation))
	// Points earning: delivered orders, refunds and sign-up bonuses, by each
	// country's rule, plus product rewards and gift points riding on orders.
	// Idempotent, so it simply runs on a timer.
	go pointsService.RunEarningLoop(context.Background(), time.Minute,
		database.Exclusive(pool, database.LockPointsEarning))
	// Sellers buying points: the provider only starts a payment; points are
	// credited when the webhook or an admin confirms it.
	// One Stripe client for orders and for sellers buying points. Nil when
	// STRIPE_SECRET_KEY is not set.
	stripeClient := services.NewStripeClient(cfg.StripeSecretKey)
	var pointsProvider services.PointsPaymentProvider
	if strings.EqualFold(strings.TrimSpace(cfg.PointsPaymentProvider), "stripe") {
		// Sellers pay for points on Stripe's Checkout page.
		pointsProvider, err = services.NewStripePointsProvider(stripeClient, cfg.AppWebURL)
	} else {
		pointsProvider, err = services.NewPointsPaymentProvider(cfg.PointsPaymentProvider)
	}
	if err != nil {
		log.Fatalf("points payments: %v", err)
	}
	if pointsProvider.SelfConfirm() {
		log.Printf("⚠️  POINTS_PAYMENT_PROVIDER=%s: seller points are credited without a confirmed payment. Connect a card provider before going live.", pointsProvider.Name())
	}
	sellerPointsService := services.NewSellerPointsService(pointsRepo, pointsProvider, cfg.PointsCentsPerPoint, cfg.PointsCurrency, cfg.PointsWebhookSecret)
	sellerPointsService.HoldAfterEmailChange(sellers) // buying points waits a few days after an email change
	// The point packages sellers pick from; the starting ones are created
	// once, at the configured rate, when there are none.
	if err := sellerPointsService.EnsureDefaultPackages(context.Background()); err != nil {
		log.Printf("points: could not create the starting packages: %v", err)
	}
	// A Stripe points purchase is credited by its webhook; this re-checks
	// any that are still pending, in case a webhook was missed.
	go sellerPointsService.RunReconcileLoop(context.Background(), time.Minute,
		database.Exclusive(pool, database.LockPointsPurchaseSync))
	shippingService := services.NewShippingService(shipments, orders)

	// Transactional email through ZeptoMail: rendered into an outbox when
	// something happens, and sent by this loop. Without a token the emails
	// wait in the outbox until one is configured.
	var emailSender services.EmailSender
	if zepto := services.NewZeptoMailSender(cfg.ZeptoMailAPIURL, cfg.ZeptoMailToken, cfg.ZeptoMailFromAddress, cfg.ZeptoMailFromName); zepto != nil {
		emailSender = zepto
		fmt.Printf("✉️  Email: ZeptoMail %s, from %s\n", cfg.ZeptoMailAPIURL, cfg.ZeptoMailFromAddress)
	} else {
		log.Printf("⚠️  ZEPTOMAIL_SYSTEM_TOKEN is not set: emails are queued but not sent.")
	}
	emailService := services.NewEmailService(repository.NewEmailRepository(pool), emailSender, cfg.AppWebURL)
	go emailService.RunDeliveryLoop(context.Background(), 15*time.Second,
		database.Exclusive(pool, database.LockEmailDelivery))
	// New customers get a welcome email.
	customerService.SendEmailsWith(emailService)
	sellerService.SendEmailsWith(emailService)
	passwordResets := repository.NewPasswordResetRepository(pool)
	customerService.UsePasswordResets(passwordResets)
	sellerService.UsePasswordResets(passwordResets)
	// Orders: the buyer gets a confirmation and the recipient a customer
	// account; the recipient is emailed only once the gift is delivered.
	giftRecipientService := services.NewGiftRecipientService(orders, customers, emailService)
	orderService.NotifyWith(giftRecipientService)
	go giftRecipientService.RunDeliveredNotices(context.Background(), time.Minute,
		database.Exclusive(pool, database.LockGiftDeliveryNotices))
	// SMS through textbee: queued like email and sent by this loop. Without
	// a key the messages wait in the outbox until one is configured.
	var smsSender services.SMSSender
	if textbee := services.NewTextBeeSender(cfg.TextBeeAPIBase, cfg.TextBeeDeviceID, cfg.TextBeeAPIKey, cfg.TextBeeSIMSubscription); textbee != nil {
		smsSender = textbee
		fmt.Printf("📱 SMS: textbee device %s\n", cfg.TextBeeDeviceID)
	} else {
		log.Printf("⚠️  TEXTBEE_API_KEY is not set: SMS are queued but not sent.")
	}
	smsService := services.NewSMSService(repository.NewSMSRepository(pool), smsSender)
	go smsService.RunDeliveryLoop(context.Background(), 5*time.Second,
		database.Exclusive(pool, database.LockSMSDelivery))
	// Recipients reached by phone get the delivered notice by text too.
	giftRecipientService.SendSMSWith(smsService, cfg.AppWebURL, cfg.DefaultPhoneCountryCode)
	// Customers sign in or sign up with a 6-digit code by SMS or email.
	loginCodeService := services.NewLoginCodeService(repository.NewLoginCodeRepository(pool), customers,
		emailService, smsService, cfg.DefaultPhoneCountryCode, cfg.JWTSecret, cfg.JWTExpiry)
	customerService.UseCodeSignups(loginCodeService)
	// Changing the login email: a code to the new address, an undo link to the
	// old one, and a cut-off that signs out every other device.
	emailChanges := repository.NewEmailChangeRepository(pool)
	customerService.UseEmailChanges(emailChanges)
	middleware.UseSessionChecker(repository.NewSessionRepository(pool))
	emailChangeHandler := handlers.NewEmailChangeHandler(customerService)
	sellerEmailChanges := repository.NewSellerEmailChangeRepository(pool)
	sellerService.UseEmailChanges(sellerEmailChanges)
	sellerEmailChangeHandler := handlers.NewSellerEmailChangeHandler(sellerService)
	loginCodeHandler := handlers.NewLoginCodeHandler(loginCodeService)
	// Gift recipients review from the link in their email or text.
	giftRecipientService.UseReviewLinks(cfg.JWTSecret)

	// Stripe: customers pay the platform account, then delivered order lines
	// are transferred to sellers' Connect accounts. Without a secret key the
	// endpoints answer "not available" and nothing else changes.
	stripeService := services.NewStripeService(
		repository.NewStripeRepository(pool),
		stripeClient,
		cfg.StripeWebhookSecret, cfg.StripeConnectWebhookSecret,
		cfg.StripeFeeBPS, cfg.AppWebURL)
	stripeService.UseConnectCountry(cfg.StripeConnectCountry)
	stripeService.UsePublishableKey(cfg.StripePublishableKey)
	if stripeService.Enabled() {
		fmt.Println("💳 Stripe: card payments and seller payouts are on")
		if cfg.StripeConnectCountry != "" {
			fmt.Printf("💳 Stripe: seller payout accounts are created in %s (STRIPE_CONNECT_COUNTRY)\n", cfg.StripeConnectCountry)
		}
		// An order only counts once it is paid: sellers do not see it, and
		// its confirmation email, recipient account and rewards wait.
		orders.RequirePayment(true)
		orderService.RequirePayment(true)
		// Checkout Sessions that pay for a seller's points.
		stripeService.OnPointsCheckout(sellerPointsService.HandleStripeCheckout)
		stripeService.OnOrderPaid(func(ctx context.Context, orderID uuid.UUID) {
			if err := orders.PayRewardsForPaidOrder(ctx, orderID); err != nil {
				log.Printf("order %s paid: rewards: %v", orderID, err)
			}
			giftRecipientService.OrderPlaced(ctx, orderID)
		})
		stripeService.CancelUnpaidWith(func(ctx context.Context, customerID, orderID string) error {
			return orders.CancelForCustomer(ctx, customerID, orderID)
		})
		orderService.AfterCancel(func(ctx context.Context, orderID uuid.UUID) {
			if err := stripeService.RefundOrder(ctx, orderID); err != nil {
				log.Printf("order %s cancelled but the refund failed: %v", orderID, err)
			}
		})
		go stripeService.RunPayoutLoop(context.Background(), time.Minute,
			database.Exclusive(pool, database.LockStripePayouts))
		go stripeService.RunExpiryLoop(context.Background(), 15*time.Minute,
			database.Exclusive(pool, database.LockStripeExpiry))
	} else {
		log.Printf("⚠️  STRIPE_SECRET_KEY is not set: card payments and seller payouts are off.")
	}
	// Competition points (handover v4): memberships, gift points, the member
	// bonus and reserve-then-capture entries. Nothing is offered until an
	// approval record covers it; see docs/competition-points.md.
	var membershipStripe *services.StripeClient
	if stripeService.Enabled() {
		membershipStripe = stripeClient
	}
	quoteSecret := cfg.CPQuoteSecret
	if quoteSecret == "" {
		sum := sha256.Sum256([]byte("cp-quote:" + cfg.JWTSecret))
		quoteSecret = hex.EncodeToString(sum[:])
	}
	competitionPointsRepo := repository.NewCompetitionPointsRepository(pool)
	competitionPointsService := services.NewCompetitionPointsService(
		competitionPointsRepo, competitionService, membershipStripe,
		services.CompetitionPointsConfig{
			WebURL:         cfg.AppWebURL,
			QuoteSecret:    quoteSecret,
			HoldTTL:        time.Duration(cfg.CPEntryHoldMinutes) * time.Minute,
			RestoreWindow:  time.Duration(cfg.CPRestoreWindowHours) * time.Hour,
			TestPayments:   cfg.MembershipTestPayments,
			PlatformFeeBPS: cfg.StripeFeeBPS,
		})
	// Members get a share of SendAGift's commission back as a discount on
	// the gifts they buy; the seller's price and payout do not change.
	orderService.UseMemberDiscounts(competitionPointsRepo.MemberDiscount, cfg.StripeFeeBPS)
	stripeService.OnMembershipEvent(competitionPointsService.HandleStripeEvent)
	go competitionPointsService.RunLoop(context.Background(), time.Minute,
		database.Exclusive(pool, database.LockCompetitionPoints))
	if cfg.MembershipTestPayments && !stripeService.Enabled() {
		log.Printf("⚠️  MEMBERSHIP_TEST_PAYMENTS=true: membership payments can be confirmed by hand (sandbox only).")
	}
	competitionPointsHandler := handlers.NewCompetitionPointsHandler(competitionPointsService)

	stripeHandler := handlers.NewStripeHandler(stripeService)
	giftReviewHandler := handlers.NewGiftReviewHandler(services.NewGiftReviewService(orders, customers, loginCodeService, cfg.JWTSecret, cfg.JWTExpiry))
	// Customer sign-in with Google, Facebook and Apple. A provider without keys
	// answers 503, so its button can be shown or hidden by configuration.
	socialAuthService := services.NewSocialAuthService(customers, countries, countryCapabilityService, emailService,
		cfg.GoogleClientIDs, cfg.FacebookAppID, cfg.FacebookAppSecret, cfg.JWTSecret, cfg.JWTExpiry).
		EnableApple(cfg.AppleClientIDs)
	socialAuthHandler := handlers.NewSocialAuthHandler(socialAuthService)

	authHandler := handlers.NewAuthHandler(authService)          // create a new auth handler
	adminHandler := handlers.NewAdminHandler(adminService)       // create a new admin handler
	countryHandler := handlers.NewCountryHandler(countryService) // create a new country handler
	countryCapabilityHandler := handlers.NewCountryCapabilityHandler(countryCapabilityService)
	customerHandler := handlers.NewCustomerHandler(customerService) // create a new customer handler
	orderHandler := handlers.NewOrderHandler(orderService)
	marketplaceService := services.NewMarketplaceService(sellers, products)
	shopsHandler := handlers.NewShopsHandler(marketplaceService)
	sellerHandler := handlers.NewSellerHandler(sellerService) // create a new seller handler
	sellerOrderHandler := handlers.NewSellerOrderHandler(orderService)
	productHandler := handlers.NewProductHandler(productService)
	reelHandler := handlers.NewReelHandler(reelService)
	reelSocialHandler := handlers.NewReelSocialHandler(reelSocialService, cfg.JWTSecret)
	productReviewHandler := handlers.NewProductReviewHandler(productReviewService, cfg.JWTSecret)
	messagingHandler := handlers.NewMessagingHandler(messagingService)
	gameHandler := handlers.NewGameHandler(gameService)
	competitionHandler := handlers.NewCompetitionHandler(competitionService)
	pointsHandler := handlers.NewPointsHandler(pointsService)
	sellerPointsHandler := handlers.NewSellerPointsHandler(sellerPointsService)
	mediaHandler := handlers.NewMediaHandler(s3Service) // create a new media handler

	// placesService proxies Google Places so the API key stays on the server
	placesService := services.NewPlacesService(cfg)
	placesHandler := handlers.NewPlacesHandler(placesService) // create a new places handler
	shippingHandler := handlers.NewShippingHandler(shippingService)
	availabilityService := services.NewAvailabilityService(repository.NewAvailabilityRepository(pool))
	availabilityHandler := handlers.NewAvailabilityHandler(availabilityService)

	// Push notifications (Firebase Cloud Messaging): queued with the event
	// they announce. A competition being published. And sent by this loop.
	// Without credentials they wait in the queue until some are configured.
	fcmSender, err := services.NewFCMSender(context.Background(), cfg.FirebaseCredentialsFile, cfg.FirebaseCredentialsJSON)
	if err != nil {
		log.Fatalf("push notifications: %v", err)
	}
	var pushSender services.PushSender
	if fcmSender != nil {
		pushSender = fcmSender
	} else {
		log.Printf("⚠️  FIREBASE_CREDENTIALS_FILE is not set: push notifications are queued but not sent.")
	}
	pushService := services.NewPushService(repository.NewPushRepository(pool), pushSender)
	go pushService.RunDeliveryLoop(context.Background(), 10*time.Second,
		database.Exclusive(pool, database.LockPushDelivery))
	pushHandler := handlers.NewPushHandler(pushService)

	// First free gift program: off unless FREE_GIFT_ENABLED=true, when every
	// customer has one free gift funded by a seller's marketing budget.
	freeGiftService := services.NewFreeGiftService(repository.NewFreeGiftRepository(pool), customers, countries, orderService,
		cfg.FreeGiftEnabled, cfg.FreeGiftIdentitySecret, cfg.FreeGiftTermsVersion, cfg.DefaultPhoneCountryCode)
	// The free gift list can be narrowed to a delivery address and day.
	freeGiftService.UseAvailability(availabilityService)
	freeGiftHandler := handlers.NewFreeGiftHandler(freeGiftService)
	if cfg.FreeGiftEnabled {
		fmt.Println("🎁 Free gift program: on")
	}

	router := routes.New(authHandler, adminHandler, countryHandler, countryCapabilityHandler, customerHandler, orderHandler, shopsHandler, sellerHandler, sellerOrderHandler, productHandler, reelHandler, reelSocialHandler, productReviewHandler, messagingHandler, gameHandler, competitionHandler, pointsHandler, sellerPointsHandler, mediaHandler, placesHandler, shippingHandler, availabilityHandler, pushHandler, socialAuthHandler, loginCodeHandler, giftReviewHandler, emailChangeHandler, sellerEmailChangeHandler, stripeHandler, freeGiftHandler, competitionPointsHandler, cfg.JWTSecret)

	addr := ":" + cfg.AppPort                                      // create a new address for the server
	fmt.Printf("✅ Database connected: %s\n", cfg.DBName)           // print the database name
	fmt.Printf("🚀 Server listening on http://localhost%s\n", addr) // print the server address

	if err := http.ListenAndServe(addr, router); err != nil { // start the server
		log.Fatalf("server error: %v", err)
	} // if the server fails to start, log the error
}
