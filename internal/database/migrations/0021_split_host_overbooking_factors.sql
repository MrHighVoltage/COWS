ALTER TABLE host_settings ADD COLUMN cpu_overbooking_factor REAL NOT NULL DEFAULT 1.0 CHECK (cpu_overbooking_factor >= 0.1 AND cpu_overbooking_factor <= 1000.0);
ALTER TABLE host_settings ADD COLUMN memory_overbooking_factor REAL NOT NULL DEFAULT 1.0 CHECK (memory_overbooking_factor >= 0.1 AND memory_overbooking_factor <= 1000.0);

UPDATE host_settings
SET cpu_overbooking_factor = CASE
        WHEN overbooking_factor < 0.1 THEN 0.1
        WHEN overbooking_factor > 1000.0 THEN 1000.0
        ELSE overbooking_factor
    END,
    memory_overbooking_factor = CASE
        WHEN overbooking_factor < 0.1 THEN 0.1
        WHEN overbooking_factor > 1000.0 THEN 1000.0
        ELSE overbooking_factor
    END;
