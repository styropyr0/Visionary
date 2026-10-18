import com.vanniktech.maven.publish.MavenPublishBaseExtension
import org.jetbrains.kotlin.gradle.dsl.JvmTarget

plugins {
    id("com.android.library")
    id("org.jetbrains.kotlin.android")
    `maven-publish`
    // Base plugin only: it adds the Maven Central repository, POM and signing to the "release"
    // publication below and leaves that publication (and JitPack's task) as it is.
    id("com.vanniktech.maven.publish.base")
}

group = "io.github.styropyr0"
// JitPack and .github/workflows/sdks.yml set VERSION to the tag being built (v0.3.0 -> 0.3.0);
// local builds are "dev".
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
            // Maven Central requires a -javadoc.jar.
            withJavadocJar()
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

// Maven Central (Central Portal). Used by .github/workflows/sdks.yml:
//   ./gradlew :visionary:publishAllPublicationsToMavenCentralRepository
// with ORG_GRADLE_PROJECT_mavenCentralUsername / ORG_GRADLE_PROJECT_mavenCentralPassword (a Central
// Portal user token) and ORG_GRADLE_PROJECT_signingInMemoryKey / ORG_GRADLE_PROJECT_signingInMemoryKeyPassword
// (an ASCII-armored GPG private key). Without a key nothing is signed, so local and JitPack
// builds (publishReleasePublicationToMavenLocal) need no GPG setup.
extensions.configure<MavenPublishBaseExtension> {
    publishToMavenCentral(automaticRelease = true)
    if (!providers.gradleProperty("signingInMemoryKey").orNull.isNullOrBlank()) {
        signAllPublications()
    }
    pom {
        name.set("Visionary for Android")
        description.set(
            "Visionary for Android: screen load times, API call timings, handled errors, crashes " +
                "and custom events, reported to a Visionary server.",
        )
        inceptionYear.set("2026")
        url.set("https://github.com/styropyr0/Visionary/tree/main/sdks/android")
        licenses {
            license {
                name.set("Apache-2.0")
                url.set("https://www.apache.org/licenses/LICENSE-2.0.txt")
                distribution.set("repo")
            }
        }
        developers {
            developer {
                id.set("styropyr0")
                name.set("styropyr0")
                url.set("https://github.com/styropyr0")
            }
        }
        scm {
            url.set("https://github.com/styropyr0/Visionary/tree/main/sdks/android")
            connection.set("scm:git:https://github.com/styropyr0/Visionary.git")
            developerConnection.set("scm:git:ssh://git@github.com/styropyr0/Visionary.git")
        }
    }
}
