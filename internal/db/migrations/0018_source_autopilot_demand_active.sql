-- Automatic demand eligibility is separate from an explicit worker pause.
ALTER TABLE source_autopilot_jobs ADD COLUMN demand_active boolean NOT NULL DEFAULT true;
