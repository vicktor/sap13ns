-- What the AI did each day, by feature (summary, image, design, assistant…),
-- kind of call (text or image), provider, model and where the AI came from
-- (own: the site's key; network: the plan's), for the usage statistics.
-- ai_usage keeps the monthly totals per user that the budgets read.
CREATE TABLE ai_calls (
	day           date NOT NULL,
	feature       text NOT NULL CHECK (length(feature) <= 40),
	kind          text NOT NULL CHECK (kind IN ('text', 'image')),
	provider      text NOT NULL CHECK (length(provider) <= 40),
	model         text NOT NULL CHECK (length(model) <= 120),
	source        text NOT NULL CHECK (source IN ('own', 'network')),
	requests      integer NOT NULL DEFAULT 0,
	input_tokens  bigint NOT NULL DEFAULT 0,
	output_tokens bigint NOT NULL DEFAULT 0,
	images        integer NOT NULL DEFAULT 0,
	PRIMARY KEY (day, feature, kind, provider, model, source)
);
