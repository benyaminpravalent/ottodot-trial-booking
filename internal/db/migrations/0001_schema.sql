-- 0001_schema.sql
-- Core tables for trial booking. Seats are NOT stored: they are derived from
-- COUNT(*) of bookings WHERE status = 'confirmed' (single source of truth).

CREATE TYPE booking_status AS ENUM ('pending_payment', 'confirmed', 'payment_failed', 'cancelled');

CREATE TABLE parents (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text        NOT NULL,
    email       text        NOT NULL UNIQUE,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE students (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    parent_id   uuid        NOT NULL REFERENCES parents(id) ON DELETE RESTRICT,
    name        text        NOT NULL,
    grade       smallint    NOT NULL CHECK (grade BETWEEN 1 AND 12),
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX students_parent_idx ON students (parent_id);

CREATE TABLE trial_classes (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    subject      text        NOT NULL CHECK (subject IN ('Science', 'Math')),
    title        text        NOT NULL,
    teacher_name text        NOT NULL,
    starts_at    timestamptz NOT NULL,
    capacity     smallint    NOT NULL DEFAULT 4 CHECK (capacity > 0),
    price_cents  integer     NOT NULL CHECK (price_cents >= 0),
    currency     char(3)     NOT NULL DEFAULT 'SGD',
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE bookings (
    id             uuid           PRIMARY KEY DEFAULT gen_random_uuid(),
    trial_class_id uuid           NOT NULL REFERENCES trial_classes(id) ON DELETE RESTRICT,
    student_id     uuid           NOT NULL REFERENCES students(id) ON DELETE RESTRICT,
    -- Denormalised for roster convenience. Must equal students.parent_id; enforced in the service layer.
    parent_id      uuid           NOT NULL REFERENCES parents(id) ON DELETE RESTRICT,
    status         booking_status NOT NULL DEFAULT 'pending_payment',
    -- Machine-readable reason for terminal states: payment_declined | class_full | duplicate_confirmed
    status_reason  text,
    created_at     timestamptz    NOT NULL DEFAULT now(),
    updated_at     timestamptz    NOT NULL DEFAULT now(),
    confirmed_at   timestamptz,
    -- confirmed_at is set if and only if the booking is confirmed.
    CONSTRAINT bookings_confirmed_at_matches_status
        CHECK ((status = 'confirmed') = (confirmed_at IS NOT NULL))
);

CREATE TABLE payment_attempts (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    booking_id   uuid        NOT NULL REFERENCES bookings(id) ON DELETE RESTRICT,
    amount_cents integer     NOT NULL CHECK (amount_cents >= 0),
    currency     char(3)     NOT NULL,
    provider     text        NOT NULL DEFAULT 'mock',
    provider_ref text        NOT NULL,
    status       text        NOT NULL CHECK (status IN ('succeeded', 'failed', 'refunded')),
    failure_code text,
    -- Set when status = 'refunded': the provider's refund reference (provider_ref keeps the original charge ref).
    refund_ref   text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT payment_attempts_refund_ref_matches_status
        CHECK ((status = 'refunded') = (refund_ref IS NOT NULL))
);
CREATE INDEX payment_attempts_booking_idx ON payment_attempts (booking_id);
