# Zen OTA — module-local ProGuard/R8 rules (used when building this module).
#
# The library performs no reflection and no JNI; the default AGP rules are
# sufficient. The block below mirrors consumer-rules.pro so a standalone
# module build (e.g. `./gradlew :zen-android:assembleRelease`) is reproducible.

-keep class ota.zen.android.** { *; }

-keepclassmembers enum ota.zen.android.** {
    public static **[] values();
    public static ** valueOf(java.lang.String);
}

-dontwarn org.json.**
