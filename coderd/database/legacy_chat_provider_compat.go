package database

import (
	"database/sql"
	"time"

	"github.com/google/uuid"
)

// ChatProvider is retained temporarily for tests that are still being migrated
// from legacy chat providers to AI providers.
//
//nolint:revive
type ChatProvider struct {
	ID                         uuid.UUID
	Provider                   string
	DisplayName                string
	APIKey                     string
	BaseUrl                    string
	ApiKeyKeyID                sql.NullString
	CreatedAt                  time.Time
	UpdatedAt                  time.Time
	CreatedBy                  uuid.NullUUID
	Enabled                    bool
	CentralApiKeyEnabled       bool
	AllowUserApiKey            bool
	AllowCentralApiKeyFallback bool
}

// InsertChatProviderParams is retained temporarily for test helpers that munge
// legacy chat provider fields before mapping them to AI providers.
//
//nolint:revive
type InsertChatProviderParams struct {
	Provider                   string
	DisplayName                string
	APIKey                     string
	BaseUrl                    string
	ApiKeyKeyID                sql.NullString
	CreatedBy                  uuid.NullUUID
	Enabled                    bool
	CentralApiKeyEnabled       bool
	AllowUserApiKey            bool
	AllowCentralApiKeyFallback bool
}
