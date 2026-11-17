-- Protect snapshots held by different server processes from overwriting each other.
CREATE TABLE catalog_revision (id boolean PRIMARY KEY DEFAULT true CHECK (id), version bigint NOT NULL DEFAULT 0);
INSERT INTO catalog_revision(id, version) VALUES (true, 0);
