-- +goose Up
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS stripe_customer_id text
        CONSTRAINT users_stripe_customer_id_check CHECK (stripe_customer_id <> '')
        -- squawk-ignore disallowed-unique-constraint
        CONSTRAINT users_stripe_customer_id_unique UNIQUE;

-- +goose Down
-- squawk-ignore ban-drop-column
ALTER TABLE users DROP COLUMN IF EXISTS stripe_customer_id;
