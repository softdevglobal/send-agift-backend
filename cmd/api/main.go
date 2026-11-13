package main

import (
	"context"  // context is used to manage the lifecycle of a request
	"fmt"      // fmt is used to format and print text
	"log"      // log is used to log messages to the console
	"net/http" // net/http is used to create and manage HTTP servers and clients
	"time"     // time is used to measure and manage time-related operations

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
	gameEntriesRepo := repository.NewGameEntriesRepository(pool)
	adRepo := repository.NewAdRepository(pool)

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
	orderService := services.NewOrderService(orders, customers, countries, shipments)
	sellerService := services.NewSellerService(sellers, countries, countryCapabilityService, cfg.JWTSecret, cfg.JWTExpiry)
	sellerService.UseAdminReads(products, orders)
	productService := services.NewProductService(products, sellers, countries, s3Service, cfg.S3Bucket)
	reelService := services.NewReelService(reels, reelSocial, sellers, s3Service, cfg.S3Bucket)
	reelSocialService := services.NewReelSocialService(reelSocial)
	productReviewService := services.NewProductReviewService(productReviews, orders, s3Service, cfg.S3Bucket)
	messagingService := services.NewMessagingService(messaging, orders, customers, sellers, admins, s3Service, cfg.S3Bucket)
	gameService := services.NewGameService(gameRepo)
	competitionService := services.NewCompetitionService(competitionRepo, gameRepo, customers, countryCapabilities, countries, gameEntriesRepo)
	gameService.UseCompetitions(competitionService)
	// Every play costs the game entries an admin set for that game
	// (competition.games.entry_cost), taken on the server when it starts.
	gameService.ChargeForPlays(gameEntriesRepo)
	// Game entries and where games are switched on. Sign-up asks for the
	// customer's state wherever their country has states.
	gameEntriesService := services.NewGameEntriesService(gameEntriesRepo, cfg.StateChangeCooldownDays)
	customerService.RequireStates(gameEntriesService.RequireStateForSignup)
	// Prize reconciliation (Progressive Prize spec §9): every round whose
	// money can still move is re-derived from its ledger on a schedule.
	// With several API servers running, an advisory lock gives each round
	// to only one of them.
	go competitionService.RunReconciliation(context.Background(), 15*time.Minute,
		database.Exclusive(pool, database.LockPrizeReconciliation))
	// Game entries housekeeping: entries on cancelled or refunded orders are
	// undone, missed grants are made, and packages past their time end.
	// Idempotent, so it simply runs on a timer.
	go gameEntriesService.RunSettleLoop(context.Background(), time.Minute,
		database.Exclusive(pool, database.LockGameEntries))
	// One Stripe client for orders, memberships and sellers' advertising
	// packages. Nil when STRIPE_SECRET_KEY is not set.
	stripeClient := services.NewStripeClient(cfg.StripeSecretKey)
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
	// Sellers' advertising packages: bought through Stripe, activated only
	// by a confirmed payment. The loop emails budget reminders and re-checks
	// purchases whose webhook never arrived.
	advertisingService := services.NewAdvertisingService(adRepo, sellers, stripeClient, emailService, cfg.AppWebURL, cfg.AdTestPayments)
	if cfg.AdTestPayments && stripeClient == nil {
		log.Printf("⚠️  ADS_TEST_PAYMENTS=true: sellers can confirm advertising payments by hand (sandbox only).")
	}
	go advertisingService.RunLoop(context.Background(), time.Minute,
		database.Exclusive(pool, database.LockAdvertising))
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
		// its confirmation email, recipient account and game entries wait.
		orders.RequirePayment(true)
		orderService.RequirePayment(true)
		// Checkout Sessions that pay for a seller's advertising package.
		stripeService.OnAdCheckout(advertisingService.HandleStripeCheckout)
		stripeService.OnOrderPaid(func(ctx context.Context, orderID uuid.UUID) {
			// The payment is confirmed: the entries promised at checkout
			// are granted now, and counted against the sellers' packages.
			if err := orders.GrantEntriesForPaidOrder(ctx, orderID); err != nil {
				log.Printf("order %s paid: game entries: %v", orderID, err)
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
	// Memberships. Nothing is sold until an approval record covers it.
	var membershipStripe *services.StripeClient
	if stripeService.Enabled() {
		membershipStripe = stripeClient
	}
	competitionPointsRepo := repository.NewCompetitionPointsRepository(pool)
	competitionPointsService := services.NewCompetitionPointsService(
		competitionPointsRepo, competitionService, membershipStripe,
		services.CompetitionPointsConfig{
			WebURL:         cfg.AppWebURL,
			TestPayments:   cfg.MembershipTestPayments,
			PlatformFeeBPS: cfg.StripeFeeBPS,
		})
	// Members get a share of SendAGift's commission back as a discount on
	// the gifts they buy; the seller's price and payout do not change.
	orderService.UseMemberDiscounts(competitionPointsRepo.MemberDiscount, cfg.StripeFeeBPS)
	stripeService.OnMembershipEvent(competitionPointsService.HandleStripeEvent)
	go competitionPointsService.RunLoop(context.Background(), time.Minute,
		database.Exclusive(pool, database.LockMembership))
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
	socialAuthService.RequireStates(gameEntriesService.RequireStateForSignup)
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
	gameEntriesHandler := handlers.NewGameEntriesHandler(gameEntriesService, advertisingService)
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

	router := routes.New(authHandler, adminHandler, countryHandler, countryCapabilityHandler, customerHandler, orderHandler, shopsHandler, sellerHandler, sellerOrderHandler, productHandler, reelHandler, reelSocialHandler, productReviewHandler, messagingHandler, gameHandler, competitionHandler, gameEntriesHandler, mediaHandler, placesHandler, shippingHandler, availabilityHandler, pushHandler, socialAuthHandler, loginCodeHandler, giftReviewHandler, emailChangeHandler, sellerEmailChangeHandler, stripeHandler, freeGiftHandler, competitionPointsHandler, cfg.JWTSecret)

	addr := ":" + cfg.AppPort                                      // create a new address for the server
	fmt.Printf("✅ Database connected: %s\n", cfg.DBName)           // print the database name
	fmt.Printf("🚀 Server listening on http://localhost%s\n", addr) // print the server address

	if err := http.ListenAndServe(addr, router); err != nil { // start the server
		log.Fatalf("server error: %v", err)
	} // if the server fails to start, log the error
}
