CREATE TABLE chat_providers (
    id uuid DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
    provider text NOT NULL UNIQUE,
    display_name text DEFAULT ''::text NOT NULL,
    api_key text DEFAULT ''::text NOT NULL,
    api_key_key_id text REFERENCES dbcrypt_keys(active_key_digest),
    created_by uuid REFERENCES users(id),
    enabled boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    base_url text DEFAULT ''::text NOT NULL,
    central_api_key_enabled boolean DEFAULT true NOT NULL,
    allow_user_api_key boolean DEFAULT false NOT NULL,
    allow_central_api_key_fallback boolean DEFAULT false NOT NULL,
    CONSTRAINT chat_providers_provider_check CHECK (provider = ANY (ARRAY['anthropic', 'azure', 'bedrock', 'google', 'openai', 'openai-compat', 'openrouter', 'vercel'])),
    CONSTRAINT valid_credential_policy CHECK ((central_api_key_enabled OR allow_user_api_key) AND ((NOT allow_central_api_key_fallback) OR (central_api_key_enabled AND allow_user_api_key)))
);

COMMENT ON COLUMN chat_providers.api_key_key_id IS 'The ID of the key used to encrypt the provider API key. If this is NULL, the API key is not encrypted';

CREATE TABLE user_chat_provider_keys (
    id uuid DEFAULT gen_random_uuid() NOT NULL PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    chat_provider_id uuid NOT NULL REFERENCES chat_providers(id) ON DELETE CASCADE,
    api_key text NOT NULL,
    api_key_key_id text REFERENCES dbcrypt_keys(active_key_digest),
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT user_chat_provider_keys_api_key_check CHECK (api_key <> '')
);

INSERT INTO chat_providers (
    id,
    provider,
    display_name,
    api_key,
    api_key_key_id,
    enabled,
    created_at,
    updated_at,
    base_url,
    central_api_key_enabled,
    allow_user_api_key,
    allow_central_api_key_fallback
)
SELECT
    ap.id,
    ap.type::text,
    COALESCE(ap.display_name, ''),
    COALESCE(provider_key.api_key, ''),
    provider_key.api_key_key_id,
    ap.enabled,
    ap.created_at,
    ap.updated_at,
    ap.base_url,
    TRUE,
    TRUE,
    TRUE
FROM (
    SELECT DISTINCT ON (type)
        *
    FROM ai_providers
    WHERE deleted = FALSE
    ORDER BY
        type ASC,
        enabled DESC,
        EXISTS (
            SELECT 1
            FROM ai_provider_keys apk
            WHERE apk.provider_id = ai_providers.id
        ) DESC,
        updated_at DESC,
        id ASC
) ap
LEFT JOIN LATERAL (
    SELECT
        apk.api_key,
        apk.api_key_key_id
    FROM ai_provider_keys apk
    WHERE apk.provider_id = ap.id
    ORDER BY
        apk.created_at ASC,
        apk.id ASC
    LIMIT 1
) provider_key ON TRUE
ON CONFLICT (provider) DO NOTHING;

INSERT INTO user_chat_provider_keys (
    id,
    user_id,
    chat_provider_id,
    api_key,
    api_key_key_id,
    created_at,
    updated_at
)
SELECT DISTINCT ON (uapk.user_id, cp.id)
    uapk.id,
    uapk.user_id,
    cp.id,
    uapk.api_key,
    uapk.api_key_key_id,
    uapk.created_at,
    uapk.updated_at
FROM user_ai_provider_keys uapk
JOIN ai_providers ap ON ap.id = uapk.ai_provider_id
JOIN chat_providers cp ON cp.provider = ap.type::text
ORDER BY
    uapk.user_id ASC,
    cp.id ASC,
    (uapk.ai_provider_id = cp.id) DESC,
    uapk.updated_at DESC,
    uapk.id ASC;

CREATE UNIQUE INDEX user_chat_provider_keys_user_id_chat_provider_id_key
    ON user_chat_provider_keys (user_id, chat_provider_id);

ALTER TABLE chat_model_configs
    DROP CONSTRAINT IF EXISTS chat_model_configs_ai_provider_required_when_active;
