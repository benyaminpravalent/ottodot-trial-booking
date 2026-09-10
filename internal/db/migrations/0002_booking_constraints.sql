-- 0002_booking_constraints.sql
-- Database-level guards for the booking invariants. These are the authoritative
-- checks; the service-layer checks are fast-fail conveniences.

-- I1: at most one CONFIRMED booking per (student, class).
-- Partial: pending_payment / payment_failed / cancelled rows may repeat, so a parent
-- can retry after a declined card without hitting this index.
CREATE UNIQUE INDEX bookings_one_confirmed_per_student_class
    ON bookings (student_id, trial_class_id)
    WHERE status = 'confirmed';

-- Roster + seat-count queries always filter by (class, status).
CREATE INDEX bookings_class_status_idx ON bookings (trial_class_id, status);
