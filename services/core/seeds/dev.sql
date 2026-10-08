-- Local demo seed (idempotent). Fictional sample data for a Tehran earthquake exercise.
INSERT INTO organizations (code, name) VALUES
  ('command',      'مرکز فرماندهی بحران (نمونه)'),
  ('fire',         'آتش‌نشانی (نمونه)'),
  ('ems',          'اورژانس (نمونه)'),
  ('municipality', 'شهرداری (نمونه)'),
  ('police',       'پلیس (نمونه)'),
  ('redcrescent',  'هلال‌احمر (نمونه)')
ON CONFLICT (code) DO NOTHING;

-- Reference layers (fictional sample facilities, verified "now" for the demo).
INSERT INTO gis_features (id, layer, name, geom, properties, owner_org_id, source, verified_at)
SELECT v.id::uuid, v.layer, v.name, ST_SetSRID(ST_MakePoint(v.lng, v.lat), 4326)::geography, v.props::jsonb,
       (SELECT id FROM organizations WHERE code = v.org), 'seed:demo', now()
FROM (VALUES
  ('a0000000-0000-0000-0000-000000000001','hospital','بیمارستان نمونه ۱ (ساختگی)',35.7219,51.3347,'{"beds":300}','ems'),
  ('a0000000-0000-0000-0000-000000000002','hospital','بیمارستان نمونه ۲ (ساختگی)',35.7004,51.4163,'{"beds":220}','ems'),
  ('a0000000-0000-0000-0000-000000000003','hospital','بیمارستان نمونه ۳ (ساختگی)',35.7623,51.4470,'{"beds":180}','ems'),
  ('a0000000-0000-0000-0000-000000000004','fire_station','ایستگاه آتش‌نشانی نمونه ۱',35.6892,51.3890,'{}','fire'),
  ('a0000000-0000-0000-0000-000000000005','fire_station','ایستگاه آتش‌نشانی نمونه ۲',35.7400,51.3600,'{}','fire'),
  ('a0000000-0000-0000-0000-000000000006','shelter','محل اسکان اضطراری نمونه ۱',35.7120,51.4030,'{"capacity":1500}','redcrescent'),
  ('a0000000-0000-0000-0000-000000000007','shelter','محل اسکان اضطراری نمونه ۲',35.6700,51.4300,'{"capacity":900}','redcrescent'),
  ('a0000000-0000-0000-0000-000000000008','assembly_point','نقطه تجمع نمونه (پارک)',35.7550,51.4100,'{}','municipality')
) AS v(id, layer, name, lat, lng, props, org)
ON CONFLICT (id) DO NOTHING;

INSERT INTO gis_features (id, layer, name, geom, properties, owner_org_id, source, verified_at, valid_until)
SELECT 'a0000000-0000-0000-0000-000000000010'::uuid, 'road_closure', 'انسداد نمونه - محور شمالی',
       ST_SetSRID(ST_GeomFromText('LINESTRING(51.3950 35.7300, 51.4050 35.7350)'), 4326)::geography,
       '{"reason":"آوار (تمرینی)"}'::jsonb, (SELECT id FROM organizations WHERE code='municipality'),
       'seed:demo', now() - interval '3 days', now() + interval '30 days'
ON CONFLICT (id) DO NOTHING;

INSERT INTO resources (id, organization_id, type, name, status, capabilities, capacity, location, last_seen_at)
SELECT v.id::uuid, (SELECT id FROM organizations WHERE code = v.org), v.type, v.name, 'available', v.caps::jsonb, v.cap,
       ST_SetSRID(ST_MakePoint(v.lng, v.lat), 4326)::geography, now() - (v.age || ' minutes')::interval
FROM (VALUES
  ('b0000000-0000-0000-0000-000000000001','ems','ambulance','آمبولانس ۱۱۵-۰۱',35.7010,51.4100,'{"als":true}',2,'1'),
  ('b0000000-0000-0000-0000-000000000002','ems','ambulance','آمبولانس ۱۱۵-۰۲',35.7150,51.3900,'{"als":false}',2,'4'),
  ('b0000000-0000-0000-0000-000000000003','ems','ambulance','آمبولانس ۱۱۵-۰۳',35.7300,51.4200,'{"als":true}',2,'45'),
  ('b0000000-0000-0000-0000-000000000004','fire','fire_truck','خودرو آتش‌نشانی ۱۲۵-۰۱',35.6895,51.3895,'{"ladder_m":32}',6,'2'),
  ('b0000000-0000-0000-0000-000000000005','fire','rescue_team','تیم آواربرداری ۱',35.7405,51.3605,'{"usar":"medium"}',8,'3'),
  ('b0000000-0000-0000-0000-000000000006','redcrescent','rescue_team','تیم امداد هلال‌احمر ۱',35.7125,51.4035,'{"usar":"light"}',10,'6'),
  ('b0000000-0000-0000-0000-000000000007','redcrescent','shelter','کمپ اسکان نمونه ۱',35.7120,51.4030,'{}',1500,'0'),
  ('b0000000-0000-0000-0000-000000000008','police','police_unit','واحد انتظامی ۱۱۰-۰۱',35.7000,51.3700,'{}',4,'8')
) AS v(id, org, type, name, lat, lng, caps, cap, age)
ON CONFLICT (id) DO NOTHING;
