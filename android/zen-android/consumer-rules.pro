# Zen OTA — consumer ProGuard/R8 rules.
# These rules ship with the AAR and are applied automatically to apps that
# depend on this library (consumerProguardFiles).

# Keep the entire Zen OTA public API surface (object, enums, data class,
# listeners). The SDK performs no reflection over its own classes, but keeping
# them guarantees stable names for consumers that serialize ZenConfig or
# reference enum values via reflection.
-keep class ota.zen.android.** { *; }

# Keep enum valueOf()/values() (standard enum rule, scoped to the SDK).
-keepclassmembers enum ota.zen.android.** {
    public static **[] values();
    public static ** valueOf(java.lang.String);
}

# org.json is part of the Android platform (android.jar), never packaged;
# silence hypothetical R8 warnings about it.
-dontwarn org.json.**

# The SDK talks to java.net.HttpURLConnection and android.content.pm.PackageInstaller
# directly; no extra keep rules are required for framework classes.
