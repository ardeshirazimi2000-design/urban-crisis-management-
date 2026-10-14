// Product identity shown on the sign-in screens: logo, name and slogan.
import 'package:flutter/material.dart';

class BrandHeader extends StatelessWidget {
  final String? subtitle;
  const BrandHeader({super.key, this.subtitle});

  @override
  Widget build(BuildContext context) {
    final dark = Theme.of(context).brightness == Brightness.dark;
    final text = Theme.of(context).textTheme;
    return Padding(
      padding: const EdgeInsets.only(top: 8, bottom: 20),
      child: Column(children: [
        Image.asset(dark ? 'assets/brand/mark-dark.png' : 'assets/brand/mark.png', width: 96, height: 96),
        const SizedBox(height: 8),
        Text('همراه پاد',
            style: text.headlineSmall?.copyWith(
                fontWeight: FontWeight.w800, color: dark ? Colors.white : const Color(0xFF17365D))),
        Text('آماده در هر لحظه', style: text.bodyMedium?.copyWith(color: Colors.grey)),
        if (subtitle != null) ...[const SizedBox(height: 4), Text(subtitle!, style: text.bodySmall)],
      ]),
    );
  }
}
