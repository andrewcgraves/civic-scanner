-- +goose NO TRANSACTION
-- +goose Up
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS stripe_customer_id text
        CONSTRAINT users_stripe_customer_id_check CHECK (stripe_customer_id <> '');

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS users_stripe_customer_id_unique
    ON users (stripe_customer_id);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS users_stripe_customer_id_unique;

-- squawk-ignore ban-drop-column
ALTER TABLE users DROP COLUMN IF EXISTS stripe_customer_id;
