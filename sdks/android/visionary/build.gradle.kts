import org.jetbrains.kotlin.gradle.dsl.JvmTarget

plugins {
    id("com.android.library")
    id("org.jetbrains.kotlin.android")
    `maven-publish`
}

group = "io.github.styropyr0"
// JitPack sets VERSION to the tag being built (v0.3.0 -> 0.3.0); local builds are "dev".
version = System.getenv("VERSION")?.removePrefix("v")?.takeIf { it.isNotBlank() } ?: "dev"

android {
    namespace = "io.github.styropyr0.visionary"
    compileSdk = 35

    defaultConfig {
        minSdk = 24
        consumerProguardFiles("consumer-rules.pro")
    }

    buildTypes {
        release {
            isMinifyEnabled = false
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_11
        targetCompatibility = JavaVersion.VERSION_11
    }

    publishing {
        singleVariant("release") {
            withSourcesJar()
        }
    }
}

kotlin {
    explicitApi()
    compilerOptions {
        jvmTarget.set(JvmTarget.JVM_11)
    }
}

dependencies {
    implementation("androidx.annotation:annotation:1.9.1")
    // The app brings its own OkHttp; VisionaryInterceptor is only loaded when it is used.
    compileOnly("com.squareup.okhttp3:okhttp:4.12.0")
}

publishing {
    publications {
        register<MavenPublication>("release") {
            groupId = "io.github.styropyr0"
            artifactId = "visionary-android"
            version = project.version.toString()
            afterEvaluate { from(components["release"]) }
        }
    }
}
