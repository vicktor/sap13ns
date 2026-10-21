-- A Business subscription pays for two sites: the second one is added
-- from My sites (Network.AddGrantSite) without another payment.
UPDATE plans SET sites_per_grant = 2 WHERE slug = 'business' AND sites_per_grant = 1;
