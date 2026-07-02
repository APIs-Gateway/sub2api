-- Normalize legacy burn-down overdraft settings after tightening the user-facing
-- value domain: NULL/0/negative = off, 1..5 = enabled depth.
UPDATE user_subscriptions
SET max_overdraft_days = NULL
WHERE max_overdraft_days IS NOT NULL
  AND max_overdraft_days <= 0;

UPDATE user_subscriptions
SET max_overdraft_days = 5
WHERE max_overdraft_days > 5;

UPDATE users u
SET subscription_overdraft_guard = EXISTS (
    SELECT 1
    FROM user_subscriptions us
    WHERE us.user_id = u.id
      AND us.status = 'active'
      AND us.max_overdraft_days IS NOT NULL
      AND us.total_overdraft_count < 5
);
