/*
 * Zen OTA — Android library module (AAR).
 *
 * The only source file is src/main/kotlin/ota/zen/android/ZenUpdater.kt — a
 * byte-identical copy of ../../dropin/ZenUpdater.kt (kept in sync on purpose).
 * The module has ZERO runtime dependencies; it compiles against the Android
 * SDK alone (android.*, org.json, JDK, Kotlin stdlib).
 *
 * Plugin versions are NOT declared here: the host app's root build.gradle.kts
 * puts com.android.library / org.jetbrains.kotlin.android on the classpath,
 * then includes this module (see README.md).
 */

plugins {
    id("com.android.library")
    id("org.jetbrains.kotlin.android")
    id("maven-publish")
}

android {
    namespace = "ota.zen.android"
    compileSdk = 34

    defaultConfig {
        minSdk = 21
        consumerProguardFiles("consumer-rules.pro")
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            proguardFiles(
                getDefaultProguardFile("proguard-android-optimize.txt"),
                "proguard-rules.pro"
            )
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    kotlinOptions {
        jvmTarget = "17"
    }

    sourceSets {
        getByName("main") {
            kotlin.srcDir("src/main/kotlin")
        }
    }
}

dependencies {
    // Intentionally empty: ZenUpdater.kt is dependency-free by design.
    // No compileOnly, no implementation, no api.
}

// Publishing placeholder — wire up to Maven Central / JitPack / a private
// repository later by configuring the repository block in the consuming
// project's settings or root build file.
afterEvaluate {
    publishing {
        publications {
            create<MavenPublication>("release") {
                from(components["release"])
                groupId = "app.zen.ota"
                artifactId = "zen-android"
                version = "0.1.0"
                pom {
                    name.set("Zen OTA Android SDK")
                    description.set("In-app auto-update library for Zen OTA: check, resumable download, silent or prompted install.")
                    url.set("https://github.com/zen-ota/zen-ota")
                }
            }
        }
    }
}
