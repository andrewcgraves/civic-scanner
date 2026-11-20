-- +goose Up
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS role text NOT NULL DEFAULT 'user'
        CONSTRAINT users_role_check CHECK (role IN ('user', 'admin'));

-- +goose Down
-- squawk-ignore ban-drop-column
ALTER TABLE users DROP COLUMN IF EXISTS role;
