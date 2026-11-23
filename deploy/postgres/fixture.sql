CREATE TABLE flagguard_catalog (id integer PRIMARY KEY, title text NOT NULL);
INSERT INTO flagguard_catalog SELECT id, repeat('catalog item ', 12) FROM generate_series(1, 40000) AS id;
ANALYZE flagguard_catalog;
