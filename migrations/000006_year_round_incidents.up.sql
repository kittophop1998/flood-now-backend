-- Year-round incident categories and category-specific details. Additive
-- only: no stored report is rewritten, and legacy categories stay valid.

ALTER TABLE reports DROP CONSTRAINT reports_type_check;
ALTER TABLE reports ADD CONSTRAINT reports_type_check CHECK (type IN (
    'flooded', 'road_closed', 'accident', 'vehicle_stalled', 'obstruction',
    'power_outage', 'help_needed', 'shelter', 'aid_point', 'other',
    'road_damage', 'construction', 'traffic_signal_issue'
));

-- Category-specific fields (lanes blocked, closure type, damage type…) as a
-- flat object of enum values, validated by the API per category
-- (apps/api/internal/domain/report/details.go). NULL for every existing row.
ALTER TABLE reports ADD COLUMN details jsonb
    CHECK (details IS NULL OR jsonb_typeof(details) = 'object');

-- Official announcements for year-round use.
ALTER TABLE announcements DROP CONSTRAINT announcements_type_check;
ALTER TABLE announcements ADD CONSTRAINT announcements_type_check CHECK (type IN (
    'flood_warning', 'evacuation', 'road_closure', 'water_release', 'weather', 'shelter_info',
    'construction', 'safety_notice', 'general'
));
