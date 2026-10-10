/// Wire models mirroring contracts/openapi/core-api.v1.yaml.
class GeoLocation {
  final double lat;
  final double lng;
  final double accuracyM;
  final String source; // gps | network | manual

  const GeoLocation({required this.lat, required this.lng, required this.accuracyM, this.source = 'gps'});

  Map<String, dynamic> toJson() => {'lat': lat, 'lng': lng, 'accuracy_m': accuracyM, 'source': source};

  factory GeoLocation.fromJson(Map<String, dynamic> j) => GeoLocation(
        lat: (j['lat'] as num).toDouble(),
        lng: (j['lng'] as num).toDouble(),
        accuracyM: (j['accuracy_m'] as num).toDouble(),
        source: (j['source'] as String?) ?? 'gps',
      );
}

const reportTypes = <String, String>{
  'trapped_people': 'افراد محبوس / زیر آوار',
  'building_collapse': 'ریزش ساختمان',
  'structural_damage': 'آسیب سازه‌ای (ترک، ریزش دیوار)',
  'injury': 'مصدوم',
  'fire': 'آتش‌سوزی',
  'gas_leak': 'نشت گاز',
  'road_blocked': 'انسداد مسیر',
  'flooding': 'آب‌گرفتگی',
  'power_outage': 'قطع برق',
  'water_outage': 'قطع آب',
  'landslide': 'رانش زمین',
  'hazmat': 'مواد خطرناک',
  'other': 'سایر',
};

class ReportSubmission {
  final String type;
  final String description;
  final GeoLocation location;

  /// Time the citizen says it happened (device clock; not authoritative).
  final DateTime? occurredAt;

  const ReportSubmission({required this.type, required this.description, required this.location, this.occurredAt});

  Map<String, dynamic> toJson() => {
        'type': type,
        'description': description,
        'location': location.toJson(),
        if (occurredAt != null) 'occurred_at': occurredAt!.toUtc().toIso8601String(),
      };
}

class ReportReceipt {
  final String reportId;
  final String status;
  final DateTime receivedAt;
  final String correlationId;

  const ReportReceipt(this.reportId, this.status, this.receivedAt, this.correlationId);

  factory ReportReceipt.fromJson(Map<String, dynamic> j) => ReportReceipt(
      j['report_id'] as String, j['status'] as String, DateTime.parse(j['received_at'] as String), (j['correlation_id'] as String?) ?? '');
}

class MyReport {
  final String id;
  final String type;
  final String status;
  final DateTime receivedAt;

  const MyReport(this.id, this.type, this.status, this.receivedAt);

  factory MyReport.fromJson(Map<String, dynamic> j) =>
      MyReport(j['id'] as String, j['type'] as String, j['status'] as String, DateTime.parse(j['received_at'] as String));
}

class PublicAlert {
  final String id;
  final String severity;
  final String text;
  final String issuer;
  final DateTime? issuedAt;
  final DateTime expiresAt;
  final String regionLabel;

  const PublicAlert(this.id, this.severity, this.text, this.issuer, this.issuedAt, this.expiresAt, this.regionLabel);

  factory PublicAlert.fromJson(Map<String, dynamic> j) => PublicAlert(
        j['id'] as String,
        j['severity'] as String,
        j['text'] as String,
        j['issuer'] as String,
        j['issued_at'] == null ? null : DateTime.parse(j['issued_at'] as String),
        DateTime.parse(j['expires_at'] as String),
        j['region_label'] as String,
      );

  Map<String, dynamic> toJson() => {
        'id': id, 'severity': severity, 'text': text, 'issuer': issuer,
        'issued_at': issuedAt?.toIso8601String(), 'expires_at': expiresAt.toIso8601String(), 'region_label': regionLabel,
      };
}

class Assignment {
  final String id;
  final String incidentId;
  final String incidentCode;
  final String resourceId;
  final String resourceName;
  final String status;
  final String reason;
  final DateTime assignedAt;
  final int version;

  const Assignment({required this.id, required this.incidentId, required this.incidentCode, required this.resourceId,
      required this.resourceName, required this.status, required this.reason, required this.assignedAt, required this.version});

  factory Assignment.fromJson(Map<String, dynamic> j) => Assignment(
        id: j['id'] as String,
        incidentId: j['incident_id'] as String,
        incidentCode: j['incident_code'] as String,
        resourceId: j['resource_id'] as String,
        resourceName: j['resource_name'] as String,
        status: j['status'] as String,
        reason: (j['reason'] as String?) ?? '',
        assignedAt: DateTime.parse(j['assigned_at'] as String),
        version: j['version'] as int,
      );

  Map<String, dynamic> toJson() => {
        'id': id, 'incident_id': incidentId, 'incident_code': incidentCode, 'resource_id': resourceId,
        'resource_name': resourceName, 'status': status, 'reason': reason, 'assigned_at': assignedAt.toIso8601String(),
        'version': version,
      };

  bool get isActive => const ['proposed', 'assigned', 'acknowledged', 'en_route', 'on_scene'].contains(status);

  /// Next statuses a responder may set (mirrors the server's assignment lifecycle).
  List<String> get nextStatuses => switch (status) {
        'assigned' => ['acknowledged', 'en_route'],
        'acknowledged' => ['en_route'],
        'en_route' => ['on_scene'],
        'on_scene' => ['completed'],
        _ => <String>[],
      };
}

const assignmentStatusLabels = <String, String>{
  'proposed': 'پیشنهادی', 'assigned': 'تخصیص‌یافته', 'acknowledged': 'دریافت شد', 'en_route': 'در مسیر',
  'on_scene': 'در محل', 'completed': 'تکمیل', 'cancelled': 'لغو',
};

/// A shelter that could take people when the list was fetched (counts are not a reservation).
class PublicShelter {
  final String id;
  final String name;
  final String organization;
  final double lat;
  final double lng;
  final double distanceM;
  final int available;
  final int capacity;
  final DateTime? updatedAt;

  const PublicShelter(this.id, this.name, this.organization, this.lat, this.lng, this.distanceM, this.available,
      this.capacity, this.updatedAt);

  factory PublicShelter.fromJson(Map<String, dynamic> j) {
    final loc = (j['location'] as Map).cast<String, dynamic>();
    return PublicShelter(
      j['id'] as String,
      j['name'] as String,
      j['organization'] as String? ?? '',
      (loc['lat'] as num).toDouble(),
      (loc['lng'] as num).toDouble(),
      (j['distance_m'] as num).toDouble(),
      j['available'] as int,
      j['capacity'] as int,
      j['updated_at'] == null ? null : DateTime.parse(j['updated_at'] as String),
    );
  }

  Map<String, dynamic> toJson() => {
        'id': id, 'name': name, 'organization': organization, 'location': {'lat': lat, 'lng': lng},
        'distance_m': distanceM, 'available': available, 'capacity': capacity, 'updated_at': updatedAt?.toIso8601String(),
      };
}
