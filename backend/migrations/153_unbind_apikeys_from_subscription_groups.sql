-- Burn-down: subscription money is granted to users.balance and spent through standard
-- channels; subscription-type groups are no longer bindable to API keys. Unbind any key
-- still pointing at a subscription-type group so users re-select a standard channel.
UPDATE api_keys
SET group_id = NULL
WHERE group_id IN (SELECT id FROM groups WHERE subscription_type = 'subscription');
