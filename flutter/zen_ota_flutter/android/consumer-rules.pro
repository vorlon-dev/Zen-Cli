# Keep the Zen OTA SDK surface (accessed via reflection-free platform channels,
# but keep everything for safety across R8 full modes).
-keep class ota.zen.android.** { *; }
-keep class ota.zen.flutter.** { *; }
-keepclassmembers enum ota.zen.android.** {
    public static **[] values();
    public static ** valueOf(java.lang.String);
}
-dontwarn org.json.**
