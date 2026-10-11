-- Two ledgers, two units.
--
--   * Upstream cost is what the provider charges us: currency (CNY).
--   * User-facing consumption is what a customer spends from their quota:
--     credits (积分), which are not currency and may be fractional.
--
-- The columns that used to carry the user-facing amount were named "sale" and
-- "sale_limit" after the old money-only model. They are renamed here so the
-- storage layer speaks the same vocabulary as the API and the console.
--
-- PostgreSQL has no "RENAME COLUMN IF EXISTS", so each rename is wrapped in a
-- guard and every statement is individually idempotent: replaying this file
-- against a schema it already touched is a no-op, which is what
-- TestMigrationsAreIndividuallyIdempotent exercises.

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = 'price_versions'
          AND column_name = 'sale'
    ) AND NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = 'price_versions'
          AND column_name = 'credits'
    ) THEN
        ALTER TABLE price_versions RENAME COLUMN sale TO credits;
    END IF;
END $$;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = 'budget_rules'
          AND column_name = 'sale_limit'
    ) AND NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = 'budget_rules'
          AND column_name = 'credit_limit'
    ) THEN
        ALTER TABLE budget_rules RENAME COLUMN sale_limit TO credit_limit;
    END IF;
END $$;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = 'requests'
          AND column_name = 'sale'
    ) AND NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = 'requests'
          AND column_name = 'credits'
    ) THEN
        ALTER TABLE requests RENAME COLUMN sale TO credits;
    END IF;
END $$;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = 'usage_ledger'
          AND column_name = 'sale'
    ) AND NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = 'usage_ledger'
          AND column_name = 'credits'
    ) THEN
        ALTER TABLE usage_ledger RENAME COLUMN sale TO credits;
    END IF;
END $$;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = 'aggregates'
          AND column_name = 'sale'
    ) AND NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = 'aggregates'
          AND column_name = 'credits'
    ) THEN
        ALTER TABLE aggregates RENAME COLUMN sale TO credits;
    END IF;
END $$;
