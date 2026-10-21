-- The AI the network's provider gives each plan, per site and day
-- (SAP13NS_NETWORK_AI_*): text requests and generated images. 0 is none.
ALTER TABLE plans ADD COLUMN ai_requests_per_day integer NOT NULL DEFAULT 0 CHECK (ai_requests_per_day >= 0),
	ADD COLUMN ai_images_per_day integer NOT NULL DEFAULT 0 CHECK (ai_images_per_day >= 0);
UPDATE plans SET ai_requests_per_day = 200, ai_images_per_day = 3 WHERE slug = 'pro';
UPDATE plans SET ai_requests_per_day = 1000, ai_images_per_day = 15 WHERE slug = 'business';
