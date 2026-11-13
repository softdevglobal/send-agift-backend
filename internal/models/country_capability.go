package models

import (
	"time"

	"github.com/google/uuid"
)

// CountryCapability maps to core.country_capabilities.
type CountryCapability struct {
	ID                           uuid.UUID `json:"id"`
	CountryID                    uuid.UUID `json:"country_id"`
	CustomerRegistrationEnabled  bool      `json:"customer_registration_enabled"`
	SellerRegistrationEnabled    bool      `json:"seller_registration_enabled"`
	SellerPayoutsEnabled         bool      `json:"seller_payouts_enabled"`
	DomesticDeliveryEnabled      bool      `json:"domestic_delivery_enabled"`
	InternationalDeliveryEnabled bool      `json:"international_delivery_enabled"`
	MembershipsEnabled           bool      `json:"memberships_enabled"`
	EntriesEarningEnabled        bool      `json:"entries_earning_enabled"`
	EntriesUsageEnabled          bool      `json:"entries_usage_enabled"`
	SkillCompetitionsEnabled     bool      `json:"skill_competitions_enabled"`
	// Progressive prizes (a prize that grows from plays paid for with game entries) need
	// their own legal sign-off per country, separate from skill competitions.
	ProgressivePrizesEnabled bool `json:"progressive_prizes_enabled"`
	// Chance games (spin, scratch, draw…) with a prize: their own legal gate.
	ChanceGamesEnabled bool      `json:"chance_games_enabled"`
	AppStoreAvailable  bool      `json:"app_store_available"`
	RuleVersion        int       `json:"rule_version"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// CountryCapabilityDetails is a country with its capability gates.
type CountryCapabilityDetails struct {
	Country    Country           `json:"country"`
	Capability CountryCapability `json:"capability"`
}
