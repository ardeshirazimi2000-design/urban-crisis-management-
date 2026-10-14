// Line icons (Lucide, ISC/MIT) shared in look with the web console; the icon font is bundled in the APK.
import 'package:flutter/widgets.dart';
import 'package:lucide_icons_flutter/lucide_icons.dart';

abstract final class AppIcons {
  static const IconData call = LucideIcons.phone;
  static const IconData campaign = LucideIcons.megaphone;
  static const IconData checkCircleOutline = LucideIcons.circleCheck;
  static const IconData close = LucideIcons.x;
  static const IconData copy = LucideIcons.copy;
  static const IconData directions = LucideIcons.navigation;
  static const IconData errorOutline = LucideIcons.circleAlert;
  static const IconData history = LucideIcons.history;
  static const IconData locationOff = LucideIcons.mapPinOff;
  static const IconData locationOn = LucideIcons.mapPin;
  static const IconData logout = LucideIcons.logOut;
  static const IconData myLocation = LucideIcons.locateFixed;
  static const IconData nightShelter = LucideIcons.tent;
  static const IconData notificationsActive = LucideIcons.bellRing;
  static const IconData refresh = LucideIcons.refreshCw;
  static const IconData schedule = LucideIcons.clock;
  static const IconData scheduleSend = LucideIcons.clockArrowUp;
  static const IconData send = LucideIcons.send;
  static const IconData supportAgent = LucideIcons.headset;
  static const IconData sync = LucideIcons.refreshCw;
  static const IconData verifiedUser = LucideIcons.shieldCheck;
  static const IconData warningAmber = LucideIcons.triangleAlert;
  static const IconData wifiOff = LucideIcons.wifiOff;
  /// Report types, matching the console.
  static IconData reportType(String type) => switch (type) {
        'structural_damage' => LucideIcons.building2,
        'building_collapse' => LucideIcons.construction,
        'trapped_people' => LucideIcons.personStanding,
        'injury' => LucideIcons.stethoscope,
        'fire' => LucideIcons.flame,
        'gas_leak' => LucideIcons.wind,
        'road_blocked' => LucideIcons.triangleAlert,
        'flooding' => LucideIcons.waves,
        'power_outage' => LucideIcons.zap,
        'water_outage' => LucideIcons.droplets,
        'landslide' || 'earthquake' || 'aftershock' => LucideIcons.mountain,
        'hazmat' => LucideIcons.biohazard,
        _ => LucideIcons.circleAlert,
      };
}
