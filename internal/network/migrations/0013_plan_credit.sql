-- A paid plan may leave the "Powered by sap13ns" credit out of its sites'
-- pages while their subscription holds them. A super administrator turns
-- it on in Network → Plans; off by default, as CPAL-1.0 (Exhibit B) asks
-- every operator but the licensor, or one it licenses, to keep it.
ALTER TABLE plans ADD COLUMN hide_credit boolean NOT NULL DEFAULT false;
