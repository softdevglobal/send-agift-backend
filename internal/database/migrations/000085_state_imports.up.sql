-- When a country's states were last fetched from Google Places.
--
-- The first customer to need a country's states triggers one import, which
-- fills core.states; after that the list is ours and Google is not asked
-- again. A country Google has no states for is still marked, so it is not
-- asked over and over.
CREATE TABLE core.state_imports (
    country_id  uuid PRIMARY KEY REFERENCES core.countries(id) ON DELETE CASCADE,
    imported_at timestamptz NOT NULL DEFAULT now(),
    found       integer NOT NULL DEFAULT 0
);
