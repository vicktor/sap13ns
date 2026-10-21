-- The site's daily use of the network's AI (store.AISourceNetwork), which
-- the plan limits per day: text requests (the writing assistant, design
-- with AI and the site assistant's answers) and generated images.
CREATE TABLE ai_network_usage (
	day      date PRIMARY KEY,
	requests integer NOT NULL DEFAULT 0,
	images   integer NOT NULL DEFAULT 0
);
