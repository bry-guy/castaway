-- Episodes can be scored from the Survivor wiki first; a later survivoR import replaces a wiki one.
ALTER TABLE episode_imports DROP CONSTRAINT episode_imports_source_check;
ALTER TABLE episode_imports ADD CONSTRAINT episode_imports_source_check CHECK (source IN ('survivor', 'manual', 'wiki'));
