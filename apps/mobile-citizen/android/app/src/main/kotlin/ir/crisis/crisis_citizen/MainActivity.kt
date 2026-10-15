package ir.crisis.crisis_citizen

import android.content.pm.PackageManager
import android.os.Build
import io.flutter.embedding.android.FlutterActivity
import io.flutter.embedding.engine.FlutterEngine
import io.flutter.plugin.common.MethodChannel

class MainActivity : FlutterActivity() {
    override fun configureFlutterEngine(flutterEngine: FlutterEngine) {
        super.configureFlutterEngine(flutterEngine)
        // Bridge for the background notification service (see PushService).
        MethodChannel(flutterEngine.dartExecutor.binaryMessenger, "crisis/push").setMethodCallHandler { call, result ->
            when (call.method) {
                "start" -> {
                    val url = call.argument<String>("url")
                    val topic = call.argument<String>("topic")
                    if (url == null || topic == null) {
                        result.error("ARGS", "url and topic are required", null)
                    } else {
                        askNotificationPermission()
                        PushService.start(this, url, topic, call.argument<String>("ongoing") ?: "")
                        result.success(true)
                    }
                }
                "stop" -> {
                    PushService.stop(this)
                    result.success(true)
                }
                else -> result.notImplemented()
            }
        }
    }

    private fun askNotificationPermission() {
        if (Build.VERSION.SDK_INT >= 33 &&
            checkSelfPermission("android.permission.POST_NOTIFICATIONS") != PackageManager.PERMISSION_GRANTED
        ) {
            requestPermissions(arrayOf("android.permission.POST_NOTIFICATIONS"), 41)
        }
    }
}
